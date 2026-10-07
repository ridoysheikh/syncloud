package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"syncloud/internal/dbs"
	"syncloud/internal/metrics"
	"syncloud/internal/store"
)

// ── managed databases (Phase 12) ────────────────────────────────────────────

func (s *Server) requireDatabases(w http.ResponseWriter) bool {
	if s.databases == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "managed databases are not enabled")
		return false
	}
	return true
}

// database resolves {project}/{env}/{database}.
func (s *Server) database(w http.ResponseWriter, r *http.Request) (store.Database, bool) {
	if !s.requireDatabases(w) {
		return store.Database{}, false
	}
	e, ok := s.environment(w, r)
	if !ok {
		return store.Database{}, false
	}
	d, err := s.store.DatabaseByName(r.Context(), e.ID, r.PathValue("database"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no database "+r.PathValue("database"))
		return d, false
	} else if err != nil {
		s.internalError(w, "get database", err)
		return d, false
	}
	return d, true
}

func (s *Server) databaseErr(w http.ResponseWriter, what string, err error) {
	var inv dbs.ErrInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, dbs.ErrNoKey), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, dbs.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
	default:
		s.internalError(w, what, err)
	}
}

func dbSRN(d store.Database) string { return envSRN(d.Project, d.Environment) + "/database/" + d.Name }

func (s *Server) handleListAllDatabases(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabases(w) {
		return
	}
	items, err := s.databases.List(r.Context())
	if err != nil {
		s.internalError(w, "list databases", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, items, itemDatabase)})
}

func (s *Server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabases(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	all, err := s.databases.List(r.Context())
	if err != nil {
		s.internalError(w, "list databases", err)
		return
	}
	items := []dbs.View{}
	for _, v := range all {
		if v.Project == r.PathValue("project") && v.Environment == e.Name {
			items = append(items, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabases(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	var req struct {
		Name    string   `json:"name"`
		Version string   `json:"version"`
		Spec    dbs.Spec `json:"spec"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if d := s.decide(r, "database:CreateDatabase", envSRN(r.PathValue("project"), e.Name)+"/database/"+req.Name); !d.Allowed {
		s.denied(w, r, "database:CreateDatabase", envSRN(r.PathValue("project"), e.Name)+"/database/"+req.Name, d.Reason)
		return
	}
	u, _ := currentUser(r.Context())
	v, err := s.databases.Create(r.Context(), e, req.Name, req.Version, req.Spec, u.Email)
	if err != nil {
		s.databaseErr(w, "create database", err)
		return
	}
	s.audit(r, u.ID, "database:Create", envSRN(v.Project, v.Environment)+"/database/"+v.Name, map[string]any{"memory": v.Spec.Memory, "replicas": v.Spec.Replicas})
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleGetDatabase(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.databases.View(r.Context(), d))
}

func (s *Server) handleUpdateDatabase(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	var req struct {
		Spec dbs.Spec `json:"spec"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, _ := currentUser(r.Context())
	v, err := s.databases.Update(r.Context(), d.ID, req.Spec, u.Email)
	if err != nil {
		s.databaseErr(w, "update database", err)
		return
	}
	s.audit(r, u.ID, "database:Update", dbSRN(d), map[string]any{"memory": v.Spec.Memory, "replicas": v.Spec.Replicas})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	if err := s.databases.Delete(r.Context(), d.ID); err != nil {
		s.databaseErr(w, "delete database", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "database:Delete", dbSRN(d), nil)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleDatabaseCredentials(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	c, err := s.databases.Credentials(r.Context(), d)
	if err != nil {
		s.internalError(w, "database credentials", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "database:GetCredentials", dbSRN(d), nil)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleFailoverDatabase(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	if err := s.databases.Failover(r.Context(), d.ID); err != nil {
		s.databaseErr(w, "failover", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "database:Failover", dbSRN(d), nil)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleDatabaseMetrics(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	if s.metrics == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "metrics are not enabled")
		return
	}
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	res, err := s.metrics.Database(r.Context(), d.ID, metrics.Ranges[rng], s.now())
	if err != nil {
		s.metricsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDatabaseEvents(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	ev, err := s.databases.Events(r.Context(), d.ID)
	if err != nil {
		s.internalError(w, "database events", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ev})
}

// ── explorer ────────────────────────────────────────────────────────────────

func (s *Server) handleScanDatabaseKeys(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	cursor, _ := strconv.ParseUint(q.Get("cursor"), 10, 64)
	count, _ := strconv.Atoi(q.Get("count"))
	page, err := s.databases.Scan(r.Context(), d, q.Get("pattern"), q.Get("type"), cursor, count)
	if err != nil {
		s.databaseErr(w, "scan keys", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleGetDatabaseKey(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	v, err := s.databases.Get(r.Context(), d, r.URL.Query().Get("key"))
	if err != nil {
		s.databaseErr(w, "get key", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleSetDatabaseKey(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	var req dbs.Write
	if !decodeJSON(w, r, &req) {
		return
	}
	key := r.URL.Query().Get("key")
	if err := s.databases.Set(r.Context(), d, key, req); err != nil {
		s.databaseErr(w, "set key", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "database:SetKey", dbSRN(d), map[string]any{"key": key, "type": req.Type, "replace": req.Replace})
	v, err := s.databases.Get(r.Context(), d, key)
	if err != nil {
		s.databaseErr(w, "get key", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteDatabaseKey(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	keys := r.URL.Query()["key"]
	if len(keys) == 0 || len(keys) > 1000 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "give 1 to 1000 keys")
		return
	}
	n, err := s.databases.DeleteKeys(r.Context(), d, keys...)
	if err != nil {
		s.databaseErr(w, "delete keys", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "database:DeleteKey", dbSRN(d), map[string]any{"keys": keys})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

func (s *Server) handleExpireDatabaseKey(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	var req struct {
		TTLSeconds int64 `json:"ttlSeconds"` // -1 removes the expiry
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	key := r.URL.Query().Get("key")
	if err := s.databases.Expire(r.Context(), d, key, req.TTLSeconds); err != nil {
		s.databaseErr(w, "expire key", err)
		return
	}
	v, err := s.databases.Get(r.Context(), d, key)
	if err != nil {
		s.databaseErr(w, "get key", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDatabaseCommand(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	var req struct {
		Args []string `json:"args"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.databases.Command(r.Context(), d, req.Args)
	u, _ := currentUser(r.Context())
	cmd := ""
	if len(req.Args) > 0 {
		cmd = strings.ToUpper(req.Args[0])
	}
	s.audit(r, u.ID, "database:RunCommand", dbSRN(d), map[string]any{"command": cmd, "ok": err == nil})
	if err != nil {
		s.databaseErr(w, "run command", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (s *Server) handleDatabaseInfo(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	info, err := s.databases.InfoSections(r.Context(), d)
	if err != nil {
		s.databaseErr(w, "info", err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleDatabaseSlowlog(w http.ResponseWriter, r *http.Request) {
	d, ok := s.database(w, r)
	if !ok {
		return
	}
	items, err := s.databases.SlowLog(r.Context(), d)
	if err != nil {
		s.databaseErr(w, "slowlog", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
