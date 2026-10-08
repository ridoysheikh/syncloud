package dbs

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// Privilege kinds and what each grants (GRANT's object types).
var privsByKind = map[string][]string{
	"database": {"CONNECT", "CREATE", "TEMPORARY"},
	"schema":   {"USAGE", "CREATE"},
	"table":    {"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"},
	"sequence": {"USAGE", "SELECT", "UPDATE"},
	"function": {"EXECUTE"},
}

// PgObjectPrivileges lists the privilege kinds an object type supports.
func PgObjectPrivileges() map[string][]string { return privsByKind }

// PgGrant is one grantee's privileges on an object.
type PgGrant struct {
	Grantee    string            `json:"grantee"` // a role, or PUBLIC
	Privileges map[string]bool   `json:"privileges"`
	Grantors   map[string]string `json:"grantors,omitempty"` // privilege -> grantor
}

// PgDefaultGrant is a default privilege: what objects a role creates in a
// schema will grant.
type PgDefaultGrant struct {
	ForRole    string          `json:"forRole"`
	ObjectType string          `json:"objectType"` // table, sequence, function, type, schema
	Grantee    string          `json:"grantee"`
	Privileges map[string]bool `json:"privileges"`
}

// PgPrivileges are the grants on one object.
type PgPrivileges struct {
	Kind      string           `json:"kind"`
	Object    string           `json:"object"`
	Owner     string           `json:"owner"`
	Available []string         `json:"available"`
	Grants    []PgGrant        `json:"grants"`
	Defaults  []PgDefaultGrant `json:"defaults,omitempty"` // schemas only
	// Effective is what Role can do through every membership, when asked.
	Role      string          `json:"role,omitempty"`
	Effective map[string]bool `json:"effective,omitempty"`
}

// PgObjectRef names a privilege target.
type PgObjectRef struct {
	Kind   string `json:"kind"` // database, schema, table (also views), sequence, function
	Schema string `json:"schema,omitempty"`
	Name   string `json:"name,omitempty"` // database or schema name, or the relation
	OID    uint32 `json:"oid,omitempty"`  // functions
}

// aclSource returns the ACL expression, owner and display name of an object.
func aclSource(ctx context.Context, c *pgx.Conn, ref PgObjectRef) (acl, owner, name, oid string, err error) {
	var q string
	var args []any
	switch ref.Kind {
	case "database":
		q, args = `SELECT coalesce(datacl, acldefault('d', datdba))::text, pg_get_userbyid(datdba), quote_ident(datname), oid::text FROM pg_database WHERE datname = $1`, []any{ref.Name}
	case "schema":
		q, args = `SELECT coalesce(nspacl, acldefault('n', nspowner))::text, pg_get_userbyid(nspowner), quote_ident(nspname), oid::text FROM pg_namespace WHERE nspname = $1`, []any{ref.Name}
	case "table", "sequence":
		q = `SELECT coalesce(c.relacl, acldefault((CASE WHEN c.relkind = 'S' THEN 's' ELSE 'r' END)::"char", c.relowner))::text, pg_get_userbyid(c.relowner),
		            quote_ident(n.nspname) || '.' || quote_ident(c.relname), c.oid::text
		     FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		     WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ` + map[bool]string{true: "('S')", false: "('r','p','v','m','f')"}[ref.Kind == "sequence"]
		args = []any{ref.Schema, ref.Name}
	case "function":
		q, args = `SELECT coalesce(proacl, acldefault('f', proowner))::text, pg_get_userbyid(proowner), oid::regprocedure::text, oid::text FROM pg_proc WHERE oid = $1`, []any{ref.OID}
	default:
		return "", "", "", "", invalidf("kind %q: use database, schema, table, sequence or function", ref.Kind)
	}
	err = c.QueryRow(ctx, q, args...).Scan(&acl, &owner, &name, &oid)
	if err == pgx.ErrNoRows {
		err = store.ErrNotFound
	}
	return
}

