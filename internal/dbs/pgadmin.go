package dbs

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// PostgreSQL explorer and administration (§13 "Explorer and
// administration"). The controller works on the leader as the platform
// superuser for catalog reads and DDL it builds itself (identifiers quoted,
// values bound); SQL typed by people runs as the app user (pgquery.go).

const explorerApp = "syncloud-explorer"

// explorerConns caps concurrent explorer connections per cluster.
const explorerConns = 4

var pgIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.$-]{0,62}$`)

// protectedRoles are the platform's roles: visible, never changed.
var protectedRoles = []string{pgSuperuser, pgReplication, "postgres"}

// forbiddenMemberships would hand out superuser-like or file access.
var forbiddenMemberships = []string{"pg_execute_server_program", "pg_read_server_files", "pg_write_server_files"}

func invalidf(format string, a ...any) error { return ErrInvalid{fmt.Errorf(format, a...)} }

func isProtectedRole(name string) bool {
	return slices.Contains(protectedRoles, name) || strings.HasPrefix(name, "pg_")
}

func isProtectedDatabase(name string) bool {
	return name == "postgres" || strings.HasPrefix(name, "template")
}

func checkIdent(what, name string) error {
	if !pgIdent.MatchString(name) {
		return invalidf("%s %q: use 1-63 letters, digits, _ . $ or -, starting with a letter or _", what, name)
	}
	return nil
}

func qi(name string) string { return pgx.Identifier{name}.Sanitize() }

// pgLeader returns the leader's address.
func (m *Manager) pgLeader(ctx context.Context, d store.Database) (string, error) {
	if d.Engine != EnginePostgres {
		return "", invalidf("%s is not a PostgreSQL database", d.Name)
	}
	st := parseState(d.State)
	members, err := m.st.DatabaseMembers(ctx, d.ID)
	if err != nil {
		return "", err
	}
	for _, mb := range members {
		if mb.Kind == KindData && mb.Ordinal == st.Primary && mb.IP != "" && mb.State == store.TaskRunning {
			return mb.IP, nil
		}
	}
	return "", ErrUnavailable
}

// explorerSlot limits concurrent explorer work on one cluster.
func (m *Manager) explorerSlot(ctx context.Context, id string) (func(), error) {
	m.mu.Lock()
	if m.pgSlots == nil {
		m.pgSlots = map[string]chan struct{}{}
	}
	ch, ok := m.pgSlots[id]
	if !ok {
		ch = make(chan struct{}, explorerConns)
		m.pgSlots[id] = ch
	}
	m.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pgAdmin runs fn on a superuser connection to database db on the leader
// ("" is the cluster's own database).
func (m *Manager) pgAdmin(ctx context.Context, d store.Database, db string, fn func(*pgx.Conn) error) error {
	return m.pgWith(ctx, d, db, pgSuperuser, fn)
}

func (m *Manager) pgWith(ctx context.Context, d store.Database, db, user string, fn func(*pgx.Conn) error) error {
	ip, err := m.pgLeader(ctx, d)
	if err != nil {
		return err
	}
	sec, err := m.secrets(d)
	if err != nil {
		return err
	}
	if db == "" {
		db = PgDatabase(d)
	}
	release, err := m.explorerSlot(ctx, d.ID)
	if err != nil {
		return err
	}
	defer release()
	cfg, err := pgx.ParseConfig("")
	if err != nil {
		return err
	}
	cfg.Host, cfg.Port, cfg.Database, cfg.User = ip, PostgresPort, db, user
	cfg.Password = sec.AdminPassword
	if user == pgAppUser {
		cfg.Password = sec.Password
	}
	cfg.ConnectTimeout = 5 * time.Second
	cfg.RuntimeParams = map[string]string{"application_name": explorerApp, "statement_timeout": "30s", "lock_timeout": "10s"}
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		var pe interface{ SQLState() string }
		if errors.As(err, &pe) && pe.SQLState() == "3D000" {
			return invalidf("no database %q", db)
		}
		return fmt.Errorf("connect to %s: %w", db, err)
	}
	defer c.Close(context.WithoutCancel(ctx))
	return fn(c)
}

// ── databases ───────────────────────────────────────────────────────────────

// PgDatabaseInfo is one database in a cluster.
type PgDatabaseInfo struct {
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	Encoding    string `json:"encoding"`
	Collation   string `json:"collation"`
	SizeBytes   int64  `json:"sizeBytes"`
	Connections int    `json:"connections"`
	ConnLimit   int    `json:"connLimit"`
	AllowConn   bool   `json:"allowConnections"`
	Comment     string `json:"comment"`
	Primary     bool   `json:"primary"`   // the cluster's own database (credentials point here)
	Protected   bool   `json:"protected"` // a platform database
}

// PgDatabases lists the cluster's databases.
func (m *Manager) PgDatabases(ctx context.Context, d store.Database) ([]PgDatabaseInfo, error) {
	var out []PgDatabaseInfo
	err := m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
SELECT d.datname, pg_get_userbyid(d.datdba), pg_encoding_to_char(d.encoding), d.datcollate,
       pg_database_size(d.oid), (SELECT count(*) FROM pg_stat_activity a WHERE a.datid = d.oid AND a.application_name <> $1)::int,
       d.datconnlimit, d.datallowconn, coalesce(shobj_description(d.oid, 'pg_database'), '')
FROM pg_database d WHERE NOT d.datistemplate ORDER BY d.datname`, explorerApp)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x PgDatabaseInfo
			if err := rows.Scan(&x.Name, &x.Owner, &x.Encoding, &x.Collation, &x.SizeBytes, &x.Connections, &x.ConnLimit, &x.AllowConn, &x.Comment); err != nil {
				return err
			}
			x.Primary, x.Protected = x.Name == PgDatabase(d), isProtectedDatabase(x.Name)
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// PgDatabaseInput creates or changes a database.
type PgDatabaseInput struct {
	Name      string  `json:"name,omitempty"` // create; or the new name when renaming
	Owner     string  `json:"owner,omitempty"`
	ConnLimit *int    `json:"connLimit,omitempty"`
	Comment   *string `json:"comment,omitempty"`
}

