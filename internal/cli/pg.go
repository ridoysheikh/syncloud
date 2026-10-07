package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// PostgreSQL explorer and administration commands under `synctl db`.

func pgPath(name, rest string, db string) string {
	p := dbItem(name) + "/pg" + rest
	if db != "" {
		p += "?db=" + url.QueryEscape(db)
	}
	return p
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// pgObjectRef parses KIND:NAME (database:shop, schema:public,
// table:public.items, sequence:public.items_id_seq, function:OID).
func pgObjectRef(s string) (map[string]any, error) {
	kind, name, ok := strings.Cut(s, ":")
	if !ok || name == "" {
		return nil, errors.New("--on takes KIND:NAME, e.g. table:public.items, schema:public, database:shop, function:16402")
	}
	ref := map[string]any{"kind": kind}
	switch kind {
	case "database", "schema":
		ref["name"] = name
	case "table", "sequence":
		schema, rel, ok := strings.Cut(name, ".")
		if !ok {
			schema, rel = "public", name
		}
		ref["schema"], ref["name"] = schema, rel
	case "function":
		oid, err := strconv.Atoi(name)
		if err != nil {
			return nil, errors.New("functions are named by OID (synctl db schema shows them)")
		}
		ref["oid"] = oid
	default:
		return nil, fmt.Errorf("kind %q: use database, schema, table, sequence or function", kind)
	}
	return ref, nil
}

func splitQualified(s string) (string, string) {
	if schema, name, ok := strings.Cut(s, "."); ok {
		return schema, name
	}
	return "public", s
}

func pgCommands(a *app) []*cobra.Command {
	// ── console ──
	var sqlDB, sqlAs string
	var write bool
	var maxRows int
	sqlCmd := &cobra.Command{
		Use: "sql NAME [SQL]", Aliases: []string{"psql"}, Short: "Run SQL on a PostgreSQL database (read-only unless --write)",
		Long: "Runs one or more statements as the app user (or --as a role it may become). Without --write every statement runs in a read-only transaction. SQL comes from the argument, or from stdin when it is missing or \"-\".",
		Example: `  synctl db sql orders "SELECT count(*) FROM items"
  synctl db sql orders --write "CREATE INDEX ON items (name)"
  synctl db sql orders --as reporting "SELECT * FROM sales LIMIT 5"
  synctl db sql orders --write - < migration.sql`,
		Args: cobra.RangeArgs(1, 2), Annotations: op("runPgQuery", "executePgQuery"),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := ""
			if len(args) == 2 && args[1] != "-" {
				text = args[1]
			} else {
				b, err := io.ReadAll(a.in)
				if err != nil {
					return err
				}
				text = string(b)
			}
			var out struct {
				Results []struct {
					Statement string `json:"statement"`
					Columns   []struct {
						Name string `json:"name"`
					} `json:"columns"`
					Rows       [][]*string `json:"rows"`
					Truncated  bool        `json:"truncated"`
					Tag        string      `json:"tag"`
					DurationMs float64     `json:"durationMs"`
				} `json:"results"`
				Error *struct {
					Statement int    `json:"statement"`
					Message   string `json:"message"`
					Code      string `json:"code"`
					Detail    string `json:"detail"`
					Hint      string `json:"hint"`
				} `json:"error"`
				RolledBack bool `json:"rolledBack"`
			}
			path := "/query"
			if write {
				path = "/execute"
			}
			body := map[string]any{"database": sqlDB, "sql": text, "role": sqlAs, "maxRows": maxRows}
			if err := a.do(cmd, "POST", dbItem(args[0])+"/pg"+path, body, &out); err != nil {
				return err
			}
			if a.output == "json" {
				if err := a.printer().json(out); err != nil {
					return err
				}
			} else {
				for _, r := range out.Results {
					if len(r.Columns) > 0 {
						headers := make([]string, len(r.Columns))
						for i, c := range r.Columns {
							headers[i] = c.Name
						}
						rows := make([][]string, len(r.Rows))
						for i, row := range r.Rows {
							rows[i] = make([]string, len(row))
							for j, v := range row {
								if v == nil {
									rows[i][j] = "NULL"
								} else {
									rows[i][j] = *v
								}
							}
						}
						printer{w: a.out, format: "table"}.table(nil, headers, rows) //nolint:errcheck
					}
					more := ""
					if r.Truncated {
						more = ", more rows not shown"
					}
					fmt.Fprintf(a.out, "%s (%.1f ms%s)\n", r.Tag, r.DurationMs, more)
				}
				if out.RolledBack {
					fmt.Fprintln(a.errOut, "warning: a transaction was left open and has been rolled back")
				}
			}
			if e := out.Error; e != nil {
				msg := fmt.Sprintf("statement %d: %s", e.Statement+1, e.Message)
				if e.Code != "" {
					msg += " [SQLSTATE " + e.Code + "]"
				}
				if e.Detail != "" {
					msg += "\n  detail: " + e.Detail
				}
				if e.Hint != "" {
					msg += "\n  hint: " + e.Hint
				}
				return errors.New(msg)
			}
			return nil
		},
	}
	sqlCmd.Flags().StringVar(&sqlDB, "db", "", "database in the cluster (default: the cluster's own)")
	sqlCmd.Flags().StringVar(&sqlAs, "as", "", "run as this role (default app)")
	sqlCmd.Flags().BoolVar(&write, "write", false, "allow writes (audited with the SQL)")
	sqlCmd.Flags().IntVar(&maxRows, "max-rows", 0, "rows to return per statement (at most 1000)")

	// ── databases ──
	databases := &cobra.Command{
		Use: "databases NAME", Short: "List the databases in a PostgreSQL cluster", Args: cobra.ExactArgs(1), Annotations: op("listPgDatabases"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Items []struct {
					Name        string `json:"name"`
					Owner       string `json:"owner"`
					SizeBytes   int64  `json:"sizeBytes"`
					Connections int    `json:"connections"`
					Encoding    string `json:"encoding"`
					Primary     bool   `json:"primary"`
				} `json:"items"`
			}
			if err := a.do(cmd, "GET", pgPath(args[0], "/databases", ""), nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, d := range out.Items {
				name := d.Name
				if d.Primary {
					name += " *"
				}
				rows = append(rows, []string{name, d.Owner, humanBytes(d.SizeBytes), strconv.Itoa(d.Connections), d.Encoding})
			}
			return a.printer().table(out.Items, []string{"DATABASE", "OWNER", "SIZE", "CONNECTIONS", "ENCODING"}, rows)
		},
	}
	database := &cobra.Command{Use: "database", Short: "Create, change or drop a database in a PostgreSQL cluster"}
	var dbOwner, dbRename string
	var dbConnLimit int
	dbCreate := &cobra.Command{
		Use: "create NAME DATABASE", Short: "Create a database", Args: cobra.ExactArgs(2), Annotations: op("createPgDatabase"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"name": args[1], "owner": dbOwner}
			if cmd.Flags().Changed("conn-limit") {
				body["connLimit"] = dbConnLimit
			}
			if err := a.do(cmd, "POST", pgPath(args[0], "/databases", ""), body, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Created database %s\n", args[1])
			return nil
		},
	}
	dbAlter := &cobra.Command{
		Use: "alter NAME DATABASE", Short: "Rename a database or change its owner or connection limit", Args: cobra.ExactArgs(2), Annotations: op("alterPgDatabase"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"name": dbRename, "owner": dbOwner}
			if cmd.Flags().Changed("conn-limit") {
				body["connLimit"] = dbConnLimit
			}
			return a.do(cmd, "PUT", dbItem(args[0])+"/pg/databases/"+url.PathEscape(args[1]), body, nil)
		},
	}
	dbDrop := &cobra.Command{
		Use: "drop NAME DATABASE", Short: "Drop a database (its sessions are disconnected)", Args: cobra.ExactArgs(2), Annotations: op("dropPgDatabase"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.do(cmd, "DELETE", dbItem(args[0])+"/pg/databases/"+url.PathEscape(args[1]), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Dropped database %s\n", args[1])
			return nil
		},
	}
	for _, c := range []*cobra.Command{dbCreate, dbAlter} {
		c.Flags().StringVar(&dbOwner, "owner", "", "owner role (default app)")
		c.Flags().IntVar(&dbConnLimit, "conn-limit", -1, "connection limit (-1 = none)")
	}
	dbAlter.Flags().StringVar(&dbRename, "rename", "", "new name")
	database.AddCommand(dbCreate, dbAlter, dbDrop)

	// ── roles ──
	var allRoles bool
	roles := &cobra.Command{
		Use: "roles NAME", Short: "List the roles of a PostgreSQL cluster", Args: cobra.ExactArgs(1), Annotations: op("listPgRoles"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Items []pgRoleView `json:"items"`
			}
			if err := a.do(cmd, "GET", pgPath(args[0], "/roles", ""), nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			var shown []pgRoleView
			for _, r := range out.Items {
				if r.Predefined && !allRoles {
					continue
				}
				shown = append(shown, r)
				rows = append(rows, []string{r.Name, yesNo(r.Login), r.attributes(), r.memberOf(), strconv.Itoa(r.Connections)})
			}
			return a.printer().table(shown, []string{"ROLE", "LOGIN", "ATTRIBUTES", "MEMBER OF", "CONNECTIONS"}, rows)
		},
	}
	roles.Flags().BoolVar(&allRoles, "all", false, "include the predefined pg_* roles")

	role := &cobra.Command{Use: "role", Short: "Show, create, change or drop a PostgreSQL role"}
	roleGet := &cobra.Command{
		Use: "get NAME ROLE", Short: "Show a role", Args: cobra.ExactArgs(2), Annotations: op("getPgRole"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r map[string]any
			if err := a.do(cmd, "GET", dbItem(args[0])+"/pg/roles/"+url.PathEscape(args[1]), nil, &r); err != nil {
				return err
			}
			return a.printer().json(r)
		},
	}
	var rf struct {
		password, validUntil, rename, comment, reassign string
		generate, login, createDB, createRole, inherit  bool
		connLimit                                       int
		memberOf                                        []string
	}
	roleBody := func(cmd *cobra.Command) (map[string]any, error) {
		b := map[string]any{}
		f := cmd.Flags()
		if f.Changed("password") {
			b["password"] = rf.password
		}
		if rf.generate {
			b["generatePassword"] = true
		}
		for flag, key := range map[string]string{"login": "login", "createdb": "createDb", "createrole": "createRole", "inherit": "inherit"} {
			if f.Changed(flag) {
				v, _ := f.GetBool(flag)
				b[key] = v
			}
		}
		if f.Changed("conn-limit") {
			b["connLimit"] = rf.connLimit
		}
		if f.Changed("valid-until") {
			b["validUntil"] = rf.validUntil
		}
		if f.Changed("comment") {
			b["comment"] = rf.comment
		}
		if f.Changed("member-of") {
			ms := []map[string]any{}
			for _, m := range rf.memberOf {
				if m == "" {
					continue
				}
				name, opt, _ := strings.Cut(m, ":")
				if opt != "" && opt != "admin" {
					return nil, fmt.Errorf("--member-of %s: use ROLE or ROLE:admin", m)
				}
				ms = append(ms, map[string]any{"role": name, "admin": opt == "admin", "inherit": true})
			}
			b["memberOf"] = ms
		}
		return b, nil
	}
	printRole := func(r map[string]any, verb string) {
		fmt.Fprintf(a.out, "%s role %v\n", verb, r["name"])
		if pw, ok := r["password"].(string); ok && pw != "" {
			fmt.Fprintf(a.out, "password: %s\n(shown once; it is not stored in SynCloud)\n", pw)
		}
	}
	roleCreate := &cobra.Command{
		Use: "create NAME ROLE", Short: "Create a role (login roles get a generated password unless --password is given)", Args: cobra.ExactArgs(2), Annotations: op("createPgRole"),
		Example: `  synctl db role create orders reporting --member-of pg_read_all_data
  synctl db role create orders analysts --login=false`,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := roleBody(cmd)
			if err != nil {
				return err
			}
			b["name"] = args[1]
			var r map[string]any
			if err := a.do(cmd, "POST", dbItem(args[0])+"/pg/roles", b, &r); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(r)
			}
			printRole(r, "Created")
			return nil
		},
	}
	roleAlter := &cobra.Command{
		Use: "alter NAME ROLE", Short: "Change a role's attributes, password, memberships or name", Args: cobra.ExactArgs(2), Annotations: op("alterPgRole"),
		Example: `  synctl db role alter orders reporting --generate-password
  synctl db role alter orders reporting --conn-limit 5 --valid-until 2027-01-01T00:00:00Z`,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := roleBody(cmd)
			if err != nil {
				return err
			}
			if rf.rename != "" {
				b["name"] = rf.rename
			}
			var r map[string]any
			if err := a.do(cmd, "PUT", dbItem(args[0])+"/pg/roles/"+url.PathEscape(args[1]), b, &r); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(r)
			}
			printRole(r, "Updated")
			return nil
		},
	}
	for _, c := range []*cobra.Command{roleCreate, roleAlter} {
		f := c.Flags()
		f.StringVar(&rf.password, "password", "", "set this password (empty removes it)")
		f.BoolVar(&rf.generate, "generate-password", false, "generate a password and print it once")
		f.BoolVar(&rf.login, "login", true, "may sign in")
		f.BoolVar(&rf.createDB, "createdb", false, "may create databases")
		f.BoolVar(&rf.createRole, "createrole", false, "may create and manage roles")
		f.BoolVar(&rf.inherit, "inherit", true, "inherits the rights of roles it is a member of")
		f.IntVar(&rf.connLimit, "conn-limit", -1, "connection limit (-1 = none)")
		f.StringVar(&rf.validUntil, "valid-until", "", "password expiry, RFC 3339 (empty = never)")
		f.StringSliceVar(&rf.memberOf, "member-of", nil, "roles it belongs to (the full list), ROLE or ROLE:admin")
		f.StringVar(&rf.comment, "comment", "", "comment")
	}
	roleAlter.Flags().StringVar(&rf.rename, "rename", "", "new name")
	roleDrop := &cobra.Command{
		Use: "drop NAME ROLE", Short: "Drop a role; its objects in every database go to --reassign-to (default app)", Args: cobra.ExactArgs(2), Annotations: op("dropPgRole"),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := dbItem(args[0]) + "/pg/roles/" + url.PathEscape(args[1])
			if rf.reassign != "" {
				p += "?reassignTo=" + url.QueryEscape(rf.reassign)
			}
			if err := a.do(cmd, "DELETE", p, nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Dropped role %s\n", args[1])
			return nil
		},
	}
	roleDrop.Flags().StringVar(&rf.reassign, "reassign-to", "", "role that takes over its objects (default app)")
	role.AddCommand(roleGet, roleCreate, roleAlter, roleDrop)

	// ── privileges ──
	var gf struct {
		db, on, access, forRole string
		privileges, schemas     []string
		grantOption, all        bool
	}
	grantFor := func(revoke bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			var body map[string]any
			switch {
			case gf.access != "":
				if revoke {
					return errors.New("--access none revokes; use grant --access none")
				}
				body = map[string]any{"preset": map[string]any{"role": args[1], "access": gf.access, "schemas": gf.schemas}}
			case len(gf.privileges) > 0 && gf.on != "":
				ch := map[string]any{"revoke": revoke, "privileges": gf.privileges, "grantee": args[1], "grantOption": gf.grantOption}
				kind, rest, _ := strings.Cut(gf.on, ":")
				switch {
				case strings.HasPrefix(kind, "all-") || strings.HasPrefix(kind, "default-"):
					ch["target"], ch["object"], ch["forRole"] = kind, map[string]any{"schema": rest}, gf.forRole
				default:
					ref, err := pgObjectRef(gf.on)
					if err != nil {
						return err
					}
					ch["target"], ch["object"] = "object", ref
				}
				body = map[string]any{"changes": []any{ch}}
			default:
				return errors.New("give --access read|write|none, or --privileges with --on")
			}
			var out struct {
				Statements []string `json:"statements"`
			}
			if err := a.do(cmd, "POST", pgPath(args[0], "/privileges", gf.db), body, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			for _, s := range out.Statements {
				fmt.Fprintln(a.out, s+";")
			}
			return nil
		}
	}
	grant := &cobra.Command{
		Use: "grant NAME ROLE", Short: "Grant privileges to a role, or give it read, write or no access to a database",
		Example: `  synctl db grant orders reporting --access read
  synctl db grant orders app_writer --access write --db analytics
  synctl db grant orders reporting --privileges SELECT --on table:public.items
  synctl db grant orders reporting --privileges SELECT --on all-tables:public
  synctl db grant orders reporting --privileges SELECT --on default-tables:public --for-role app`,
		Args: cobra.ExactArgs(2), Annotations: op("changePgPrivileges"), RunE: grantFor(false),
	}
	revoke := &cobra.Command{
		Use: "revoke NAME ROLE", Short: "Revoke privileges from a role", Args: cobra.ExactArgs(2),
		Example: `  synctl db revoke orders reporting --privileges ALL --on table:public.items`,
		RunE:    grantFor(true),
	}
	for _, c := range []*cobra.Command{grant, revoke} {
		f := c.Flags()
		f.StringVar(&gf.db, "db", "", "database in the cluster (default: the cluster's own)")
		f.StringVar(&gf.on, "on", "", "KIND:NAME (table:public.items, schema:public, database:shop, function:OID), or all-tables|all-sequences|all-functions|default-tables|default-sequences|default-functions:SCHEMA")
		f.StringSliceVar(&gf.privileges, "privileges", nil, "privileges, e.g. SELECT,INSERT or ALL")
		f.BoolVar(&gf.grantOption, "grant-option", false, "with grant option (revoke: only the grant option)")
		f.StringVar(&gf.forRole, "for-role", "", "default-*: objects created by this role")
	}
	grant.Flags().StringVar(&gf.access, "access", "", "read, write or none on the whole database (every schema, existing and future tables)")
	grant.Flags().StringSliceVar(&gf.schemas, "schema", nil, "--access: only these schemas")
	var privRole string
	privileges := &cobra.Command{
		Use: "privileges NAME", Short: "Show the grants on an object (and a role's effective rights)", Args: cobra.ExactArgs(1), Annotations: op("getPgPrivileges"),
		Example: `  synctl db privileges orders --on table:public.items --role reporting`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := pgObjectRef(gf.on)
			if err != nil {
				return err
			}
			q := url.Values{}
			for k, v := range ref {
				q.Set(k, fmt.Sprint(v))
			}
			if gf.db != "" {
				q.Set("db", gf.db)
			}
			if privRole != "" {
				q.Set("role", privRole)
			}
			var out struct {
				Object    string `json:"object"`
				Owner     string `json:"owner"`
				Available []string
				Grants    []struct {
					Grantee    string          `json:"grantee"`
					Privileges map[string]bool `json:"privileges"`
				} `json:"grants"`
				Effective map[string]bool `json:"effective"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/pg/privileges?"+q.Encode(), nil, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			fmt.Fprintf(a.out, "%s (owner %s)\n", out.Object, out.Owner)
			rows := [][]string{}
			for _, g := range out.Grants {
				var ps []string
				for p, opt := range g.Privileges {
					if opt {
						p += "*"
					}
					ps = append(ps, p)
				}
				sort.Strings(ps)
				rows = append(rows, []string{g.Grantee, strings.Join(ps, ", ")})
			}
			printer{w: a.out, format: "table"}.table(nil, []string{"GRANTEE", "PRIVILEGES (* = with grant option)"}, rows) //nolint:errcheck
			if out.Effective != nil {
				var ps []string
				for p, ok := range out.Effective {
					if ok {
						ps = append(ps, p)
					}
				}
				sort.Strings(ps)
				fmt.Fprintf(a.out, "%s can: %s\n", privRole, strings.Join(ps, ", "))
			}
			return nil
		},
	}
	privileges.Flags().StringVar(&gf.on, "on", "", "KIND:NAME (table:public.items, schema:public, database:shop, function:OID)")
	privileges.Flags().StringVar(&gf.db, "db", "", "database in the cluster (default: the cluster's own)")
	privileges.Flags().StringVar(&privRole, "role", "", "also show what this role can do")

	// ── schema ──
	var schemaDB string
	var system bool
	schema := &cobra.Command{
		Use: "schema NAME", Short: "List schemas and their tables, views, sequences, routines and types", Args: cobra.ExactArgs(1), Annotations: op("getPgSchema"),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := pgPath(args[0], "/schema", schemaDB)
			if system {
				p += map[bool]string{true: "&", false: "?"}[schemaDB != ""] + "system=true"
			}
			var out struct {
				Schemas []struct {
					Name    string `json:"name"`
					Objects []struct {
						Name        string `json:"name"`
						Kind        string `json:"kind"`
						OID         int    `json:"oid"`
						RowEstimate int64  `json:"rowEstimate"`
						SizeBytes   int64  `json:"sizeBytes"`
					} `json:"objects"`
				} `json:"schemas"`
			}
			if err := a.do(cmd, "GET", p, nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, s := range out.Schemas {
				if len(s.Objects) == 0 {
					rows = append(rows, []string{s.Name, "", "(empty)", "", ""})
				}
				for _, o := range s.Objects {
					name := o.Name
					if o.Kind == "function" || o.Kind == "procedure" || o.Kind == "aggregate" {
						name += fmt.Sprintf("  [oid %d]", o.OID)
					}
					size := ""
					if o.SizeBytes > 0 {
						size = humanBytes(o.SizeBytes)
					}
					rowsEst := ""
					if o.RowEstimate > 0 {
						rowsEst = strconv.FormatInt(o.RowEstimate, 10)
					}
					rows = append(rows, []string{s.Name, o.Kind, name, rowsEst, size})
				}
			}
			return a.printer().table(out.Schemas, []string{"SCHEMA", "KIND", "NAME", "ROWS (EST.)", "SIZE"}, rows)
		},
	}
	schema.Flags().StringVar(&schemaDB, "db", "", "database in the cluster (default: the cluster's own)")
	schema.Flags().BoolVar(&system, "system", false, "include catalog and extension schemas")

	var ddl bool
	describe := &cobra.Command{
		Use: "describe NAME [SCHEMA.]OBJECT", Aliases: []string{"desc"}, Short: "Describe a table, view, sequence or type (--ddl prints its CREATE statement)",
		Args: cobra.ExactArgs(2), Annotations: op("getPgObject"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, n := splitQualified(args[1])
			q := url.Values{"schema": {s}, "name": {n}}
			if oid, err := strconv.Atoi(args[1]); err == nil {
				q = url.Values{"oid": {strconv.Itoa(oid)}}
			}
			if schemaDB != "" {
				q.Set("db", schemaDB)
			}
			var out struct {
				Kind    string `json:"kind"`
				DDL     string `json:"ddl"`
				Columns []struct {
					Name    string `json:"name"`
					Type    string `json:"type"`
					NotNull bool   `json:"notNull"`
					Default string `json:"default"`
				} `json:"columns"`
				Indexes []struct {
					Definition string `json:"definition"`
				} `json:"indexes"`
				Constraints []struct {
					Name       string `json:"name"`
					Definition string `json:"definition"`
				} `json:"constraints"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/pg/object?"+q.Encode(), nil, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			if ddl || len(out.Columns) == 0 {
				fmt.Fprint(a.out, out.DDL)
				return nil
			}
			rows := [][]string{}
			for _, c := range out.Columns {
				null := "null"
				if c.NotNull {
					null = "not null"
				}
				rows = append(rows, []string{c.Name, c.Type, null, c.Default})
			}
			printer{w: a.out, format: "table"}.table(nil, []string{"COLUMN", "TYPE", "NULLABLE", "DEFAULT"}, rows) //nolint:errcheck
			for _, k := range out.Constraints {
				fmt.Fprintf(a.out, "constraint %s %s\n", k.Name, k.Definition)
			}
			for _, i := range out.Indexes {
				fmt.Fprintf(a.out, "%s\n", i.Definition)
			}
			return nil
		},
	}
	describe.Flags().StringVar(&schemaDB, "db", "", "database in the cluster (default: the cluster's own)")
	describe.Flags().BoolVar(&ddl, "ddl", false, "print the CREATE statement")

	var rq struct {
		limit, offset int
		order         string
		desc, count   bool
		where         []string
	}
	rowsCmd := &cobra.Command{
		Use: "rows NAME [SCHEMA.]TABLE", Short: "Read a page of a table's rows", Args: cobra.ExactArgs(2), Annotations: op("readPgRows"),
		Example: `  synctl db rows orders public.items --limit 20 --order id --desc
  synctl db rows orders items --where "status=open" --where "total>=100" --count`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t := splitQualified(args[1])
			var filters []map[string]string
			for _, w := range rq.where {
				f, err := parseWhere(w)
				if err != nil {
					return err
				}
				filters = append(filters, f)
			}
			body := map[string]any{"schema": s, "table": t, "limit": rq.limit, "offset": rq.offset, "orderBy": rq.order, "desc": rq.desc, "count": rq.count, "filters": filters}
			var out struct {
				Columns []struct {
					Name string `json:"name"`
				} `json:"columns"`
				Rows  [][]*string `json:"rows"`
				More  bool        `json:"more"`
				Total *int64      `json:"total"`
			}
			if err := a.do(cmd, "POST", pgPath(args[0], "/rows", schemaDB), body, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			headers := make([]string, len(out.Columns))
			for i, c := range out.Columns {
				headers[i] = c.Name
			}
			rows := make([][]string, len(out.Rows))
			for i, r := range out.Rows {
				rows[i] = make([]string, len(r))
				for j, v := range r {
					rows[i][j] = "NULL"
					if v != nil {
						rows[i][j] = *v
					}
				}
			}
			printer{w: a.out, format: "table"}.table(nil, headers, rows) //nolint:errcheck
			if out.Total != nil {
				fmt.Fprintf(a.out, "%d matching rows\n", *out.Total)
			} else if out.More {
				fmt.Fprintln(a.out, "(more rows: use --offset)")
			}
			return nil
		},
	}
	f := rowsCmd.Flags()
	f.StringVar(&schemaDB, "db", "", "database in the cluster (default: the cluster's own)")
	f.IntVar(&rq.limit, "limit", 50, "rows (at most 1000)")
	f.IntVar(&rq.offset, "offset", 0, "rows to skip")
	f.StringVar(&rq.order, "order", "", "order by this column")
	f.BoolVar(&rq.desc, "desc", false, "descending order")
	f.BoolVar(&rq.count, "count", false, "also count the matching rows")
	f.StringArrayVar(&rq.where, "where", nil, "COLUMN=VALUE (also <> < <= > >= ~ for LIKE, ~* for ILIKE, :null, :notnull)")

	// ── extensions ──
	var extDB, extSchema string
	var cascade bool
	extensions := &cobra.Command{
		Use: "extensions NAME", Short: "List the offered extensions and which are installed", Args: cobra.ExactArgs(1), Annotations: op("listPgExtensions"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Items []struct {
					Name      string `json:"name"`
					Default   string `json:"defaultVersion"`
					Installed string `json:"installedVersion"`
					Schema    string `json:"schema"`
					Comment   string `json:"comment"`
				} `json:"items"`
			}
			if err := a.do(cmd, "GET", pgPath(args[0], "/extensions", extDB), nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, e := range out.Items {
				rows = append(rows, []string{e.Name, e.Installed, e.Default, e.Schema, e.Comment})
			}
			return a.printer().table(out.Items, []string{"EXTENSION", "INSTALLED", "AVAILABLE", "SCHEMA", "DESCRIPTION"}, rows)
		},
	}
	extensions.Flags().StringVar(&extDB, "db", "", "database in the cluster (default: the cluster's own)")
	extension := &cobra.Command{Use: "extension", Short: "Install, update or drop an extension"}
	for _, action := range []string{"install", "update", "drop"} {
		c := &cobra.Command{
			Use: action + " NAME EXTENSION", Short: strings.ToUpper(action[:1]) + action[1:] + " an extension", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				body := map[string]any{"name": args[1], "action": action, "schema": extSchema, "cascade": cascade}
				if err := a.do(cmd, "POST", pgPath(args[0], "/extensions", extDB), body, nil); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s: %s done\n", args[1], action)
				return nil
			},
		}
		if action == "install" {
			c.Annotations = op("changePgExtension")
			c.Flags().StringVar(&extSchema, "schema", "", "schema to install into")
		}
		if action != "update" {
			c.Flags().BoolVar(&cascade, "cascade", false, "also install dependencies (install) or drop dependent objects (drop)")
		}
		c.Flags().StringVar(&extDB, "db", "", "database in the cluster (default: the cluster's own)")
		extension.AddCommand(c)
	}

	// ── sessions ──
	var allSessions bool
	sessions := &cobra.Command{
		Use: "sessions NAME", Short: "List client sessions and what they are running", Args: cobra.ExactArgs(1), Annotations: op("listPgSessions"),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := dbItem(args[0]) + "/pg/sessions"
			if allSessions {
				p += "?all=true"
			}
			var out struct {
				Items []struct {
					PID       int    `json:"pid"`
					User      string `json:"user"`
					Database  string `json:"database"`
					Client    string `json:"client"`
					State     string `json:"state"`
					WaitEvent string `json:"waitEvent"`
					Query     string `json:"query"`
					BlockedBy []int  `json:"blockedBy"`
				} `json:"items"`
			}
			if err := a.do(cmd, "GET", p, nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, s := range out.Items {
				q := strings.Join(strings.Fields(s.Query), " ")
				if len(q) > 80 {
					q = q[:80] + "…"
				}
				blocked := ""
				if len(s.BlockedBy) > 0 {
					blocked = fmt.Sprint(s.BlockedBy)
				}
				rows = append(rows, []string{strconv.Itoa(s.PID), s.User, s.Database, s.Client, s.State, s.WaitEvent, blocked, q})
			}
			return a.printer().table(out.Items, []string{"PID", "USER", "DATABASE", "CLIENT", "STATE", "WAITING ON", "BLOCKED BY", "QUERY"}, rows)
		},
	}
	sessions.Flags().BoolVar(&allSessions, "all", false, "include the platform's sessions")
	session := &cobra.Command{Use: "session", Short: "Cancel a session's query or end the session"}
	for _, verb := range []string{"cancel", "terminate"} {
		session.AddCommand(&cobra.Command{
			Use: verb + " NAME PID", Short: map[string]string{"cancel": "Cancel the running query", "terminate": "End the session"}[verb],
			Args: cobra.ExactArgs(2), Annotations: op(verb + "PgSession"),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.do(cmd, "POST", dbItem(args[0])+"/pg/sessions/"+url.PathEscape(args[1])+"/"+verb, nil, nil)
			},
		})
	}

	// ── backups ──
	backups := &cobra.Command{
		Use: "backups NAME", Short: "Base backups in S3, recent runs, WAL archiving and the restore window", Args: cobra.ExactArgs(1),
		Annotations: op("listDatabaseBackups"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Configured bool   `json:"configured"`
				Location   string `json:"location"`
				Error      string `json:"error"`
				Backups    []struct {
					Name           string    `json:"name"`
					FinishTime     time.Time `json:"finishTime"`
					CompressedSize int64     `json:"compressedSize"`
					StartLSN       string    `json:"startLsn"`
				} `json:"backups"`
				Runs []struct {
					Member    string    `json:"member"`
					Trigger   string    `json:"trigger"`
					State     string    `json:"state"`
					Error     string    `json:"error"`
					StartedAt time.Time `json:"startedAt"`
				} `json:"runs"`
				Window *struct {
					From time.Time `json:"from"`
					To   time.Time `json:"to"`
				} `json:"window"`
				Archiver *struct {
					LastAt      *time.Time `json:"lastArchivedAt"`
					FailedCount int64      `json:"failedCount"`
				} `json:"archiver"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/backups", nil, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			if !out.Configured {
				fmt.Fprintf(a.out, "Backups are off. Turn them on with: synctl db backup config %s --endpoint E --bucket B\n", args[0])
				return nil
			}
			fmt.Fprintf(a.out, "Archive:  %s\n", out.Location)
			if out.Archiver != nil {
				last := "nothing yet"
				if out.Archiver.LastAt != nil {
					last = out.Archiver.LastAt.Local().Format(time.RFC3339)
				}
				fmt.Fprintf(a.out, "WAL:      last archived %s, %d failures\n", last, out.Archiver.FailedCount)
			}
			if out.Window != nil {
				fmt.Fprintf(a.out, "Restore:  any moment from %s to %s\n", out.Window.From.Format(time.RFC3339), out.Window.To.Format(time.RFC3339))
			}
			if out.Error != "" {
				fmt.Fprintf(a.out, "S3:       %s\n", out.Error)
			}
			rows := [][]string{}
			for _, b := range out.Backups {
				rows = append(rows, []string{b.Name, b.FinishTime.Format(time.RFC3339), humanBytes(b.CompressedSize), b.StartLSN})
			}
			fmt.Fprintln(a.out)
			printer{w: a.out, format: "table"}.table(nil, []string{"BACKUP", "FINISHED", "SIZE", "START LSN"}, rows) //nolint:errcheck
			if len(out.Runs) > 0 {
				fmt.Fprintln(a.out)
				rows = [][]string{}
				for i, r := range out.Runs {
					if i == 5 {
						break
					}
					rows = append(rows, []string{r.StartedAt.Local().Format(time.RFC3339), r.Trigger, r.Member, r.State, r.Error})
				}
				printer{w: a.out, format: "table"}.table(nil, []string{"RUN", "TRIGGER", "FROM", "STATE", "ERROR"}, rows) //nolint:errcheck
			}
			return nil
		},
	}
	backup := &cobra.Command{Use: "backup", Short: "Take a base backup now, or set up backups"}
	backupNow := &cobra.Command{
		Use: "now NAME", Short: "Take a base backup now", Args: cobra.ExactArgs(1), Annotations: op("startDatabaseBackup"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				ID     string `json:"id"`
				Member string `json:"member"`
			}
			if err := a.do(cmd, "POST", dbItem(args[0])+"/backups", nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Base backup started from %s (%s); follow it with: synctl db backups %s\n", out.Member, out.ID, args[0])
			return nil
		},
	}
	var bc struct {
		endpoint, bucket, prefix string
		every, full, days        int
		off                      bool
	}
	backupConfig := &cobra.Command{
		Use: "config NAME", Short: "Turn on WAL archiving and base backups to an S3 endpoint (or --off)", Args: cobra.ExactArgs(1),
		Example: `  synctl db backup config orders --endpoint minio --bucket pg-backups
  synctl db backup config orders --every 6 --retain-days 14
  synctl db backup config orders --off`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var cur struct {
				Spec map[string]any `json:"spec"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0]), nil, &cur); err != nil {
				return err
			}
			pg, _ := cur.Spec["postgres"].(map[string]any)
			if pg == nil {
				return errors.New(args[0] + " is not a PostgreSQL database")
			}
			if bc.off {
				delete(pg, "backup")
			} else {
				b, _ := pg["backup"].(map[string]any)
				if b == nil {
					b = map[string]any{}
				}
				f := cmd.Flags()
				for flag, key := range map[string]string{"endpoint": "endpoint", "bucket": "bucket", "prefix": "prefix"} {
					if f.Changed(flag) {
						v, _ := f.GetString(flag)
						b[key] = v
					}
				}
				for flag, key := range map[string]string{"every": "everyHours", "retain-full": "retainFull", "retain-days": "retainDays"} {
					if f.Changed(flag) {
						v, _ := f.GetInt(flag)
						b[key] = v
					}
				}
				pg["backup"] = b
			}
			if err := a.do(cmd, "PUT", dbItem(args[0]), map[string]any{"spec": cur.Spec}, nil); err != nil {
				return err
			}
			if bc.off {
				fmt.Fprintf(a.out, "Backups of %s are off (members restart one at a time; what is in S3 stays)\n", args[0])
			} else {
				fmt.Fprintf(a.out, "Backups of %s are on (members restart one at a time to start archiving)\n", args[0])
			}
			return nil
		},
	}
	f2 := backupConfig.Flags()
	f2.StringVar(&bc.endpoint, "endpoint", "", "S3 endpoint (synctl s3 endpoints)")
	f2.StringVar(&bc.bucket, "bucket", "", "bucket")
	f2.StringVar(&bc.prefix, "prefix", "", "prefix in the bucket (default syncloud-pg/<database ID>)")
	f2.IntVar(&bc.every, "every", 24, "hours between base backups")
	f2.IntVar(&bc.full, "retain-full", 7, "base backups to keep")
	f2.IntVar(&bc.days, "retain-days", 7, "also keep every backup of the last N days (how far back a restore can go)")
	f2.BoolVar(&bc.off, "off", false, "stop archiving and base backups")
	backup.AddCommand(backupNow, backupConfig)

	return []*cobra.Command{sqlCmd, databases, database, roles, role, grant, revoke, privileges, schema, describe, rowsCmd, extensions, extension, sessions, session, backups, backup}
}

