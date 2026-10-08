package dbs

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// The SQL console signs in as the app user, never the superuser, so
// nothing typed into it (RESET ROLE included) can reach platform rights.
// "Run as" uses SET ROLE to a role app may become (CreatePgRole grants
// that). Read-only runs put every statement in its own BEGIN READ ONLY
// transaction over the extended protocol, which takes one statement at a
// time, so a COMMIT in the text cannot end the read-only transaction early.

const (
	maxQueryRows       = 1000
	maxQueryStatements = 50
	maxQueryBytes      = 1 << 20
)

// PgQuery is a console run.
type PgQuery struct {
	Database string `json:"database"` // "" = the cluster's own
	SQL      string `json:"sql"`
	Role     string `json:"role"` // "" = app
	MaxRows  int    `json:"maxRows"`
}

// PgStatementResult is one statement's outcome.
type PgStatementResult struct {
	Statement    string         `json:"statement"`
	Columns      []PgColumnMeta `json:"columns"`
	Rows         [][]*string    `json:"rows"`
	Truncated    bool           `json:"truncated"`
	Tag          string         `json:"tag"`
	RowsAffected int64          `json:"rowsAffected"`
	DurationMs   float64        `json:"durationMs"`
}

// PgQueryError is where and why a run stopped.
type PgQueryError struct {
	Statement int    `json:"statement"` // index into the statements
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Hint      string `json:"hint,omitempty"`
	Position  int    `json:"position,omitempty"` // 1-based, in the whole text
}

// PgQueryResult is a console run's results.
type PgQueryResult struct {
	Results    []PgStatementResult `json:"results"`
	Error      *PgQueryError       `json:"error,omitempty"`
	RolledBack bool                `json:"rolledBack,omitempty"` // a transaction left open was rolled back
	ReadOnly   bool                `json:"readOnly"`
	Role       string              `json:"role"`
	Database   string              `json:"database"`
}

var copyStdio = regexp.MustCompile(`(?is)^\s*copy\b.*\b(stdin|stdout)\b`)

// RunPgQuery runs SQL in the console.
func (m *Manager) RunPgQuery(ctx context.Context, d store.Database, q PgQuery, write bool) (PgQueryResult, error) {
	out := PgQueryResult{Results: []PgStatementResult{}, ReadOnly: !write, Role: q.Role, Database: q.Database}
	if out.Role == "" {
		out.Role = pgAppUser
	}
	if out.Database == "" {
		out.Database = PgDatabase(d)
	}
	if len(q.SQL) > maxQueryBytes {
		return out, invalidf("the SQL is over 1 MiB")
	}
	if q.MaxRows <= 0 || q.MaxRows > maxQueryRows {
		q.MaxRows = maxQueryRows
	}
	stmts := splitSQL(q.SQL)
	if len(stmts) == 0 {
		return out, invalidf("no SQL to run")
	}
	if len(stmts) > maxQueryStatements {
		return out, invalidf("at most %d statements per run", maxQueryStatements)
	}
	if out.Role != pgAppUser {
		if err := checkIdent("role", out.Role); err != nil {
			return out, err
		}
		if isProtectedRole(out.Role) {
			return out, invalidf("the console cannot run as %q", out.Role)
		}
	}
	err := m.pgWith(ctx, d, q.Database, pgAppUser, func(c *pgx.Conn) error {
		pc := c.PgConn()
		if out.Role != pgAppUser {
			if err := pc.Exec(ctx, "SET ROLE "+qi(out.Role)).Close(); err != nil {
				return invalidf("run as %s: %s", out.Role, pgMessage(err))
			}
		}
		oids := map[uint32]string{}
		for i, s := range stmts {
			if copyStdio.MatchString(s.text) {
				out.Error = &PgQueryError{Statement: i, Message: "COPY to STDOUT or from STDIN needs a client; use COPY to a file on S3 or psql"}
				break
			}
			if !write {
				if err := pc.Exec(ctx, "BEGIN READ ONLY").Close(); err != nil {
					return err
				}
			}
			res, err := runStatement(ctx, pc, s.text, q.MaxRows)
			res.Statement = s.text
			if !write {
				_ = pc.Exec(ctx, "ROLLBACK").Close()
			}
			if err != nil {
				out.Error = queryError(i, s, err)
				if ctx.Err() != nil {
					return nil
				}
				break
			}
			for _, col := range res.Columns {
				oids[parseOID(col.Type)] = ""
			}
			out.Results = append(out.Results, res)
		}
		if pc.TxStatus() != 'I' {
			_ = pc.Exec(ctx, "ROLLBACK").Close()
			out.RolledBack = write
		}
		typeNames(ctx, c, oids)
		for i := range out.Results {
			for j, col := range out.Results[i].Columns {
				if n := oids[parseOID(col.Type)]; n != "" {
					out.Results[i].Columns[j].Type = n
				}
			}
		}
		return nil
	})
	return out, err
}

