package dbs

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"syncloud/internal/store"
)

// PgObject is one object in a schema.
type PgObject struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // table, partitioned, view, matview, foreign, sequence, function, procedure, aggregate, type
	OID         uint32 `json:"oid"`
	Owner       string `json:"owner"`
	RowEstimate int64  `json:"rowEstimate,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	Partitions  int    `json:"partitions,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

// PgSchema is a schema and its objects.
type PgSchema struct {
	Name    string     `json:"name"`
	Owner   string     `json:"owner"`
	System  bool       `json:"system"` // catalogs and extension-internal schemas
	Comment string     `json:"comment,omitempty"`
	Objects []PgObject `json:"objects"`
}

var relKinds = map[string]string{"r": "table", "p": "partitioned", "v": "view", "m": "matview", "f": "foreign", "S": "sequence"}
var procKinds = map[string]string{"f": "function", "p": "procedure", "a": "aggregate", "w": "function"}

// systemSchema says whether a schema belongs to the catalogs or an extension.
const systemSchemaSQL = `(n.nspname IN ('pg_catalog', 'information_schema') OR n.nspname LIKE 'pg\_%'
   OR n.nspname LIKE '\_timescaledb%' OR n.nspname IN ('timescaledb_information', 'timescaledb_experimental', 'cron', 'duckdb', 'partman')
   OR EXISTS (SELECT 1 FROM pg_depend dep WHERE dep.classid = 'pg_namespace'::regclass AND dep.objid = n.oid AND dep.deptype = 'e'))`

// notExtension filters out objects an extension installed.
const notExtension = `NOT EXISTS (SELECT 1 FROM pg_depend dep WHERE dep.objid = %s AND dep.deptype = 'e')`