// pgRoleView is a role as the CLI lists it.
type pgRoleView struct {
	Name       string `json:"name"`
	Login      bool   `json:"login"`
	CreateDB   bool   `json:"createDb"`
	CreateRole bool   `json:"createRole"`
	Superuser  bool   `json:"superuser"`
	Inherit    bool   `json:"inherit"`
	ConnLimit  int    `json:"connLimit"`
	MemberOf   []struct {
		Role  string `json:"role"`
		Admin bool   `json:"admin"`
	} `json:"memberOf"`
	Connections int  `json:"connections"`
	Predefined  bool `json:"predefined"`
	Protected   bool `json:"protected"`
}

func (r pgRoleView) attributes() string {
	var a []string
	if r.Superuser {
		a = append(a, "superuser")
	}
	if r.CreateDB {
		a = append(a, "createdb")
	}
	if r.CreateRole {
		a = append(a, "createrole")
	}
	if !r.Inherit {
		a = append(a, "noinherit")
	}
	if r.ConnLimit >= 0 {
		a = append(a, fmt.Sprintf("limit %d", r.ConnLimit))
	}
	if r.Protected {
		a = append(a, "platform")
	}
	return strings.Join(a, ", ")
}

func (r pgRoleView) memberOf() string {
	var m []string
	for _, g := range r.MemberOf {
		if g.Admin {
			m = append(m, g.Role+" (admin)")
		} else {
			m = append(m, g.Role)
		}
	}
	return strings.Join(m, ", ")
}

// parseWhere turns COLUMN<op>VALUE into a row filter.
func parseWhere(s string) (map[string]string, error) {
	if col, ok := strings.CutSuffix(s, ":null"); ok {
		return map[string]string{"column": col, "op": "null"}, nil
	}
	if col, ok := strings.CutSuffix(s, ":notnull"); ok {
		return map[string]string{"column": col, "op": "notnull"}, nil
	}
	for _, o := range []struct{ tok, op string }{{"~*", "ilike"}, {"<>", "<>"}, {"<=", "<="}, {">=", ">="}, {"~", "like"}, {"=", "="}, {"<", "<"}, {">", ">"}} {
		if col, val, ok := strings.Cut(s, o.tok); ok && col != "" {
			return map[string]string{"column": col, "op": o.op, "value": val}, nil
		}
	}
	return nil, fmt.Errorf("--where %q: use COLUMN=VALUE (or <> < <= > >= ~ ~* :null :notnull)", s)
}