// PgObjectGrants reads an object's grants (and the effective rights of role).
func (m *Manager) PgObjectGrants(ctx context.Context, d store.Database, db string, ref PgObjectRef, role string) (PgPrivileges, error) {
	out := PgPrivileges{Kind: ref.Kind, Available: privsByKind[ref.Kind], Grants: []PgGrant{}}
	err := m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
		acl, owner, name, oid, err := aclSource(ctx, c, ref)
		if err != nil {
			return err
		}
		out.Object, out.Owner = name, owner
		rows, err := c.Query(ctx, `
SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END, pg_get_userbyid(a.grantor), a.privilege_type, a.is_grantable
FROM aclexplode($1::aclitem[]) a ORDER BY 1, 3`, acl)
		if err != nil {
			return err
		}
		idx := map[string]int{}
		for rows.Next() {
			var grantee, grantor, priv string
			var grantable bool
			if err := rows.Scan(&grantee, &grantor, &priv, &grantable); err != nil {
				return err
			}
			i, ok := idx[grantee]
			if !ok {
				i = len(out.Grants)
				idx[grantee] = i
				out.Grants = append(out.Grants, PgGrant{Grantee: grantee, Privileges: map[string]bool{}, Grantors: map[string]string{}})
			}
			out.Grants[i].Privileges[priv] = grantable
			out.Grants[i].Grantors[priv] = grantor
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if ref.Kind == "schema" {
			rows, err := c.Query(ctx, `
SELECT pg_get_userbyid(da.defaclrole), da.defaclobjtype::text,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END, a.privilege_type, a.is_grantable
FROM pg_default_acl da JOIN pg_namespace n ON n.oid = da.defaclnamespace, aclexplode(da.defaclacl) a
WHERE n.nspname = $1 ORDER BY 1, 2, 3`, ref.Name)
			if err != nil {
				return err
			}
			objTypes := map[string]string{"r": "table", "S": "sequence", "f": "function", "T": "type", "n": "schema"}
			for rows.Next() {
				var g PgDefaultGrant
				var typ, priv string
				var grantable bool
				if err := rows.Scan(&g.ForRole, &typ, &g.Grantee, &priv, &grantable); err != nil {
					return err
				}
				g.ObjectType = objTypes[typ]
				n := len(out.Defaults)
				if n > 0 && out.Defaults[n-1].ForRole == g.ForRole && out.Defaults[n-1].ObjectType == g.ObjectType && out.Defaults[n-1].Grantee == g.Grantee {
					out.Defaults[n-1].Privileges[priv] = grantable
					continue
				}
				g.Privileges = map[string]bool{priv: grantable}
				out.Defaults = append(out.Defaults, g)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		if role == "" {
			return nil
		}
		fn := map[string]string{"database": "has_database_privilege", "schema": "has_schema_privilege", "table": "has_table_privilege",
			"sequence": "has_sequence_privilege", "function": "has_function_privilege"}[ref.Kind]
		out.Role, out.Effective = role, map[string]bool{}
		for _, p := range privsByKind[ref.Kind] {
			var ok bool
			if err := c.QueryRow(ctx, `SELECT `+fn+`($1, $2::oid, $3)`, role, oid, p).Scan(&ok); err != nil {
				return err
			}
			out.Effective[p] = ok
		}
		return nil
	})
	return out, err
}

// PgPrivilegeChange is one GRANT or REVOKE.
type PgPrivilegeChange struct {
	Revoke      bool     `json:"revoke,omitempty"`
	Privileges  []string `json:"privileges"` // or ["ALL"]
	Grantee     string   `json:"grantee"`    // a role, or PUBLIC
	GrantOption bool     `json:"grantOption,omitempty"`
	// Target: one object; "all-tables", "all-sequences" or "all-functions"
	// in Schema; or "default-tables", "default-sequences", "default-functions"
	// (ALTER DEFAULT PRIVILEGES FOR ROLE ForRole IN SCHEMA Schema).
	Target  string      `json:"target"` // "object" (default) or the above
	Object  PgObjectRef `json:"object"`
	ForRole string      `json:"forRole,omitempty"`
}

var bulkKinds = map[string]string{"tables": "table", "sequences": "sequence", "functions": "function"}

// privilegeSQL renders a change as one statement.
func privilegeSQL(ctx context.Context, c *pgx.Conn, ch PgPrivilegeChange) (string, error) {
	if ch.Grantee == "" {
		return "", invalidf("grantee is required")
	}
	grantee := "PUBLIC"
	if !strings.EqualFold(ch.Grantee, "public") {
		if slices.Contains(protectedRoles, ch.Grantee) {
			return "", invalidf("%q is a platform role", ch.Grantee)
		}
		grantee = qi(ch.Grantee)
	}
	kind, on := "", ""
	target := ch.Target
	if target == "" {
		target = "object"
	}
	switch {
	case target == "object":
		kind = ch.Object.Kind
		if kind == "function" {
			_, _, name, _, err := aclSource(ctx, c, ch.Object)
			if err != nil {
				return "", err
			}
			on = "FUNCTION " + name
		} else {
			_, _, name, _, err := aclSource(ctx, c, ch.Object)
			if err != nil {
				return "", err
			}
			kw := map[string]string{"database": "DATABASE", "schema": "SCHEMA", "table": "TABLE", "sequence": "SEQUENCE"}[kind]
			on = kw + " " + name
		}
	case strings.HasPrefix(target, "all-"):
		k, ok := bulkKinds[strings.TrimPrefix(target, "all-")]
		if !ok || ch.Object.Schema == "" {
			return "", invalidf("target %q needs a schema", target)
		}
		kind = k
		on = "ALL " + strings.ToUpper(strings.TrimPrefix(target, "all-")) + " IN SCHEMA " + qi(ch.Object.Schema)
	case strings.HasPrefix(target, "default-"):
		k, ok := bulkKinds[strings.TrimPrefix(target, "default-")]
		if !ok || ch.Object.Schema == "" || ch.ForRole == "" {
			return "", invalidf("target %q needs a schema and forRole", target)
		}
		kind = k
		on = strings.ToUpper(strings.TrimPrefix(target, "default-"))
	default:
		return "", invalidf("target %q: use object, all-tables, all-sequences, all-functions, default-tables, default-sequences or default-functions", target)
	}
	allowed, ok := privsByKind[kind]
	if !ok {
		return "", invalidf("kind %q: use database, schema, table, sequence or function", kind)
	}
	var privs []string
	for _, p := range ch.Privileges {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "ALL" || p == "ALL PRIVILEGES" {
			privs = []string{"ALL PRIVILEGES"}
			break
		}
		if !slices.Contains(allowed, p) {
			return "", invalidf("privilege %s does not apply to a %s (use %s)", p, kind, strings.Join(allowed, ", "))
		}
		privs = append(privs, p)
	}
	if len(privs) == 0 {
		return "", invalidf("no privileges given")
	}
	var stmt string
	if ch.Revoke {
		opt := ""
		if ch.GrantOption {
			opt = "GRANT OPTION FOR "
		}
		stmt = fmt.Sprintf("REVOKE %s%s ON %s FROM %s", opt, strings.Join(privs, ", "), on, grantee)
	} else {
		stmt = fmt.Sprintf("GRANT %s ON %s TO %s", strings.Join(privs, ", "), on, grantee)
		if ch.GrantOption {
			stmt += " WITH GRANT OPTION"
		}
	}
	if strings.HasPrefix(target, "default-") {
		stmt = "ALTER DEFAULT PRIVILEGES FOR ROLE " + qi(ch.ForRole) + " IN SCHEMA " + qi(ch.Object.Schema) + " " + stmt
	}
	return stmt, nil
}

// ApplyPgPrivileges runs the changes in one transaction on db and returns
// the statements it ran.
func (m *Manager) ApplyPgPrivileges(ctx context.Context, d store.Database, db string, changes []PgPrivilegeChange) ([]string, error) {
	if len(changes) == 0 || len(changes) > 200 {
		return nil, invalidf("give 1 to 200 changes")
	}
	var ran []string
	err := m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		for _, ch := range changes {
			stmt, err := privilegeSQL(ctx, c, ch)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
			ran = append(ran, stmt)
		}
		return tx.Commit(ctx)
	})
	return ran, err
}