func runStatement(ctx context.Context, pc *pgconn.PgConn, sql string, maxRows int) (PgStatementResult, error) {
	res := PgStatementResult{Columns: []PgColumnMeta{}, Rows: [][]*string{}}
	start := time.Now()
	rr := pc.ExecParams(ctx, sql, nil, nil, nil, nil) // text results
	for _, fd := range rr.FieldDescriptions() {
		res.Columns = append(res.Columns, PgColumnMeta{Name: fd.Name, Type: "oid:" + strconv.Itoa(int(fd.DataTypeOID))})
	}
	for rr.NextRow() {
		if len(res.Rows) >= maxRows {
			res.Truncated = true
			continue
		}
		vals := rr.Values()
		row := make([]*string, len(vals))
		for i, v := range vals {
			if v != nil {
				s := string(v)
				if len(s) > maxCell {
					s = s[:maxCell] + "…"
				}
				row[i] = &s
			}
		}
		res.Rows = append(res.Rows, row)
	}
	tag, err := rr.Close()
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	res.Tag = tag.String()
	res.RowsAffected = tag.RowsAffected()
	return res, err
}

func parseOID(t string) uint32 {
	var n uint32
	for _, ch := range strings.TrimPrefix(t, "oid:") {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + uint32(ch-'0')
	}
	return n
}

// typeNames fills in the type name of each OID.
func typeNames(ctx context.Context, c *pgx.Conn, oids map[uint32]string) {
	var list []uint32
	for oid := range oids {
		if t, ok := c.TypeMap().TypeForOID(oid); ok {
			oids[oid] = t.Name
		} else {
			list = append(list, oid)
		}
	}
	if len(list) == 0 {
		return
	}
	rows, err := c.Query(ctx, `SELECT oid, format_type(oid, NULL) FROM pg_type WHERE oid = ANY($1)`, list)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var oid uint32
		var name string
		if rows.Scan(&oid, &name) == nil {
			oids[oid] = name
		}
	}
}

func pgMessage(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Message
	}
	return err.Error()
}

func queryError(i int, s sqlStatement, err error) *PgQueryError {
	qe := &PgQueryError{Statement: i, Message: err.Error()}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		qe.Message, qe.Code, qe.Detail, qe.Hint = pe.Message, pe.Code, pe.Detail, pe.Hint
		if pe.Position > 0 {
			qe.Position = s.offset + int(pe.Position)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		qe.Message = "the run took too long and was cancelled"
	}
	return qe
}

type sqlStatement struct {
	text   string
	offset int // bytes before it in the whole text
}