// PgSchemaTree lists a database's schemas and their objects.
func (m *Manager) PgSchemaTree(ctx context.Context, d store.Database, db string, system bool) ([]PgSchema, error) {
	var out []PgSchema
	err := m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
SELECT n.oid, n.nspname, pg_get_userbyid(n.nspowner), `+systemSchemaSQL+`, coalesce(obj_description(n.oid, 'pg_namespace'), '')
FROM pg_namespace n WHERE n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'
ORDER BY 4, n.nspname = 'public' DESC, n.nspname`)
		if err != nil {
			return err
		}
		idx := map[uint32]int{}
		for rows.Next() {
			var oid uint32
			var s PgSchema
			if err := rows.Scan(&oid, &s.Name, &s.Owner, &s.System, &s.Comment); err != nil {
				return err
			}
			if s.System && !system {
				continue
			}
			s.Objects = []PgObject{}
			idx[oid] = len(out)
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		add := func(ns uint32, o PgObject) {
			if i, ok := idx[ns]; ok {
				out[i].Objects = append(out[i].Objects, o)
			}
		}
		rows, err = c.Query(ctx, `
SELECT c.relnamespace, c.oid, c.relname, c.relkind::text, pg_get_userbyid(c.relowner),
       CASE WHEN c.relkind IN ('r','m','p') THEN greatest(c.reltuples, 0)::bigint ELSE 0 END,
       CASE WHEN c.relkind IN ('r','m','S') THEN pg_total_relation_size(c.oid)
            WHEN c.relkind = 'p' THEN coalesce((SELECT sum(pg_total_relation_size(i.inhrelid)) FROM pg_inherits i WHERE i.inhparent = c.oid), 0)::bigint
            ELSE 0 END,
       (SELECT count(*) FROM pg_inherits i WHERE i.inhparent = c.oid)::int,
       coalesce(obj_description(c.oid, 'pg_class'), '')
FROM pg_class c
WHERE c.relkind IN ('r','p','v','m','f','S') AND NOT c.relispartition AND `+fmt.Sprintf(notExtension, "c.oid")+`
ORDER BY c.relname`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ns uint32
			var o PgObject
			var kind string
			if err := rows.Scan(&ns, &o.OID, &o.Name, &kind, &o.Owner, &o.RowEstimate, &o.SizeBytes, &o.Partitions, &o.Comment); err != nil {
				return err
			}
			o.Kind = relKinds[kind]
			add(ns, o)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = c.Query(ctx, `
SELECT p.pronamespace, p.oid, p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', p.prokind::text,
       pg_get_userbyid(p.proowner), coalesce(obj_description(p.oid, 'pg_proc'), '')
FROM pg_proc p WHERE `+fmt.Sprintf(notExtension, "p.oid")+` ORDER BY p.proname`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ns uint32
			var o PgObject
			var kind string
			if err := rows.Scan(&ns, &o.OID, &o.Name, &kind, &o.Owner, &o.Comment); err != nil {
				return err
			}
			o.Kind = procKinds[kind]
			add(ns, o)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Enums, domains, composite and range types (not a table's row type).
		rows, err = c.Query(ctx, `
SELECT t.typnamespace, t.oid, t.typname, pg_get_userbyid(t.typowner), coalesce(obj_description(t.oid, 'pg_type'), '')
FROM pg_type t
WHERE t.typtype IN ('e','d','r','c') AND (t.typrelid = 0 OR (SELECT relkind FROM pg_class WHERE oid = t.typrelid) = 'c')
  AND NOT EXISTS (SELECT 1 FROM pg_type el WHERE el.oid = t.typelem AND el.typarray = t.oid)
  AND `+fmt.Sprintf(notExtension, "t.oid")+` ORDER BY t.typname`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ns uint32
			o := PgObject{Kind: "type"}
			if err := rows.Scan(&ns, &o.OID, &o.Name, &o.Owner, &o.Comment); err != nil {
				return err
			}
			add(ns, o)
		}
		return rows.Err()
	})
	return out, err
}

// PgColumn is a table column.
type PgColumn struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	NotNull   bool   `json:"notNull"`
	Default   string `json:"default,omitempty"`
	Identity  string `json:"identity,omitempty"`  // "always" or "by default"
	Generated string `json:"generated,omitempty"` // the expression of a stored generated column
	Comment   string `json:"comment,omitempty"`
}

// PgConstraint is a table constraint.
type PgConstraint struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // primary key, foreign key, unique, check, exclusion, not null
	Definition string `json:"definition"`
	References string `json:"references,omitempty"` // foreign keys: the referenced table
}

// PgIndex is an index on a table.
type PgIndex struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Primary    bool   `json:"primary"`
	Unique     bool   `json:"unique"`
	Valid      bool   `json:"valid"`
	SizeBytes  int64  `json:"sizeBytes"`
	Scans      int64  `json:"scans"`
}

// PgTrigger is a trigger on a table.
type PgTrigger struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Enabled    bool   `json:"enabled"`
}

// PgObjectDetail describes one object: a relation's structure and
// statistics, or a routine's or type's definition.
type PgObjectDetail struct {
	PgObject
	Schema         string         `json:"schema"`
	TableBytes     int64          `json:"tableBytes,omitempty"`
	IndexBytes     int64          `json:"indexBytes,omitempty"`
	ToastBytes     int64          `json:"toastBytes,omitempty"`
	LiveRows       int64          `json:"liveRows,omitempty"`
	DeadRows       int64          `json:"deadRows,omitempty"`
	SeqScans       int64          `json:"seqScans,omitempty"`
	IndexScans     int64          `json:"indexScans,omitempty"`
	LastVacuum     *time.Time     `json:"lastVacuum,omitempty"`
	LastAutovacuum *time.Time     `json:"lastAutovacuum,omitempty"`
	LastAnalyze    *time.Time     `json:"lastAnalyze,omitempty"`
	Columns        []PgColumn     `json:"columns"`
	Constraints    []PgConstraint `json:"constraints"`
	Indexes        []PgIndex      `json:"indexes"`
	Triggers       []PgTrigger    `json:"triggers"`
	PartitionOf    string         `json:"partitionOf,omitempty"`
	PartitionKey   string         `json:"partitionKey,omitempty"`
	PartitionList  []string       `json:"partitionList,omitempty"`
	DDL            string         `json:"ddl"`
}

// PgObjectByName describes a relation or type in schema by name, or a
// routine by its OID (oid > 0).
func (m *Manager) PgObjectByName(ctx context.Context, d store.Database, db, schema, name string, oid uint32) (PgObjectDetail, error) {
	var out PgObjectDetail
	err := m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
		if oid > 0 {
			return routineDetail(ctx, c, oid, &out)
		}
		var kind string
		err := c.QueryRow(ctx, `
SELECT c.oid, c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','v','m','f','S')`, schema, name).Scan(&out.OID, &kind)
		if err == pgx.ErrNoRows {
			var toid uint32
			if c.QueryRow(ctx, `SELECT t.oid FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE n.nspname = $1 AND t.typname = $2`, schema, name).Scan(&toid) == nil {
				return typeDetail(ctx, c, toid, &out)
			}
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		out.Kind = relKinds[kind]
		return relationDetail(ctx, c, &out)
	})
	return out, err
}

