package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Database is a managed database (Phase 12). Spec, State and Network are
// JSON owned by package dbs; Secrets are sealed. A standalone database has
// no environment (EnvironmentID, Project and Environment are "").
type Database struct {
	ID            string
	EnvironmentID string
	Project       string // names, joined in
	Environment   string
	Name          string // unique in the cluster
	Engine        string
	Version       string
	Spec          string
	State         string
	Network       string
	Secrets       []byte
	Status        string
	Deleting      bool
	VIPRW, VIPRO  int // 0 = not allocated
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// DatabaseNetwork is who may reach a database: internal peers (security
// group peer syntax, fully qualified) and the public endpoint.
type DatabaseNetwork struct {
	// Access lists the internal peers allowed in. Absent (nil) means the
	// default: a project database's own environment; nothing for a
	// standalone one.
	Access NullList[string] `json:"access"`
	Public DatabasePublic   `json:"public"`
}

// DatabasePublic is the endpoint Traefik serves from outside the cluster.
type DatabasePublic struct {
	Enabled bool     `json:"enabled"`
	Allow   []string `json:"allow"` // client CIDRs
	// RequireTLS refuses plain connections on the dedicated ports.
	RequireTLS bool `json:"requireTls,omitempty"`
	// Port and ReadPort are the dedicated read-write and read-only ports,
	// assigned by the controller while the endpoint is on (Phase 18).
	Port     int `json:"port,omitempty"`
	ReadPort int `json:"readPort,omitempty"`
}

// ParseNetwork returns the database's network settings with defaults.
func (d Database) ParseNetwork() DatabaseNetwork {
	var n DatabaseNetwork
	_ = json.Unmarshal([]byte(d.Network), &n)
	if n.Access == nil {
		n.Access = []string{}
		if d.Project != "" {
			n.Access = []string{"environment:" + d.Project + "/" + d.Environment}
		}
	}
	if n.Public.Allow == nil {
		n.Public.Allow = []string{}
	}
	return n
}

// Standalone reports a database outside any project.
func (d Database) Standalone() bool { return d.EnvironmentID == "" }

// DatabaseMember is one container of a database: a data member or a sentinel.
type DatabaseMember struct {
	ID         string
	DatabaseID string
	Kind       string // data | sentinel
	Ordinal    int
	NodeID     string
	Desired    string // running | stopped
	State      string // pending | running | exited | failed | stopped | lost
	IP         string
	Error      string
	SpecHash   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DatabaseEvent is a scaling or failover event.
type DatabaseEvent struct {
	ID         int64     `json:"id"`
	DatabaseID string    `json:"-"`
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Reason     string    `json:"reason"`
	Actor      string    `json:"actor"`
}

const databaseCols = `SELECT d.id, coalesce(d.environment_id, ''), coalesce(p.name, ''), coalesce(e.name, ''), d.name, d.engine, d.version,
	d.spec, d.state, d.network, d.secrets_enc, d.status, d.deleting, coalesce(d.vip_rw, 0), coalesce(d.vip_ro, 0), d.created_at, d.updated_at
	FROM databases d LEFT JOIN environments e ON e.id = d.environment_id LEFT JOIN projects p ON p.id = e.project_id`

func scanDatabase(r scanner) (Database, error) {
	var d Database
	var created, updated int64
	err := r.Scan(&d.ID, &d.EnvironmentID, &d.Project, &d.Environment, &d.Name, &d.Engine, &d.Version, &d.Spec, &d.State, &d.Network, &d.Secrets,
		&d.Status, &d.Deleting, &d.VIPRW, &d.VIPRO, &created, &updated)
	d.CreatedAt, d.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return d, err
}

// CreateDatabase stores a new database. Its name must be free among all
// databases, and for a project database also among its environment's
// services (they share DNS names).
func (s *Store) CreateDatabase(ctx context.Context, d Database) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var env any
	if d.EnvironmentID != "" {
		env = d.EnvironmentID
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM services WHERE environment_id = ? AND name = ?`, d.EnvironmentID, d.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrNameTaken
		}
	}
	if d.Network == "" {
		d.Network = "{}"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO databases (id, environment_id, name, engine, version, spec, state, network, secrets_enc, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d.ID, env, d.Name, d.Engine, d.Version, d.Spec, d.State, d.Network, d.Secrets,
		d.CreatedAt.Unix(), d.CreatedAt.Unix()); isUnique(err) {
		return ErrNameTaken
	} else if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListDatabases(ctx context.Context) ([]Database, error) {
	rows, err := s.R.QueryContext(ctx, databaseCols+` ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		d, err := scanDatabase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DatabaseByID(ctx context.Context, id string) (Database, error) {
	d, err := scanDatabase(s.R.QueryRowContext(ctx, databaseCols+` WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

func (s *Store) DatabaseByName(ctx context.Context, name string) (Database, error) {
	d, err := scanDatabase(s.R.QueryRowContext(ctx, databaseCols+` WHERE d.name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DatabaseNameTaken reports whether a database of the environment uses the
// name (services check it).
func (s *Store) DatabaseNameTaken(ctx context.Context, environmentID, name string) (bool, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM databases WHERE environment_id = ? AND name = ?`, environmentID, name).Scan(&n)
	return n > 0, err
}

func (s *Store) SetDatabaseSpec(ctx context.Context, id, spec string, now time.Time) error {
	return s.execOne(ctx, `UPDATE databases SET spec = ?, updated_at = ? WHERE id = ?`, spec, now.Unix(), id)
}

func (s *Store) SetDatabaseNetwork(ctx context.Context, id, network string, now time.Time) error {
	return s.execOne(ctx, `UPDATE databases SET network = ?, updated_at = ? WHERE id = ?`, network, now.Unix(), id)
}

func (s *Store) SetDatabaseState(ctx context.Context, id, state string) error {
	return s.execOne(ctx, `UPDATE databases SET state = ? WHERE id = ?`, state, id)
}

func (s *Store) SetDatabaseStatus(ctx context.Context, id, status string) error {
	return s.execOne(ctx, `UPDATE databases SET status = ? WHERE id = ?`, status, id)
}

func (s *Store) MarkDatabaseDeleting(ctx context.Context, id string, now time.Time) error {
	return s.execOne(ctx, `UPDATE databases SET deleting = 1, updated_at = ? WHERE id = ?`, now.Unix(), id)
}

// DeleteDatabase removes a database and its members, releasing its VIPs
// into the cooldown.
func (s *Store) DeleteDatabase(ctx context.Context, id string, now time.Time) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rw, ro sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT vip_rw, vip_ro FROM databases WHERE id = ?`, id).Scan(&rw, &ro); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	for _, v := range []sql.NullInt64{rw, ro} {
		if v.Valid {
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO ipam_released (kind, idx, released_at) VALUES ('vip', ?, ?)`, v.Int64, now.Unix()); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM databases WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsureDatabaseVIPs allocates the read-write and read-only VIPs from the
// same pool as service VIPs.
func (s *Store) EnsureDatabaseVIPs(ctx context.Context, id string, pool IndexPool, cooldown time.Duration, now time.Time) (int, int, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var rw, ro sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT vip_rw, vip_ro FROM databases WHERE id = ?`, id).Scan(&rw, &ro); errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	} else if err != nil {
		return 0, 0, err
	}
	if rw.Valid && ro.Valid {
		return int(rw.Int64), int(ro.Int64), nil
	}
	taken, err := takenVIPs(ctx, tx, cooldown, now)
	if err != nil {
		return 0, 0, err
	}
	next := func() (int, error) {
		for i := pool.Min; i <= pool.Max; i++ {
			if !taken[i] && (pool.Skip == nil || !pool.Skip(i)) {
				taken[i] = true
				return i, nil
			}
		}
		return 0, ErrPoolExhausted
	}
	for _, v := range []*sql.NullInt64{&rw, &ro} {
		if !v.Valid {
			i, err := next()
			if err != nil {
				return 0, 0, err
			}
			*v = sql.NullInt64{Int64: int64(i), Valid: true}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE databases SET vip_rw = ?, vip_ro = ? WHERE id = ?`, rw.Int64, ro.Int64, id); err != nil {
		return 0, 0, err
	}
	return int(rw.Int64), int(ro.Int64), tx.Commit()
}

// takenVIPs is every VIP index in use by services or databases, or cooling down.
func takenVIPs(ctx context.Context, tx *sql.Tx, cooldown time.Duration, now time.Time) (map[int]bool, error) {
	taken := map[int]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT vip_index FROM services WHERE vip_index IS NOT NULL
		UNION SELECT vip_rw FROM databases WHERE vip_rw IS NOT NULL
		UNION SELECT vip_ro FROM databases WHERE vip_ro IS NOT NULL
		UNION SELECT idx FROM ipam_released WHERE kind = 'vip' AND released_at > ?`, now.Add(-cooldown).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		taken[i] = true
	}
	return taken, rows.Err()
}

// ── members ─────────────────────────────────────────────────────────────────

const memberCols = `SELECT id, database_id, kind, ordinal, node_id, desired, state, ip, error, spec_hash, created_at, updated_at FROM database_members`

func scanMember(r scanner) (DatabaseMember, error) {
	var m DatabaseMember
	var created, updated int64
	err := r.Scan(&m.ID, &m.DatabaseID, &m.Kind, &m.Ordinal, &m.NodeID, &m.Desired, &m.State, &m.IP, &m.Error, &m.SpecHash, &created, &updated)
	m.CreatedAt, m.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return m, err
}

func (s *Store) queryMembers(ctx context.Context, where string, args ...any) ([]DatabaseMember, error) {
	rows, err := s.R.QueryContext(ctx, memberCols+` WHERE `+where+` ORDER BY database_id, kind, ordinal`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseMember
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DatabaseMembers(ctx context.Context, databaseID string) ([]DatabaseMember, error) {
	return s.queryMembers(ctx, `database_id = ?`, databaseID)
}

// AllDatabaseMembers is every member of every database.
func (s *Store) AllDatabaseMembers(ctx context.Context) ([]DatabaseMember, error) {
	return s.queryMembers(ctx, `1 = 1`)
}

func (s *Store) NodeDatabaseMembers(ctx context.Context, nodeID string) ([]DatabaseMember, error) {
	return s.queryMembers(ctx, `node_id = ?`, nodeID)
}

func (s *Store) DatabaseMemberByID(ctx context.Context, id string) (DatabaseMember, error) {
	m, err := scanMember(s.R.QueryRowContext(ctx, memberCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

func (s *Store) CreateDatabaseMember(ctx context.Context, m DatabaseMember) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO database_members (id, database_id, kind, ordinal, node_id, desired, state, spec_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.DatabaseID, m.Kind, m.Ordinal, m.NodeID, m.Desired, m.State, m.SpecHash,
		m.CreatedAt.Unix(), m.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

// UpdateDatabaseMember saves a member's observed and desired state.
func (s *Store) UpdateDatabaseMember(ctx context.Context, m DatabaseMember, now time.Time) error {
	return s.execOne(ctx, `UPDATE database_members SET desired = ?, state = ?, ip = ?, error = ?, spec_hash = ?, updated_at = ? WHERE id = ?`,
		m.Desired, m.State, m.IP, m.Error, m.SpecHash, now.Unix(), m.ID)
}

func (s *Store) DeleteDatabaseMember(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM database_members WHERE id = ?`, id)
}

// ── events ──────────────────────────────────────────────────────────────────

func (s *Store) AddDatabaseEvent(ctx context.Context, e DatabaseEvent) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO database_events (database_id, at, kind, from_value, to_value, reason, actor) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.DatabaseID, e.At.Unix(), e.Kind, e.From, e.To, e.Reason, e.Actor)
	if err != nil {
		return err
	}
	// Keep the newest 500 per database.
	_, err = s.W.ExecContext(ctx, `DELETE FROM database_events WHERE database_id = ? AND id NOT IN
		(SELECT id FROM database_events WHERE database_id = ? ORDER BY id DESC LIMIT 500)`, e.DatabaseID, e.DatabaseID)
	return err
}

// DatabaseEvents returns the newest events, older than the event before
// names when set.
func (s *Store) DatabaseEvents(ctx context.Context, databaseID string, limit int, before string) ([]DatabaseEvent, error) {
	b := idCursor(before)
	rows, err := s.R.QueryContext(ctx, `SELECT id, at, kind, from_value, to_value, reason, actor FROM database_events
		WHERE database_id = ? AND (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`, databaseID, b, b, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseEvent
	for rows.Next() {
		var e DatabaseEvent
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Kind, &e.From, &e.To, &e.Reason, &e.Actor); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) execOne(ctx context.Context, q string, args ...any) error {
	res, err := s.W.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