// CreatePgDatabase creates a database (owner defaults to app).
func (m *Manager) CreatePgDatabase(ctx context.Context, d store.Database, in PgDatabaseInput) error {
	if err := checkIdent("database", in.Name); err != nil {
		return err
	}
	if isProtectedDatabase(in.Name) {
		return invalidf("%q is reserved", in.Name)
	}
	if in.Owner == "" {
		in.Owner = pgAppUser
	}
	if isProtectedRole(in.Owner) {
		return invalidf("a platform role cannot own databases")
	}
	return m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `CREATE DATABASE `+qi(in.Name)+` OWNER `+qi(in.Owner)); err != nil {
			return err
		}
		return alterDatabase(ctx, c, in.Name, PgDatabaseInput{ConnLimit: in.ConnLimit, Comment: in.Comment})
	})
}

// AlterPgDatabase renames a database, or changes its owner, limit or comment.
func (m *Manager) AlterPgDatabase(ctx context.Context, d store.Database, name string, in PgDatabaseInput) error {
	if isProtectedDatabase(name) {
		return invalidf("%q is a platform database", name)
	}
	if in.Name != "" && in.Name != name {
		if name == PgDatabase(d) {
			return invalidf("%q is the cluster's own database; its name is part of the credentials", name)
		}
		if err := checkIdent("database", in.Name); err != nil {
			return err
		}
		if isProtectedDatabase(in.Name) {
			return invalidf("%q is reserved", in.Name)
		}
	}
	if in.Owner != "" && isProtectedRole(in.Owner) {
		return invalidf("a platform role cannot own databases")
	}
	return m.pgAdmin(ctx, d, "postgres", func(c *pgx.Conn) error {
		if in.Owner != "" {
			if _, err := c.Exec(ctx, `ALTER DATABASE `+qi(name)+` OWNER TO `+qi(in.Owner)); err != nil {
				return err
			}
		}
		if err := alterDatabase(ctx, c, name, in); err != nil {
			return err
		}
		if in.Name != "" && in.Name != name {
			// Renaming needs the database idle.
			if _, err := c.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); err != nil {
				return err
			}
			if _, err := c.Exec(ctx, `ALTER DATABASE `+qi(name)+` RENAME TO `+qi(in.Name)); err != nil {
				return err
			}
		}
		return nil
	})
}

