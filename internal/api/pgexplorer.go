package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ridoysheikh/syncloud/internal/dbs"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// PostgreSQL explorer and administration (§13 "Explorer and administration").

// postgres resolves a PostgreSQL database from the path.
func (s *Server) postgres(w http.ResponseWriter, r *http.Request) (store.Database, bool) {
	d, ok := s.database(w, r)
	if ok && d.Engine != dbs.EnginePostgres {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "this tool is for PostgreSQL databases; "+d.Name+" runs "+d.Engine)
		return d, false
	}
	return d, ok
}

// pgErr reports explorer errors; PostgreSQL's own errors (a missing
// table, a failed grant) go back as 400 with its message and SQLSTATE.
func (s *Server) pgErr(w http.ResponseWriter, what string, err error) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		msg := pe.Message
		if pe.Detail != "" {
			msg += " (" + pe.Detail + ")"
		}
		writeError(w, http.StatusBadRequest, CodeBadRequest, msg+" [SQLSTATE "+pe.Code+"]")
		return
	}
	s.databaseErr(w, what, err)
}

func (s *Server) pgAudit(r *http.Request, action string, d store.Database, detail map[string]any) {
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, action, dbSRN(d), detail)
}

// ── databases ───────────────────────────────────────────────────────────────

func (s *Server) handleListPgDatabases(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	out, err := s.databases.PgDatabases(r.Context(), d)
	if err != nil {
		s.pgErr(w, "list databases", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleCreatePgDatabase(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	var in dbs.PgDatabaseInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.databases.CreatePgDatabase(r.Context(), d, in); err != nil {
		s.pgErr(w, "create database", err)
		return
	}
	s.pgAudit(r, "database:CreatePgDatabase", d, map[string]any{"database": in.Name, "owner": in.Owner})
	writeJSON(w, http.StatusCreated, map[string]any{"name": in.Name})
}

func (s *Server) handleAlterPgDatabase(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	var in dbs.PgDatabaseInput
	if !decodeJSON(w, r, &in) {
		return
	}
	name := r.PathValue("db")
	if err := s.databases.AlterPgDatabase(r.Context(), d, name, in); err != nil {
		s.pgErr(w, "alter database", err)
		return
	}
	s.pgAudit(r, "database:AlterPgDatabase", d, map[string]any{"database": name, "rename": in.Name, "owner": in.Owner})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDropPgDatabase(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	name := r.PathValue("db")
	if err := s.databases.DropPgDatabase(r.Context(), d, name); err != nil {
		s.pgErr(w, "drop database", err)
		return
	}
	s.pgAudit(r, "database:DropPgDatabase", d, map[string]any{"database": name})
	w.WriteHeader(http.StatusNoContent)
}

// ── roles ───────────────────────────────────────────────────────────────────

func (s *Server) handleListPgRoles(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	out, err := s.databases.PgRoles(r.Context(), d)
	if err != nil {
		s.pgErr(w, "list roles", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleGetPgRole(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	role, err := s.databases.PgRoleByName(r.Context(), d, r.PathValue("role"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no role "+r.PathValue("role"))
		return
	}
	if err != nil {
		s.pgErr(w, "get role", err)
		return
	}
	writeJSON(w, http.StatusOK, role)
}

// roleResponse returns the role, and the password once when one was set.
func (s *Server) roleResponse(w http.ResponseWriter, r *http.Request, d store.Database, name, password string, status int) {
	role, err := s.databases.PgRoleByName(r.Context(), d, name)
	if err != nil {
		s.pgErr(w, "get role", err)
		return
	}
	writeJSON(w, status, struct {
		dbs.PgRole
		Password string `json:"password,omitempty"`
	}{role, password})
}

func (s *Server) handleCreatePgRole(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	var in dbs.PgRoleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	pw, err := s.databases.CreatePgRole(r.Context(), d, in)
	if err != nil {
		s.pgErr(w, "create role", err)
		return
	}
	s.pgAudit(r, "database:CreatePgRole", d, roleAudit(in.Name, in))
	s.roleResponse(w, r, d, in.Name, pw, http.StatusCreated)
}

func (s *Server) handleAlterPgRole(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	var in dbs.PgRoleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	name := r.PathValue("role")
	pw, err := s.databases.AlterPgRole(r.Context(), d, name, in)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no role "+name)
		return
	}
	if err != nil {
		s.pgErr(w, "alter role", err)
		return
	}
	s.pgAudit(r, "database:AlterPgRole", d, roleAudit(name, in))
	if in.Name != "" {
		name = in.Name
	}
	s.roleResponse(w, r, d, name, pw, http.StatusOK)
}

// roleAudit records what changed, never a password.
func roleAudit(name string, in dbs.PgRoleInput) map[string]any {
	m := map[string]any{"role": name, "password": in.Password != nil || in.GeneratePassword}
	if in.Name != "" && in.Name != name {
		m["rename"] = in.Name
	}
	for k, v := range map[string]*bool{"login": in.Login, "createDb": in.CreateDB, "createRole": in.CreateRole, "inherit": in.Inherit} {
		if v != nil {
			m[k] = *v
		}
	}
	if in.ConnLimit != nil {
		m["connLimit"] = *in.ConnLimit
	}
	if in.ValidUntil != nil {
		m["validUntil"] = *in.ValidUntil
	}
	if in.MemberOf != nil {
		m["memberOf"] = *in.MemberOf
	}
	return m
}

func (s *Server) handleDropPgRole(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	name, to := r.PathValue("role"), r.URL.Query().Get("reassignTo")
	err := s.databases.DropPgRole(r.Context(), d, name, to)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no role "+name)
		return
	}
	if err != nil {
		s.pgErr(w, "drop role", err)
		return
	}
	s.pgAudit(r, "database:DropPgRole", d, map[string]any{"role": name, "reassignTo": to})
	w.WriteHeader(http.StatusNoContent)
}

// ── schema ──────────────────────────────────────────────────────────────────

func (s *Server) handleGetPgSchema(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	out, err := s.databases.PgSchemaTree(r.Context(), d, q.Get("db"), q.Get("system") == "true")
	if err != nil {
		s.pgErr(w, "read schema", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schemas": out})
}

func (s *Server) handleGetPgObject(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	oid, _ := strconv.ParseUint(q.Get("oid"), 10, 32)
	out, err := s.databases.PgObjectByName(r.Context(), d, q.Get("db"), q.Get("schema"), q.Get("name"), uint32(oid))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such object")
		return
	}
	if err != nil {
		s.pgErr(w, "read object", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReadPgRows(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	var q dbs.PgRowsQuery
	if !decodeJSON(w, r, &q) {
		return
	}
	out, err := s.databases.PgTableRows(r.Context(), d, r.URL.Query().Get("db"), q)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no table "+q.Schema+"."+q.Table)
		return
	}
	if err != nil {
		s.pgErr(w, "read rows", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── privileges ──────────────────────────────────────────────────────────────

func (s *Server) handleGetPgPrivileges(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	oid, _ := strconv.ParseUint(q.Get("oid"), 10, 32)
	ref := dbs.PgObjectRef{Kind: q.Get("kind"), Schema: q.Get("schema"), Name: q.Get("name"), OID: uint32(oid)}
	out, err := s.databases.PgObjectGrants(r.Context(), d, q.Get("db"), ref, q.Get("role"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such object")
		return
	}
	if err != nil {
		s.pgErr(w, "read privileges", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleChangePgPrivileges(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	var req struct {
		Changes []dbs.PgPrivilegeChange `json:"changes"`
		Preset  *dbs.PgAccessPreset     `json:"preset"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	db := r.URL.Query().Get("db")
	var ran []string
	var err error
	if req.Preset != nil {
		if db == "" {
			db = dbs.PgDatabase(d)
		}
		ran, err = s.databases.ApplyPgPreset(r.Context(), d, db, *req.Preset)
	} else {
		ran, err = s.databases.ApplyPgPrivileges(r.Context(), d, db, req.Changes)
	}
	if err != nil {
		s.pgErr(w, "change privileges", err)
		return
	}
	s.pgAudit(r, "database:ChangePgPrivileges", d, map[string]any{"db": db, "statements": ran})
	writeJSON(w, http.StatusOK, map[string]any{"statements": ran})
}

// ── extensions ──────────────────────────────────────────────────────────────

func (s *Server) handleListPgExtensions(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	out, err := s.databases.PgExtensions(r.Context(), d, r.URL.Query().Get("db"))
	if err != nil {
		s.pgErr(w, "list extensions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleChangePgExtension(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	var ch dbs.PgExtensionChange
	if !decodeJSON(w, r, &ch) {
		return
	}
	db := r.URL.Query().Get("db")
	if err := s.databases.ChangePgExtension(r.Context(), d, db, ch); err != nil {
		s.pgErr(w, "change extension", err)
		return
	}
	s.pgAudit(r, "database:ChangePgExtension", d, map[string]any{"db": db, "extension": ch.Name, "action": ch.Action, "cascade": ch.Cascade})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── console ─────────────────────────────────────────────────────────────────

// queryTimeout bounds a console run, whatever statement_timeout it sets.
const queryTimeout = 2 * time.Minute

func (s *Server) handleRunPgQuery(w http.ResponseWriter, r *http.Request) { s.runPgQuery(w, r, false) }

func (s *Server) handleExecutePgQuery(w http.ResponseWriter, r *http.Request) {
	s.runPgQuery(w, r, true)
}

func (s *Server) runPgQuery(w http.ResponseWriter, r *http.Request, write bool) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	var q dbs.PgQuery
	if !decodeJSON(w, r, &q) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	out, err := s.databases.RunPgQuery(ctx, d, q, write)
	if write {
		// Writes are audited with their text; reads only by size.
		detail := map[string]any{"db": out.Database, "role": out.Role, "sql": truncate(q.SQL, 4000), "ok": err == nil && out.Error == nil}
		s.pgAudit(r, "database:ExecutePgQuery", d, detail)
	}
	if err != nil {
		s.pgErr(w, "run query", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── configuration (§13c2) ───────────────────────────────────────────────────

func (s *Server) handleListPgSettings(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	out, err := s.databases.PgSettings(r.Context(), d)
	if err != nil {
		s.pgErr(w, "read settings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "addons": dbs.PgAddons})
}

func (s *Server) handleGetPgReplication(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	out, err := s.databases.PgReplicationStatus(r.Context(), d)
	if err != nil {
		s.pgErr(w, "read replication", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── sessions ────────────────────────────────────────────────────────────────

func (s *Server) handleListPgSessions(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	out, err := s.databases.PgSessions(r.Context(), d, r.URL.Query().Get("all") == "true")
	if err != nil {
		s.pgErr(w, "list sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleCancelPgSession(w http.ResponseWriter, r *http.Request) {
	s.signalPg(w, r, false)
}

func (s *Server) handleTerminatePgSession(w http.ResponseWriter, r *http.Request) {
	s.signalPg(w, r, true)
}

func (s *Server) signalPg(w http.ResponseWriter, r *http.Request, terminate bool) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "pid must be a number")
		return
	}
	if err := s.databases.SignalPgSession(r.Context(), d, pid, terminate); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "no session "+strconv.Itoa(pid))
			return
		}
		s.pgErr(w, "signal session", err)
		return
	}
	action := "database:CancelPgSession"
	if terminate {
		action = "database:TerminatePgSession"
	}
	s.pgAudit(r, action, d, map[string]any{"pid": pid})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── backups (Phase 13c) ─────────────────────────────────────────────────────

func (s *Server) handleListDatabaseBackups(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	out, err := s.databases.Backups(r.Context(), d)
	if err != nil {
		s.pgErr(w, "list backups", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStartDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	d, ok := s.postgres(w, r)
	if !ok {
		return
	}
	run, err := s.databases.StartBackup(r.Context(), d)
	if err != nil {
		s.pgErr(w, "start backup", err)
		return
	}
	s.pgAudit(r, "database:StartDatabaseBackup", d, map[string]any{"run": run.ID, "member": run.Member})
	writeJSON(w, http.StatusAccepted, map[string]any{"id": run.ID, "member": run.Member, "startedAt": run.StartedAt})
}
