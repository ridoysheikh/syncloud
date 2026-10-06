package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// User kinds.
const (
	UserHuman   = "user"
	UserService = "service" // service account: keys and tokens only, no console login
)

// IAMUser is a user with its IAM fields.
type IAMUser struct {
	User
	Kind        string
	Disabled    bool
	MFAEnabled  bool
	MFASecret   []byte // sealed
	LastLoginAt *time.Time
	Groups      []string // group IDs
}

const iamUserCols = `SELECT id, email, name, password_hash, is_root, created_at, kind, disabled, mfa_enabled, mfa_secret_enc, last_login_at FROM users`

func scanIAMUser(r scanner) (IAMUser, error) {
	var u IAMUser
	var created int64
	var last sql.NullInt64
	err := r.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsRoot, &created, &u.Kind, &u.Disabled, &u.MFAEnabled, &u.MFASecret, &last)
	u.CreatedAt = time.Unix(created, 0).UTC()
	u.LastLoginAt = timePtr(last)
	return u, err
}

func (s *Store) ListIAMUsers(ctx context.Context) ([]IAMUser, error) {
	rows, err := s.R.QueryContext(ctx, iamUserCols+` ORDER BY is_root DESC, email`)
	if err != nil {
		return nil, err
	}
	var out []IAMUser
	for rows.Next() {
		u, err := scanIAMUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := s.groupMemberships(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Groups = members[out[i].ID]
	}
	return out, nil
}

func (s *Store) IAMUser(ctx context.Context, id string) (IAMUser, error) {
	u, err := scanIAMUser(s.R.QueryRowContext(ctx, iamUserCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	members, err := s.groupMemberships(ctx)
	u.Groups = members[u.ID]
	return u, err
}

func (s *Store) groupMemberships(ctx context.Context) (map[string][]string, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT user_id, group_id FROM iam_group_members ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var u, g string
		if err := rows.Scan(&u, &g); err != nil {
			return nil, err
		}
		out[u] = append(out[u], g)
	}
	return out, rows.Err()
}

// CreateUser adds a user or service account. ErrNameTaken: the email is used.
func (s *Store) CreateUser(ctx context.Context, u IAMUser) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO users (id, email, name, password_hash, is_root, created_at, kind) VALUES (?, ?, ?, ?, 0, ?, ?)`,
		u.ID, u.Email, u.Name, u.PasswordHash, u.CreatedAt.Unix(), u.Kind)
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

// UpdateUser changes a user's name, disabled flag and (when set) password.
func (s *Store) UpdateUser(ctx context.Context, id, name string, disabled bool, passwordHash string) error {
	q, args := `UPDATE users SET name = ?, disabled = ?`, []any{name, disabled}
	if passwordHash != "" {
		q += `, password_hash = ?`
		args = append(args, passwordHash)
	}
	res, err := s.W.ExecContext(ctx, q+` WHERE id = ? AND is_root = 0`, append(args, id)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if disabled {
		_, _ = s.W.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
	}
	return nil
}

// SetPassword changes any user's password (including root) and ends their sessions.
func (s *Store) SetPassword(ctx context.Context, id, hash string) error {
	if _, err := s.W.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
		return err
	}
	_, err := s.W.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
	return err
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND is_root = 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM iam_attachments WHERE principal_type = 'user' AND principal_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMFA stores (or clears, with nil) a user's sealed TOTP secret.
func (s *Store) SetMFA(ctx context.Context, id string, secret []byte, enabled bool) error {
	_, err := s.W.ExecContext(ctx, `UPDATE users SET mfa_secret_enc = ?, mfa_enabled = ? WHERE id = ?`, secret, enabled, id)
	return err
}

func (s *Store) TouchLogin(ctx context.Context, id string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

// ── groups ──────────────────────────────────────────────────────────────────

type IAMGroup struct {
	ID          string
	Name        string
	Description string
	Members     []string // user IDs
	CreatedAt   time.Time
}

func (s *Store) ListIAMGroups(ctx context.Context) ([]IAMGroup, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, description, created_at FROM iam_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var out []IAMGroup
	for rows.Next() {
		var g IAMGroup
		var at int64
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &at); err != nil {
			rows.Close()
			return nil, err
		}
		g.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, g)
	}
	rows.Close()
	m, err := s.R.QueryContext(ctx, `SELECT group_id, user_id FROM iam_group_members`)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	by := map[string][]string{}
	for m.Next() {
		var g, u string
		if err := m.Scan(&g, &u); err != nil {
			return nil, err
		}
		by[g] = append(by[g], u)
	}
	for i := range out {
		out[i].Members = by[out[i].ID]
	}
	return out, m.Err()
}

// PutIAMGroup creates or updates a group and replaces its members.
func (s *Store) PutIAMGroup(ctx context.Context, g IAMGroup) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO iam_groups (id, name, description, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description`, g.ID, g.Name, g.Description, g.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM iam_group_members WHERE group_id = ?`, g.ID); err != nil {
		return err
	}
	for _, u := range g.Members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO iam_group_members (group_id, user_id) VALUES (?, ?)`, g.ID, u); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteIAMGroup(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM iam_groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.W.ExecContext(ctx, `DELETE FROM iam_attachments WHERE principal_type = 'group' AND principal_id = ?`, id)
	return err
}

// ── policies and attachments ────────────────────────────────────────────────

type IAMPolicy struct {
	ID          string
	Name        string
	Description string
	Document    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s *Store) ListIAMPolicies(ctx context.Context) ([]IAMPolicy, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, description, document, created_at, updated_at FROM iam_policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IAMPolicy
	for rows.Next() {
		var p IAMPolicy
		var c, u int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Document, &c, &u); err != nil {
			return nil, err
		}
		p.CreatedAt, p.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PutIAMPolicy(ctx context.Context, p IAMPolicy) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO iam_policies (id, name, description, document, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, document = excluded.document, updated_at = excluded.updated_at`,
		p.ID, p.Name, p.Description, p.Document, p.CreatedAt.Unix(), p.UpdatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) DeleteIAMPolicy(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM iam_policies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.W.ExecContext(ctx, `DELETE FROM iam_attachments WHERE policy = ?`, id)
	return err
}

type Attachment struct {
	PrincipalType string `json:"principalType"`
	PrincipalID   string `json:"principalId"`
	Policy        string `json:"policy"`
}

func (s *Store) ListAttachments(ctx context.Context) ([]Attachment, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT principal_type, principal_id, policy FROM iam_attachments ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.PrincipalType, &a.PrincipalID, &a.Policy); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Attach(ctx context.Context, a Attachment, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `INSERT OR IGNORE INTO iam_attachments (principal_type, principal_id, policy, created_at) VALUES (?, ?, ?, ?)`,
		a.PrincipalType, a.PrincipalID, a.Policy, at.Unix())
	return err
}

func (s *Store) Detach(ctx context.Context, a Attachment) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM iam_attachments WHERE principal_type = ? AND principal_id = ? AND policy = ?`,
		a.PrincipalType, a.PrincipalID, a.Policy)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ── roles ───────────────────────────────────────────────────────────────────

// RoleTrust says who may assume a role.
type RoleTrust struct {
	Users      []string `json:"users"`  // user IDs
	Groups     []string `json:"groups"` // group IDs
	RequireMFA bool     `json:"requireMfa"`
}

type IAMRole struct {
	ID                string
	Name              string
	Description       string
	Trust             RoleTrust
	MaxSessionSeconds int
	CreatedAt         time.Time
}

func (s *Store) ListIAMRoles(ctx context.Context) ([]IAMRole, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, description, trust, max_session_seconds, created_at FROM iam_roles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IAMRole
	for rows.Next() {
		var r IAMRole
		var trust string
		var at int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &trust, &r.MaxSessionSeconds, &at); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(trust), &r.Trust)
		r.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) PutIAMRole(ctx context.Context, r IAMRole) error {
	trust, _ := json.Marshal(r.Trust)
	_, err := s.W.ExecContext(ctx, `INSERT INTO iam_roles (id, name, description, trust, max_session_seconds, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, trust = excluded.trust,
		  max_session_seconds = excluded.max_session_seconds`, r.ID, r.Name, r.Description, string(trust), r.MaxSessionSeconds, r.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) DeleteIAMRole(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM iam_roles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.W.ExecContext(ctx, `DELETE FROM iam_attachments WHERE principal_type = 'role' AND principal_id = ?`, id)
	return err
}

// ── temporary credentials ───────────────────────────────────────────────────

// Kinds of temporary credentials.
const (
	TempRole   = "role"   // sts assume-role
	TempDevice = "device" // synctl login
	TempShell  = "shell"  // Cloud Shell
)

type TempCredential struct {
	ID         string
	Kind       string
	UserID     string
	RoleID     string
	SecretEnc  []byte
	TokenHash  string
	MFA        bool
	SourceIP   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

func (s *Store) CreateTempCredential(ctx context.Context, c TempCredential) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO temp_credentials (id, kind, user_id, role_id, secret_enc, token_hash, mfa, source_ip, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, c.Kind, c.UserID, nullStr(c.RoleID), c.SecretEnc, c.TokenHash, c.MFA, c.SourceIP,
		c.CreatedAt.Unix(), c.ExpiresAt.Unix())
	if err == nil {
		_, _ = s.W.ExecContext(ctx, `DELETE FROM temp_credentials WHERE expires_at < ?`, c.CreatedAt.Add(-24*time.Hour).Unix())
	}
	return err
}

// TempCredentialByID returns ErrNotFound for unknown or expired credentials.
func (s *Store) TempCredentialByID(ctx context.Context, id string, now time.Time) (TempCredential, error) {
	var c TempCredential
	var created, expires int64
	var last sql.NullInt64
	var role sql.NullString
	err := s.R.QueryRowContext(ctx, `SELECT id, kind, user_id, role_id, secret_enc, token_hash, mfa, source_ip, created_at, expires_at, last_used_at
		FROM temp_credentials WHERE id = ? AND expires_at > ?`, id, now.Unix()).
		Scan(&c.ID, &c.Kind, &c.UserID, &role, &c.SecretEnc, &c.TokenHash, &c.MFA, &c.SourceIP, &created, &expires, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.RoleID = role.String
	c.CreatedAt, c.ExpiresAt, c.LastUsedAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC(), timePtr(last)
	return c, err
}

func (s *Store) TouchTempCredential(ctx context.Context, id string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE temp_credentials SET last_used_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

func (s *Store) DeleteTempCredential(ctx context.Context, id string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM temp_credentials WHERE id = ?`, id)
	return err
}

// ── device codes ────────────────────────────────────────────────────────────

type DeviceCode struct {
	DeviceHash string
	UserCode   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ApprovedBy string
	Credential string
}

func (s *Store) CreateDeviceCode(ctx context.Context, d DeviceCode) error {
	_, _ = s.W.ExecContext(ctx, `DELETE FROM device_codes WHERE expires_at < ?`, d.CreatedAt.Unix())
	_, err := s.W.ExecContext(ctx, `INSERT INTO device_codes (device_hash, user_code, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		d.DeviceHash, d.UserCode, d.CreatedAt.Unix(), d.ExpiresAt.Unix())
	return err
}

func (s *Store) deviceCode(ctx context.Context, where string, arg any, now time.Time) (DeviceCode, error) {
	var d DeviceCode
	var c, e int64
	var by, cred sql.NullString
	err := s.R.QueryRowContext(ctx, `SELECT device_hash, user_code, created_at, expires_at, approved_by, credential FROM device_codes WHERE `+where+` AND expires_at > ?`, arg, now.Unix()).
		Scan(&d.DeviceHash, &d.UserCode, &c, &e, &by, &cred)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.CreatedAt, d.ExpiresAt, d.ApprovedBy, d.Credential = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC(), by.String, cred.String
	return d, err
}

func (s *Store) DeviceCodeByHash(ctx context.Context, hash string, now time.Time) (DeviceCode, error) {
	return s.deviceCode(ctx, "device_hash = ?", hash, now)
}

func (s *Store) DeviceCodeByUserCode(ctx context.Context, code string, now time.Time) (DeviceCode, error) {
	return s.deviceCode(ctx, "user_code = ?", code, now)
}

// ApproveDeviceCode records the approving user and the credential issued.
func (s *Store) ApproveDeviceCode(ctx context.Context, hash, userID, credential string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE device_codes SET approved_by = ?, credential = ? WHERE device_hash = ? AND approved_by IS NULL`, userID, credential, hash)
	return err
}

// DeleteDeviceCode is called once the CLI has picked up its credential.
func (s *Store) DeleteDeviceCode(ctx context.Context, hash string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM device_codes WHERE device_hash = ?`, hash)
	return err
}

// ── audit queries ───────────────────────────────────────────────────────────

// AuditRecord is a stored audit event.
type AuditRecord struct {
	ID        int64          `json:"id"`
	At        time.Time      `json:"at"`
	ActorID   string         `json:"actorId"`
	Actor     string         `json:"actor"` // email, joined in
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	IP        string         `json:"ip"`
	UserAgent string         `json:"userAgent"`
	Detail    map[string]any `json:"detail"`
}

// AuditFilter selects events; empty fields match everything.
type AuditFilter struct {
	ActorID, Action, Resource, Text string
	Since, Until                    time.Time
	BeforeID                        int64 // paging: events older than this ID
}

func (s *Store) QueryAudit(ctx context.Context, f AuditFilter, limit int) ([]AuditRecord, error) {
	q := `SELECT a.id, a.at, coalesce(a.actor_id, ''), coalesce(u.email, ''), a.action, a.resource, a.ip, a.user_agent, a.detail
		FROM audit_events a LEFT JOIN users u ON u.id = a.actor_id WHERE 1 = 1`
	var args []any
	if f.ActorID != "" {
		q += ` AND a.actor_id = ?`
		args = append(args, f.ActorID)
	}
	if f.Action != "" {
		q += ` AND a.action LIKE ? ESCAPE '\'`
		args = append(args, likePattern(f.Action))
	}
	if f.Resource != "" {
		q += ` AND a.resource LIKE ? ESCAPE '\'`
		args = append(args, likePattern(f.Resource))
	}
	if f.Text != "" {
		q += ` AND (a.detail LIKE ? ESCAPE '\' OR a.resource LIKE ? ESCAPE '\' OR a.action LIKE ? ESCAPE '\' OR u.email LIKE ? ESCAPE '\' OR a.ip LIKE ? ESCAPE '\')`
		p := "%" + escapeLike(f.Text) + "%"
		args = append(args, p, p, p, p, p)
	}
	if !f.Since.IsZero() {
		q += ` AND a.at >= ?`
		args = append(args, f.Since.UnixMilli())
	}
	if !f.Until.IsZero() {
		q += ` AND a.at < ?`
		args = append(args, f.Until.UnixMilli())
	}
	if f.BeforeID > 0 {
		q += ` AND a.id < ?`
		args = append(args, f.BeforeID)
	}
	q += ` ORDER BY a.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRecord{}
	for rows.Next() {
		var r AuditRecord
		var at int64
		var detail string
		if err := rows.Scan(&r.ID, &at, &r.ActorID, &r.Actor, &r.Action, &r.Resource, &r.IP, &r.UserAgent, &detail); err != nil {
			return nil, err
		}
		r.At = time.UnixMilli(at).UTC()
		_ = json.Unmarshal([]byte(detail), &r.Detail)
		out = append(out, r)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	out := make([]rune, 0, len(s))
	for _, c := range s {
		if c == '%' || c == '_' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(out)
}

// likePattern turns a user pattern with * wildcards into a LIKE pattern.
func likePattern(s string) string {
	e := escapeLike(s)
	out := make([]rune, 0, len(e))
	for _, c := range e {
		if c == '*' {
			c = '%'
		}
		out = append(out, c)
	}
	return string(out)
}