func relationDetail(ctx context.Context, c *pgx.Conn, o *PgObjectDetail) error {
	oid := o.OID
	err := c.QueryRow(ctx, `
SELECT n.nspname, c.relname, pg_get_userbyid(c.relowner), greatest(c.reltuples, 0)::bigint, coalesce(obj_description(c.oid, 'pg_class'), ''),
       pg_total_relation_size(c.oid), pg_relation_size(c.oid), pg_indexes_size(c.oid),
       coalesce(pg_total_relation_size(nullif(c.reltoastrelid, 0)), 0),
       coalesce(s.n_live_tup, 0), coalesce(s.n_dead_tup, 0), coalesce(s.seq_scan, 0), coalesce(s.idx_scan, 0),
       s.last_vacuum, s.last_autovacuum, greatest(s.last_analyze, s.last_autoanalyze),
       coalesce((SELECT i.inhparent::regclass::text FROM pg_inherits i WHERE i.inhrelid = c.oid AND c.relispartition), ''),
       coalesce(pg_get_partkeydef(c.oid), ''),
       coalesce((SELECT array_agg(i.inhrelid::regclass::text || ' ' || pg_get_expr(p.relpartbound, p.oid) ORDER BY 1)
                 FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhrelid WHERE i.inhparent = c.oid), '{}')
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace LEFT JOIN pg_stat_all_tables s ON s.relid = c.oid
WHERE c.oid = $1`, oid).Scan(&o.Schema, &o.Name, &o.Owner, &o.RowEstimate, &o.Comment,
		&o.SizeBytes, &o.TableBytes, &o.IndexBytes, &o.ToastBytes,
		&o.LiveRows, &o.DeadRows, &o.SeqScans, &o.IndexScans,
		&o.LastVacuum, &o.LastAutovacuum, &o.LastAnalyze, &o.PartitionOf, &o.PartitionKey, &o.PartitionList)
	if err != nil {
		return err
	}
	o.Columns, o.Constraints, o.Indexes, o.Triggers = []PgColumn{}, []PgConstraint{}, []PgIndex{}, []PgTrigger{}
	rows, err := c.Query(ctx, `
SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
       CASE WHEN a.attgenerated = '' THEN coalesce(pg_get_expr(ad.adbin, ad.adrelid), '') ELSE '' END,
       CASE a.attidentity WHEN 'a' THEN 'always' WHEN 'd' THEN 'by default' ELSE '' END,
       CASE WHEN a.attgenerated <> '' THEN coalesce(pg_get_expr(ad.adbin, ad.adrelid), '') ELSE '' END,
       coalesce(col_description(a.attrelid, a.attnum), '')
FROM pg_attribute a LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var col PgColumn
		if err := rows.Scan(&col.Name, &col.Type, &col.NotNull, &col.Default, &col.Identity, &col.Generated, &col.Comment); err != nil {
			return err
		}
		o.Columns = append(o.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	conKinds := map[string]string{"p": "primary key", "f": "foreign key", "u": "unique", "c": "check", "x": "exclusion", "n": "not null"}
	rows, err = c.Query(ctx, `
SELECT conname, contype::text, pg_get_constraintdef(oid, true), CASE WHEN confrelid <> 0 THEN confrelid::regclass::text ELSE '' END
FROM pg_constraint WHERE conrelid = $1 AND contype <> 'n' ORDER BY contype = 'p' DESC, contype, conname`, oid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k PgConstraint
		var kind string
		if err := rows.Scan(&k.Name, &kind, &k.Definition, &k.References); err != nil {
			return err
		}
		k.Kind = conKinds[kind]
		o.Constraints = append(o.Constraints, k)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = c.Query(ctx, `
SELECT ic.relname, pg_get_indexdef(i.indexrelid), i.indisprimary, i.indisunique, i.indisvalid,
       pg_relation_size(i.indexrelid), coalesce(s.idx_scan, 0)
FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid LEFT JOIN pg_stat_all_indexes s ON s.indexrelid = i.indexrelid
WHERE i.indrelid = $1 ORDER BY i.indisprimary DESC, ic.relname`, oid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var x PgIndex
		if err := rows.Scan(&x.Name, &x.Definition, &x.Primary, &x.Unique, &x.Valid, &x.SizeBytes, &x.Scans); err != nil {
			return err
		}
		o.Indexes = append(o.Indexes, x)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = c.Query(ctx, `SELECT tgname, pg_get_triggerdef(oid, true), tgenabled <> 'D' FROM pg_trigger WHERE tgrelid = $1 AND NOT tgisinternal ORDER BY tgname`, oid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t PgTrigger
		if err := rows.Scan(&t.Name, &t.Definition, &t.Enabled); err != nil {
			return err
		}
		o.Triggers = append(o.Triggers, t)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return relationDDL(ctx, c, o)
}

// relationDDL reconstructs the object's CREATE statement.
func relationDDL(ctx context.Context, c *pgx.Conn, o *PgObjectDetail) error {
	full := qi(o.Schema) + "." + qi(o.Name)
	var b strings.Builder
	switch o.Kind {
	case "view", "matview":
		var def string
		if err := c.QueryRow(ctx, `SELECT pg_get_viewdef($1::oid, true)`, o.OID).Scan(&def); err != nil {
			return err
		}
		kw := "VIEW"
		if o.Kind == "matview" {
			kw = "MATERIALIZED VIEW"
		}
		fmt.Fprintf(&b, "CREATE %s %s AS\n%s\n", kw, full, strings.TrimRight(def, "\n"))
	case "sequence":
		var typ string
		var start, inc, minv, maxv, cache int64
		var cycle bool
		err := c.QueryRow(ctx, `SELECT format_type(seqtypid, NULL), seqstart, seqincrement, seqmin, seqmax, seqcache, seqcycle FROM pg_sequence WHERE seqrelid = $1`, o.OID).
			Scan(&typ, &start, &inc, &minv, &maxv, &cache, &cycle)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "CREATE SEQUENCE %s AS %s\n  INCREMENT BY %d MINVALUE %d MAXVALUE %d START WITH %d CACHE %d%s;\n",
			full, typ, inc, minv, maxv, start, cache, map[bool]string{true: " CYCLE", false: ""}[cycle])
	default:
		kw := "TABLE"
		if o.Kind == "foreign" {
			kw = "FOREIGN TABLE"
		}
		fmt.Fprintf(&b, "CREATE %s %s (\n", kw, full)
		var lines []string
		for _, col := range o.Columns {
			l := "  " + qi(col.Name) + " " + col.Type
			switch {
			case col.Identity != "":
				l += " GENERATED " + strings.ToUpper(col.Identity) + " AS IDENTITY"
			case col.Generated != "":
				l += " GENERATED ALWAYS AS (" + col.Generated + ") STORED"
			case col.Default != "":
				l += " DEFAULT " + col.Default
			}
			if col.NotNull {
				l += " NOT NULL"
			}
			lines = append(lines, l)
		}
		for _, k := range o.Constraints {
			lines = append(lines, "  CONSTRAINT "+qi(k.Name)+" "+k.Definition)
		}
		b.WriteString(strings.Join(lines, ",\n"))
		b.WriteString("\n)")
		if o.PartitionKey != "" {
			b.WriteString(" PARTITION BY " + o.PartitionKey)
		}
		b.WriteString(";\n")
		for _, x := range o.Indexes {
			if !slices.ContainsFunc(o.Constraints, func(k PgConstraint) bool { return k.Name == x.Name }) {
				b.WriteString(x.Definition + ";\n")
			}
		}
		for _, t := range o.Triggers {
			b.WriteString(t.Definition + ";\n")
		}
	}
	if o.Comment != "" {
		kw := map[string]string{"view": "VIEW", "matview": "MATERIALIZED VIEW", "sequence": "SEQUENCE", "foreign": "FOREIGN TABLE"}[o.Kind]
		if kw == "" {
			kw = "TABLE"
		}
		fmt.Fprintf(&b, "COMMENT ON %s %s IS %s;\n", kw, full, quoteLiteral(o.Comment))
	}
	for _, col := range o.Columns {
		if col.Comment != "" {
			fmt.Fprintf(&b, "COMMENT ON COLUMN %s.%s IS %s;\n", full, qi(col.Name), quoteLiteral(col.Comment))
		}
	}
	o.DDL = b.String()
	return nil
}

func routineDetail(ctx context.Context, c *pgx.Conn, oid uint32, o *PgObjectDetail) error {
	var kind string
	err := c.QueryRow(ctx, `
SELECT p.oid, n.nspname, p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', p.prokind::text, pg_get_userbyid(p.proowner),
       coalesce(obj_description(p.oid, 'pg_proc'), ''),
       CASE WHEN p.prokind = 'a' THEN '-- aggregate ' || p.oid::regprocedure::text ELSE pg_get_functiondef(p.oid) END
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE p.oid = $1`, oid).
		Scan(&o.OID, &o.Schema, &o.Name, &kind, &o.Owner, &o.Comment, &o.DDL)
	if err == pgx.ErrNoRows {
		return store.ErrNotFound
	}
	o.Kind = procKinds[kind]
	o.Columns, o.Constraints, o.Indexes, o.Triggers = []PgColumn{}, []PgConstraint{}, []PgIndex{}, []PgTrigger{}
	return err
}

func typeDetail(ctx context.Context, c *pgx.Conn, oid uint32, o *PgObjectDetail) error {
	var typ, base string
	var labels []string
	err := c.QueryRow(ctx, `
SELECT t.oid, n.nspname, t.typname, pg_get_userbyid(t.typowner), coalesce(obj_description(t.oid, 'pg_type'), ''), t.typtype::text,
       CASE WHEN t.typtype = 'd' THEN format_type(t.typbasetype, t.typtypmod) ELSE '' END,
       coalesce((SELECT array_agg(enumlabel ORDER BY enumsortorder) FROM pg_enum WHERE enumtypid = t.oid), '{}')
FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE t.oid = $1`, oid).
		Scan(&o.OID, &o.Schema, &o.Name, &o.Owner, &o.Comment, &typ, &base, &labels)
	if err != nil {
		return err
	}
	o.Kind = "type"
	o.Columns, o.Constraints, o.Indexes, o.Triggers = []PgColumn{}, []PgConstraint{}, []PgIndex{}, []PgTrigger{}
	full := qi(o.Schema) + "." + qi(o.Name)
	switch typ {
	case "e":
		q := make([]string, len(labels))
		for i, l := range labels {
			q[i] = quoteLiteral(l)
		}
		o.DDL = fmt.Sprintf("CREATE TYPE %s AS ENUM (%s);\n", full, strings.Join(q, ", "))
	case "d":
		var checks []string
		rows, err := c.Query(ctx, `SELECT pg_get_constraintdef(oid, true) FROM pg_constraint WHERE contypid = $1`, oid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			checks = append(checks, " "+s)
		}
		o.DDL = fmt.Sprintf("CREATE DOMAIN %s AS %s%s;\n", full, base, strings.Join(checks, ""))
	default:
		rows, err := c.Query(ctx, `
SELECT a.attname, format_type(a.atttypid, a.atttypmod) FROM pg_attribute a JOIN pg_type t ON t.typrelid = a.attrelid
WHERE t.oid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
		if err != nil {
			return err
		}
		var cols []string
		for rows.Next() {
			var col PgColumn
			if err := rows.Scan(&col.Name, &col.Type); err != nil {
				return err
			}
			o.Columns = append(o.Columns, col)
			cols = append(cols, "  "+qi(col.Name)+" "+col.Type)
		}
		if typ == "r" {
			o.DDL = fmt.Sprintf("-- range type %s\n", full)
		} else {
			o.DDL = fmt.Sprintf("CREATE TYPE %s AS (\n%s\n);\n", full, strings.Join(cols, ",\n"))
		}
	}
	return nil
}