// PgAccessPreset gives a role a common level of access to a database.
type PgAccessPreset struct {
	Role    string   `json:"role"`
	Access  string   `json:"access"`            // read, write or none
	Schemas []string `json:"schemas,omitempty"` // default: every non-system schema
}

// ApplyPgPreset grants (or revokes) read-only or read-write access to a
// whole database: CONNECT, USAGE on its schemas, rights on every existing
// table and sequence, and default privileges for objects the schema
// owners and the database owner create later.
func (m *Manager) ApplyPgPreset(ctx context.Context, d store.Database, db string, p PgAccessPreset) ([]string, error) {
	if p.Access != "read" && p.Access != "write" && p.Access != "none" {
		return nil, invalidf("access %q: use read, write or none", p.Access)
	}
	if err := checkIdent("role", p.Role); err != nil {
		return nil, err
	}
	var schemas []PgSchema
	var dbOwner string
	err := m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT n.nspname, pg_get_userbyid(n.nspowner) FROM pg_namespace n WHERE NOT `+systemSchemaSQL+` AND n.nspname NOT LIKE 'pg\_%' ORDER BY 1`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s PgSchema
			if err := rows.Scan(&s.Name, &s.Owner); err != nil {
				return err
			}
			if len(p.Schemas) == 0 || slices.Contains(p.Schemas, s.Name) {
				schemas = append(schemas, s)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return c.QueryRow(ctx, `SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = current_database()`).Scan(&dbOwner)
	})
	if err != nil {
		return nil, err
	}
	tablePrivs := []string{"SELECT"}
	seqPrivs := []string{"SELECT"}
	if p.Access == "write" {
		tablePrivs = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}
		seqPrivs = []string{"USAGE", "SELECT", "UPDATE"}
	}
	revoke := p.Access == "none"
	if revoke || p.Access == "read" {
		// Going down to read (or none) first takes away everything.
		tablePrivs, seqPrivs = []string{"ALL"}, []string{"ALL"}
	}
	var changes []PgPrivilegeChange
	add := func(revoke bool, privs []string, target string, obj PgObjectRef, forRole string) {
		changes = append(changes, PgPrivilegeChange{Revoke: revoke, Privileges: privs, Grantee: p.Role, Target: target, Object: obj, ForRole: forRole})
	}
	if revoke {
		add(true, []string{"CONNECT", "TEMPORARY", "CREATE"}, "object", PgObjectRef{Kind: "database", Name: db}, "")
	} else {
		add(false, []string{"CONNECT"}, "object", PgObjectRef{Kind: "database", Name: db}, "")
	}
	for _, s := range schemas {
		obj := PgObjectRef{Kind: "schema", Name: s.Name, Schema: s.Name}
		owners := []string{s.Owner}
		if dbOwner != s.Owner && !isProtectedRole(dbOwner) {
			owners = append(owners, dbOwner)
		}
		owners = slices.DeleteFunc(owners, func(o string) bool { return isProtectedRole(o) || o == p.Role })
		if revoke {
			add(true, []string{"ALL"}, "object", obj, "")
		} else {
			add(false, []string{"USAGE"}, "object", obj, "")
		}
		if p.Access == "read" {
			// Drop any write rights, then grant reads.
			add(true, tablePrivs, "all-tables", obj, "")
			add(true, seqPrivs, "all-sequences", obj, "")
			for _, o := range owners {
				add(true, tablePrivs, "default-tables", obj, o)
				add(true, seqPrivs, "default-sequences", obj, o)
			}
			add(false, []string{"SELECT"}, "all-tables", obj, "")
			add(false, []string{"SELECT"}, "all-sequences", obj, "")
			for _, o := range owners {
				add(false, []string{"SELECT"}, "default-tables", obj, o)
				add(false, []string{"SELECT"}, "default-sequences", obj, o)
			}
			continue
		}
		add(revoke, tablePrivs, "all-tables", obj, "")
		add(revoke, seqPrivs, "all-sequences", obj, "")
		for _, o := range owners {
			add(revoke, tablePrivs, "default-tables", obj, o)
			add(revoke, seqPrivs, "default-sequences", obj, o)
		}
	}
	return m.ApplyPgPrivileges(ctx, d, db, changes)
}

// ── extensions ──────────────────────────────────────────────────────────────

// pgExtensionAllowList is what the image ships and the platform offers.
var pgExtensionAllowList = []string{
	"vector", "timescaledb", "pg_duckdb", "postgis", "postgis_topology", "postgis_raster", "postgis_tiger_geocoder", "pg_partman", "pg_cron", "hypopg",
	"pg_stat_statements", "pg_trgm", "pgcrypto", "hstore", "btree_gin", "btree_gist", "postgres_fdw", "uuid-ossp", "tablefunc",
	"citext", "ltree", "fuzzystrmatch", "unaccent", "intarray", "cube", "earthdistance", "pg_buffercache", "pgstattuple",
	"pg_prewarm", "amcheck", "dblink", "tsm_system_rows", "tsm_system_time", "isn", "seg", "bloom", "pg_visibility", "plpgsql",
}

// PgExtension is an extension available in the image.
type PgExtension struct {
	Name             string `json:"name"`
	DefaultVersion   string `json:"defaultVersion"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	Schema           string `json:"schema,omitempty"`
	Comment          string `json:"comment"`
	Preloaded        bool   `json:"preloaded"`       // its library is loaded (add-ons that need one)
	Addon            string `json:"addon,omitempty"` // the add-on that offers it ("" = PostgreSQL's own)
	Enabled          bool   `json:"enabled"`         // installable: PostgreSQL's own, or an enabled add-on
}