// splitSQL splits text into statements at top-level semicolons, minding
// quotes, dollar quotes and comments.
func splitSQL(sql string) []sqlStatement {
	var out []sqlStatement
	start := 0
	emit := func(end int) {
		raw := sql[start:end]
		trimmed := strings.TrimLeft(raw, " \t\r\n")
		lead := len(raw) - len(trimmed)
		trimmed = strings.TrimRight(trimmed, " \t\r\n")
		if onlyComments(trimmed) {
			return
		}
		out = append(out, sqlStatement{text: trimmed, offset: start + lead})
	}
	n := len(sql)
	for i := 0; i < n; i++ {
		ch := sql[i]
		switch {
		case ch == '\'' || ch == '"':
			// E'' strings use backslash escapes; others double the quote.
			esc := ch == '\'' && i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i < 2 || !isIdentChar(sql[i-2]))
			for i++; i < n; i++ {
				if esc && sql[i] == '\\' {
					i++
					continue
				}
				if sql[i] == ch {
					if i+1 < n && sql[i+1] == ch {
						i++
						continue
					}
					break
				}
			}
		case ch == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case ch == '/' && i+1 < n && sql[i+1] == '*':
			depth := 0
			for ; i+1 < n; i++ {
				if sql[i] == '/' && sql[i+1] == '*' {
					depth++
					i++
				} else if sql[i] == '*' && sql[i+1] == '/' {
					depth--
					i++
					if depth == 0 {
						break
					}
				}
			}
		case ch == '$' && (i == 0 || !isIdentChar(sql[i-1])):
			j := i + 1
			for j < n && (isIdentChar(sql[j]) && !(sql[j] >= '0' && sql[j] <= '9' && j == i+1)) {
				j++
			}
			if j < n && sql[j] == '$' {
				tag := sql[i : j+1]
				if k := strings.Index(sql[j+1:], tag); k >= 0 {
					i = j + 1 + k + len(tag) - 1
				} else {
					i = n
				}
			}
		case ch == ';':
			emit(i)
			start = i + 1
		}
	}
	if start < n {
		emit(n)
	}
	return out
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c >= 0x80
}

func onlyComments(s string) bool {
	for s != "" {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case s == "":
			return true
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
			} else {
				return true
			}
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
			} else {
				return true
			}
		default:
			return false
		}
	}
	return true
}

// ── sessions ────────────────────────────────────────────────────────────────

// PgSession is a client connection.
type PgSession struct {
	PID          int        `json:"pid"`
	User         string     `json:"user"`
	Database     string     `json:"database"`
	Client       string     `json:"client"`
	Application  string     `json:"application"`
	State        string     `json:"state"`
	WaitEvent    string     `json:"waitEvent"`
	Query        string     `json:"query"`
	BackendStart *time.Time `json:"backendStart"`
	XactStart    *time.Time `json:"xactStart"`
	QueryStart   *time.Time `json:"queryStart"`
	BlockedBy    []int      `json:"blockedBy"`
	Platform     bool       `json:"platform"` // Patroni, replication and the explorer
}

const sessionQuery = `
SELECT pid, coalesce(usename, ''), coalesce(datname, ''), coalesce(host(client_addr), 'local'), application_name, coalesce(state, ''),
       coalesce(wait_event_type || ': ' || wait_event, ''), left(query, 4000), backend_start, xact_start, query_start,
       coalesce(pg_blocking_pids(pid), '{}')
FROM pg_stat_activity WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()`

// PgSessions lists client sessions (platform ones only when all is set).
func (m *Manager) PgSessions(ctx context.Context, d store.Database, all bool) ([]PgSession, error) {
	out := []PgSession{}
	err := m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, sessionQuery+` ORDER BY state = 'active' DESC, xact_start NULLS LAST, pid`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s PgSession
			var blocked []int32
			if err := rows.Scan(&s.PID, &s.User, &s.Database, &s.Client, &s.Application, &s.State, &s.WaitEvent, &s.Query,
				&s.BackendStart, &s.XactStart, &s.QueryStart, &blocked); err != nil {
				return err
			}
			for _, b := range blocked {
				s.BlockedBy = append(s.BlockedBy, int(b))
			}
			s.Platform = slices.Contains(protectedRoles, s.User) || s.Application == explorerApp
			if s.Platform && !all {
				continue
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// SignalPgSession cancels a session's query, or terminates the session.
func (m *Manager) SignalPgSession(ctx context.Context, d store.Database, pid int, terminate bool) error {
	return m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		var user, app string
		err := c.QueryRow(ctx, `SELECT coalesce(usename, ''), application_name FROM pg_stat_activity WHERE pid = $1 AND backend_type = 'client backend'`, pid).Scan(&user, &app)
		if err == pgx.ErrNoRows {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if slices.Contains(protectedRoles, user) {
			return invalidf("session %d belongs to the platform", pid)
		}
		fn := "pg_cancel_backend"
		if terminate {
			fn = "pg_terminate_backend"
		}
		_, err = c.Exec(ctx, `SELECT `+fn+`($1)`, pid)
		return err
	})
}