// PgRowFilter narrows a data page.
type PgRowFilter struct {
	Column string `json:"column"`
	Op     string `json:"op"` // = <> < <= > >= like ilike null notnull
	Value  string `json:"value"`
}

// PgRowsQuery asks for a page of a relation's rows.
type PgRowsQuery struct {
	Schema  string        `json:"schema"`
	Table   string        `json:"table"`
	Limit   int           `json:"limit"`
	Offset  int           `json:"offset"`
	OrderBy string        `json:"orderBy"`
	Desc    bool          `json:"desc"`
	Filters []PgRowFilter `json:"filters"`
	Count   bool          `json:"count"` // also count the matching rows
}

// PgColumnMeta names a result column.
type PgColumnMeta struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// PgRows is a page of rows, every value in PostgreSQL's text form.
type PgRows struct {
	Columns []PgColumnMeta `json:"columns"`
	Rows    [][]*string    `json:"rows"`
	More    bool           `json:"more"`
	Total   *int64         `json:"total,omitempty"`
}

// maxCell caps one value's text in data pages.
const maxCell = 10000

// PgTableRows reads a page of a table, view or materialized view.
func (m *Manager) PgTableRows(ctx context.Context, d store.Database, db string, q PgRowsQuery) (PgRows, error) {
	out := PgRows{Rows: [][]*string{}}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	q.Limit = min(q.Limit, 1000)
	q.Offset = max(q.Offset, 0)
	err := m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
SELECT a.attname, format_type(a.atttypid, a.atttypmod)
FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','v','m','f') AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, q.Schema, q.Table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var col PgColumnMeta
			if err := rows.Scan(&col.Name, &col.Type); err != nil {
				return err
			}
			out.Columns = append(out.Columns, col)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Columns) == 0 {
			return store.ErrNotFound
		}
		typeOf := map[string]string{}
		for _, col := range out.Columns {
			typeOf[col.Name] = col.Type
		}
		has := func(name string) bool { _, ok := typeOf[name]; return ok }
		var where []string
		var args []any
		for _, f := range q.Filters {
			if !has(f.Column) {
				return invalidf("no column %q", f.Column)
			}
			col := qi(f.Column)
			switch f.Op {
			case "null":
				where = append(where, col+" IS NULL")
			case "notnull":
				where = append(where, col+" IS NOT NULL")
			case "=", "<>", "<", "<=", ">", ">=":
				// The value is cast to the column's type (format_type output,
				// from the catalog), so numbers and times compare as such.
				args = append(args, f.Value)
				where = append(where, fmt.Sprintf("%s %s $%d::%s", col, f.Op, len(args), typeOf[f.Column]))
			case "like", "ilike":
				args = append(args, f.Value)
				where = append(where, fmt.Sprintf("%s::text %s $%d", col, strings.ToUpper(f.Op), len(args)))
			default:
				return invalidf("filter operator %q: use = <> < <= > >= like ilike null notnull", f.Op)
			}
		}
		from := " FROM " + qi(q.Schema) + "." + qi(q.Table)
		if len(where) > 0 {
			from += " WHERE " + strings.Join(where, " AND ")
		}
		sel := make([]string, len(out.Columns))
		for i, col := range out.Columns {
			sel[i] = "left(" + qi(col.Name) + "::text, " + strconv.Itoa(maxCell) + ")"
		}
		sql := "SELECT " + strings.Join(sel, ", ") + from
		if q.OrderBy != "" {
			if !has(q.OrderBy) {
				return invalidf("no column %q", q.OrderBy)
			}
			sql += " ORDER BY " + qi(q.OrderBy) + map[bool]string{true: " DESC", false: ""}[q.Desc]
		}
		sql += fmt.Sprintf(" LIMIT %d OFFSET %d", q.Limit+1, q.Offset)
		tx, err := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		rows, err = tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			vals := make([]*string, len(out.Columns))
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			out.Rows = append(out.Rows, vals)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Rows) > q.Limit {
			out.Rows, out.More = out.Rows[:q.Limit], true
		}
		if q.Count {
			var n int64
			if err := tx.QueryRow(ctx, "SELECT count(*)"+from, args...).Scan(&n); err != nil {
				return err
			}
			out.Total = &n
		}
		return nil
	})
	return out, err
}