func alterDatabase(ctx context.Context, c *pgx.Conn, name string, in PgDatabaseInput) error {
	if in.ConnLimit != nil {
		if _, err := c.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %s CONNECTION LIMIT %d`, qi(name), max(-1, *in.ConnLimit))); err != nil {
			return err
		}
	}
	if in.Comment != nil {
		if _, err := c.Exec(ctx, `COMMENT ON DATABASE `+qi(name)+` IS `+quoteLiteral(*in.Comment)); err != nil {
			return err
		}
	}
	return nil
}

// DropPgDatabase drops a database, disconnecting its sessions.
func (m *Manager) DropPgDatabase(ctx context.Context, d store.Database, name string) error {
	if isProtectedDatabase(name) {
		return invalidf("%q is a platform database", name)
	}
	if name == PgDatabase(d) {
		return invalidf("%q is the cluster's own database; delete the cluster instead", name)
	}
	return m.pgAdmin(ctx, d, "postgres", func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `DROP DATABASE `+qi(name)+` WITH (FORCE)`)
		return err
	})
}

// ── roles ───────────────────────────────────────────────────────────────────

// PgMembership is membership in another role.
type PgMembership struct {
	Role    string `json:"role"`
	Admin   bool   `json:"admin"`
	Inherit bool   `json:"inherit"`
}

// PgRole is a role with its attributes.
type PgRole struct {
	Name        string         `json:"name"`
	Login       bool           `json:"login"`
	CreateDB    bool           `json:"createDb"`
	CreateRole  bool           `json:"createRole"`
	Inherit     bool           `json:"inherit"`
	Superuser   bool           `json:"superuser"`
	Replication bool           `json:"replication"`
	BypassRLS   bool           `json:"bypassRls"`
	ConnLimit   int            `json:"connLimit"`
	ValidUntil  *time.Time     `json:"validUntil"`
	HasPassword bool           `json:"hasPassword"`
	MemberOf    []PgMembership `json:"memberOf"`
	Members     []string       `json:"members"`
	Owns        []string       `json:"owns"` // databases
	Connections int            `json:"connections"`
	Comment     string         `json:"comment"`
	Protected   bool           `json:"protected"`  // a platform role
	Predefined  bool           `json:"predefined"` // pg_* built-in roles
	App         bool           `json:"app"`        // the credentials' role
}

const roleQuery = `
SELECT r.rolname, r.rolcanlogin, r.rolcreatedb, r.rolcreaterole, r.rolinherit, r.rolsuper, r.rolreplication, r.rolbypassrls,
       r.rolconnlimit, nullif(r.rolvaliduntil, 'infinity'), a.rolpassword IS NOT NULL,
       coalesce((SELECT json_agg(json_build_object('role', g.rolname, 'admin', m.admin_option, 'inherit', m.inherit_option) ORDER BY g.rolname)
                 FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid WHERE m.member = r.oid), '[]'),
       coalesce((SELECT array_agg(u.rolname ORDER BY u.rolname) FROM pg_auth_members m JOIN pg_roles u ON u.oid = m.member WHERE m.roleid = r.oid), '{}'),
       coalesce((SELECT array_agg(datname ORDER BY datname) FROM pg_database WHERE datdba = r.oid AND NOT datistemplate), '{}'),
       (SELECT count(*) FROM pg_stat_activity s WHERE s.usesysid = r.oid AND s.application_name <> $1)::int,
       coalesce(shobj_description(r.oid, 'pg_authid'), '')
FROM pg_roles r JOIN pg_authid a ON a.oid = r.oid`

func scanRoles(rows pgx.Rows) ([]PgRole, error) {
	var out []PgRole
	for rows.Next() {
		var x PgRole
		var member []byte
		if err := rows.Scan(&x.Name, &x.Login, &x.CreateDB, &x.CreateRole, &x.Inherit, &x.Superuser, &x.Replication, &x.BypassRLS,
			&x.ConnLimit, &x.ValidUntil, &x.HasPassword, &member, &x.Members, &x.Owns, &x.Connections, &x.Comment); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(member, &x.MemberOf)
		x.Predefined = strings.HasPrefix(x.Name, "pg_")
		x.Protected = isProtectedRole(x.Name)
		x.App = x.Name == pgAppUser
		out = append(out, x)
	}
	return out, rows.Err()
}

// PgRoles lists every role, the predefined pg_* ones included.
func (m *Manager) PgRoles(ctx context.Context, d store.Database) ([]PgRole, error) {
	var out []PgRole
	err := m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, roleQuery+` ORDER BY r.rolname LIKE 'pg\_%', r.rolname`, explorerApp)
		if err != nil {
			return err
		}
		out, err = scanRoles(rows)
		return err
	})
	return out, err
}

// PgRoleByName returns one role.
func (m *Manager) PgRoleByName(ctx context.Context, d store.Database, name string) (PgRole, error) {
	var out []PgRole
	err := m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, roleQuery+` WHERE r.rolname = $2`, explorerApp, name)
		if err != nil {
			return err
		}
		out, err = scanRoles(rows)
		return err
	})
	if err != nil {
		return PgRole{}, err
	}
	if len(out) == 0 {
		return PgRole{}, store.ErrNotFound
	}
	return out[0], nil
}

// PgRoleInput creates or changes a role. Nil fields stay as they are.
type PgRoleInput struct {
	Name             string          `json:"name,omitempty"` // create; or the new name when renaming
	Password         *string         `json:"password,omitempty"`
	GeneratePassword bool            `json:"generatePassword,omitempty"`
	Login            *bool           `json:"login,omitempty"`
	CreateDB         *bool           `json:"createDb,omitempty"`
	CreateRole       *bool           `json:"createRole,omitempty"`
	Inherit          *bool           `json:"inherit,omitempty"`
	ConnLimit        *int            `json:"connLimit,omitempty"`
	ValidUntil       *string         `json:"validUntil,omitempty"` // RFC 3339; "" = never expires
	MemberOf         *[]PgMembership `json:"memberOf,omitempty"`   // the full set wanted
	Comment          *string         `json:"comment,omitempty"`
}

// roleOptions renders the attribute clauses of CREATE/ALTER ROLE.
func roleOptions(in PgRoleInput, password string) (string, error) {
	var o []string
	flag := func(b *bool, on, off string) {
		if b != nil {
			o = append(o, map[bool]string{true: on, false: off}[*b])
		}
	}
	flag(in.Login, "LOGIN", "NOLOGIN")
	flag(in.CreateDB, "CREATEDB", "NOCREATEDB")
	flag(in.CreateRole, "CREATEROLE", "NOCREATEROLE")
	flag(in.Inherit, "INHERIT", "NOINHERIT")
	if in.ConnLimit != nil {
		o = append(o, fmt.Sprintf("CONNECTION LIMIT %d", max(-1, *in.ConnLimit)))
	}
	if in.ValidUntil != nil {
		if *in.ValidUntil == "" {
			o = append(o, "VALID UNTIL 'infinity'")
		} else {
			t, err := time.Parse(time.RFC3339, *in.ValidUntil)
			if err != nil {
				return "", invalidf("validUntil: use RFC 3339, e.g. 2027-01-31T00:00:00Z")
			}
			o = append(o, "VALID UNTIL "+quoteLiteral(t.UTC().Format(time.RFC3339)))
		}
	}
	if password != "" {
		v, err := scramVerifier(password)
		if err != nil {
			return "", err
		}
		o = append(o, "PASSWORD "+quoteLiteral(v))
	} else if in.Password != nil && *in.Password == "" {
		o = append(o, "PASSWORD NULL")
	}
	if len(o) == 0 {
		return "", nil
	}
	return " WITH " + strings.Join(o, " "), nil
}

func checkMemberships(ms []PgMembership) error {
	for _, g := range ms {
		if slices.Contains(protectedRoles, g.Role) || slices.Contains(forbiddenMemberships, g.Role) {
			return invalidf("membership in %q is not allowed", g.Role)
		}
	}
	return nil
}

// CreatePgRole creates a role and returns the password it set ("" when none).
func (m *Manager) CreatePgRole(ctx context.Context, d store.Database, in PgRoleInput) (string, error) {
	if err := checkIdent("role", in.Name); err != nil {
		return "", err
	}
	if isProtectedRole(in.Name) || in.Name == "public" {
		return "", invalidf("%q is reserved", in.Name)
	}
	t := true
	if in.Login == nil {
		in.Login = &t
	}
	if in.ValidUntil != nil && *in.ValidUntil == "" {
		in.ValidUntil = nil // a new role never expires unless told
	}
	password := ""
	if in.Password != nil {
		password = *in.Password
	}
	if password == "" && (in.GeneratePassword || (*in.Login && in.Password == nil)) {
		password = randomPassword()
	}
	if in.MemberOf != nil {
		if err := checkMemberships(*in.MemberOf); err != nil {
			return "", err
		}
	}
	opts, err := roleOptions(in, password)
	if err != nil {
		return "", err
	}
	err = m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx, `CREATE ROLE `+qi(in.Name)+opts); err != nil {
			return err
		}
		// The app user administers the roles made here, and may SET ROLE to
		// them (the console's "run as"), without inheriting their rights.
		if _, err := tx.Exec(ctx, `GRANT `+qi(in.Name)+` TO `+qi(pgAppUser)+` WITH ADMIN TRUE, INHERIT FALSE, SET TRUE`); err != nil {
			return err
		}
		if in.MemberOf != nil {
			if err := setMemberships(ctx, tx, in.Name, nil, *in.MemberOf); err != nil {
				return err
			}
		}
		if in.Comment != nil {
			if _, err := tx.Exec(ctx, `COMMENT ON ROLE `+qi(in.Name)+` IS `+quoteLiteral(*in.Comment)); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	return password, err
}

func setMemberships(ctx context.Context, tx pgx.Tx, role string, have, want []PgMembership) error {
	for _, w := range want {
		i := slices.IndexFunc(have, func(h PgMembership) bool { return h.Role == w.Role })
		if i >= 0 && have[i] == w {
			continue
		}
		q := fmt.Sprintf(`GRANT %s TO %s WITH ADMIN %t, INHERIT %t`, qi(w.Role), qi(role), w.Admin, w.Inherit)
		if _, err := tx.Exec(ctx, q); err != nil {
			return err
		}
	}
	for _, h := range have {
		if !slices.ContainsFunc(want, func(w PgMembership) bool { return w.Role == h.Role }) {
			if _, err := tx.Exec(ctx, `REVOKE `+qi(h.Role)+` FROM `+qi(role)+` CASCADE`); err != nil {
				return err
			}
		}
	}
	return nil
}

// AlterPgRole changes a role and returns the password it set ("" when unchanged).
func (m *Manager) AlterPgRole(ctx context.Context, d store.Database, name string, in PgRoleInput) (string, error) {
	if isProtectedRole(name) {
		return "", invalidf("%q is a platform role", name)
	}
	rename := in.Name != "" && in.Name != name
	if name == pgAppUser {
		// The platform holds app's password: it is in the credentials and
		// the console signs in with it.
		if rename || in.Password != nil || in.GeneratePassword || (in.Login != nil && !*in.Login) || in.ValidUntil != nil {
			return "", invalidf("app is the cluster's credentials role: its name, password, login and expiry are managed by the platform")
		}
	}
	if rename {
		if err := checkIdent("role", in.Name); err != nil {
			return "", err
		}
		if isProtectedRole(in.Name) || in.Name == "public" {
			return "", invalidf("%q is reserved", in.Name)
		}
	}
	password := ""
	if in.Password != nil {
		password = *in.Password
	}
	if in.GeneratePassword {
		password = randomPassword()
	}
	if in.MemberOf != nil {
		if err := checkMemberships(*in.MemberOf); err != nil {
			return "", err
		}
	}
	opts, err := roleOptions(in, password)
	if err != nil {
		return "", err
	}
	cur, err := m.PgRoleByName(ctx, d, name)
	if err != nil {
		return "", err
	}
	err = m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if opts != "" {
			if _, err := tx.Exec(ctx, `ALTER ROLE `+qi(name)+opts); err != nil {
				return err
			}
		}
		if in.MemberOf != nil {
			if err := setMemberships(ctx, tx, name, cur.MemberOf, *in.MemberOf); err != nil {
				return err
			}
		}
		if in.Comment != nil {
			if _, err := tx.Exec(ctx, `COMMENT ON ROLE `+qi(name)+` IS `+quoteLiteral(*in.Comment)); err != nil {
				return err
			}
		}
		if rename {
			// Renaming clears an MD5 password; SCRAM ones survive.
			if _, err := tx.Exec(ctx, `ALTER ROLE `+qi(name)+` RENAME TO `+qi(in.Name)); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	return password, err
}

// DropPgRole hands the role's objects in every database to reassignTo (app
// by default), drops its privileges, then drops it.
func (m *Manager) DropPgRole(ctx context.Context, d store.Database, name, reassignTo string) error {
	if isProtectedRole(name) || name == pgAppUser {
		return invalidf("%q is managed by the platform", name)
	}
	if reassignTo == "" {
		reassignTo = pgAppUser
	}
	if reassignTo == name || slices.Contains(protectedRoles, reassignTo) {
		return invalidf("choose another role to take over %q's objects", name)
	}
	if _, err := m.PgRoleByName(ctx, d, name); err != nil {
		return err
	}
	dbsList, err := m.PgDatabases(ctx, d)
	if err != nil {
		return err
	}
	for _, x := range dbsList {
		if !x.AllowConn {
			continue
		}
		err := m.pgAdmin(ctx, d, x.Name, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, `REASSIGN OWNED BY `+qi(name)+` TO `+qi(reassignTo)+`; DROP OWNED BY `+qi(name))
			return err
		})
		if err != nil {
			return fmt.Errorf("database %s: %w", x.Name, err)
		}
	}
	return m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `DROP ROLE `+qi(name))
		return err
	})
}

// scramVerifier hashes a password the way PostgreSQL stores it, so the
// plain text never reaches the server (or its statement log).
func scramVerifier(password string) (string, error) {
	const iterations = 4096
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	server := mac(salted, "Server Key")
	b := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b(salt), b(stored[:]), b(server)), nil
}