// PgExtensions lists the offered extensions and which are installed in db.
func (m *Manager) PgExtensions(ctx context.Context, d store.Database, db string) ([]PgExtension, error) {
	var out []PgExtension
	spec, _ := parseSpec(d.Spec)
	spec.ForEngine(d.Engine)
	addons := enabledAddons(*spec.Postgres)
	err := m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
		var loaded string
		if err := c.QueryRow(ctx, `SELECT current_setting('shared_preload_libraries')`).Scan(&loaded); err != nil {
			return err
		}
		rows, err := c.Query(ctx, `
SELECT a.name, coalesce(a.default_version, ''), coalesce(e.extversion, ''), coalesce(n.nspname, ''), coalesce(a.comment, '')
FROM pg_available_extensions a LEFT JOIN pg_extension e ON e.extname = a.name LEFT JOIN pg_namespace n ON n.oid = e.extnamespace
WHERE a.name = ANY($1) ORDER BY e.extname IS NULL, a.name`, pgExtensionAllowList)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x PgExtension
			if err := rows.Scan(&x.Name, &x.DefaultVersion, &x.InstalledVersion, &x.Schema, &x.Comment); err != nil {
				return err
			}
			x.Addon = addonOf(x.Name)
			x.Enabled = x.Addon == "" || slices.Contains(addons, x.Addon)
			if a, ok := pgAddon(x.Addon); ok && a.Preload != "" {
				x.Preloaded = slices.Contains(strings.Split(strings.ReplaceAll(loaded, " ", ""), ","), a.Preload)
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// PgExtensionChange installs, updates or drops an extension.
type PgExtensionChange struct {
	Name    string `json:"name"`
	Action  string `json:"action"`           // install, update, drop
	Schema  string `json:"schema,omitempty"` // install
	Cascade bool   `json:"cascade,omitempty"`
}

// ChangePgExtension applies an extension change in db.
func (m *Manager) ChangePgExtension(ctx context.Context, d store.Database, db string, ch PgExtensionChange) error {
	if !slices.Contains(pgExtensionAllowList, ch.Name) {
		return invalidf("extension %q is not offered", ch.Name)
	}
	if ch.Name == "pg_cron" && db != "" && db != PgDatabase(d) {
		return invalidf("pg_cron runs in the cluster's own database (%s); schedule jobs in other databases with cron.schedule_in_database", PgDatabase(d))
	}
	var stmt string
	addon, _ := pgAddon(addonOf(ch.Name))
	if ch.Action == "install" && addon.Name != "" {
		spec, _ := parseSpec(d.Spec)
		spec.ForEngine(d.Engine)
		if !slices.Contains(enabledAddons(*spec.Postgres), addon.Name) {
			return invalidf("%s is an add-on that is not enabled on %s: enable it in the database's settings first (synctl db addon enable %s %s)", addon.Title, d.Name, d.Name, addon.Name)
		}
	}
	switch ch.Action {
	case "install":
		stmt = "CREATE EXTENSION IF NOT EXISTS " + qi(ch.Name)
		if ch.Schema != "" {
			if err := checkIdent("schema", ch.Schema); err != nil {
				return err
			}
			stmt += " SCHEMA " + qi(ch.Schema)
		}
		if ch.Cascade {
			stmt += " CASCADE"
		}
	case "update":
		stmt = "ALTER EXTENSION " + qi(ch.Name) + " UPDATE"
	case "drop":
		stmt = "DROP EXTENSION IF EXISTS " + qi(ch.Name)
		if ch.Cascade {
			stmt += " CASCADE"
		}
	default:
		return invalidf("action %q: use install, update or drop", ch.Action)
	}
	return m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
		if ch.Action == "install" && addon.Preload != "" {
			var loaded string
			if err := c.QueryRow(ctx, `SELECT current_setting('shared_preload_libraries')`).Scan(&loaded); err != nil {
				return err
			}
			if !slices.Contains(strings.Split(strings.ReplaceAll(loaded, " ", ""), ","), addon.Preload) {
				return invalidf("%s is enabled, and its library loads once the members restart (they do so one at a time): try again in a minute", addon.Title)
			}
		}
		_, err := c.Exec(ctx, stmt)
		return err
	})
}
