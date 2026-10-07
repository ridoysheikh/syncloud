package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"syncloud/internal/logs"
)

// logFilter reads the line filter. stream=access selects request lines
// (Traefik's access log), which take status (2xx…5xx) and client filters.
func logFilter(w http.ResponseWriter, r *http.Request) (logs.Filter, bool) {
	q := r.URL.Query()
	f := logs.Filter{Project: q.Get("project"), Environment: q.Get("environment"), Service: q.Get("service"),
		TaskID: q.Get("task"), Node: q.Get("node"), Text: q.Get("q"), Stream: q.Get("stream"), Client: q.Get("client")}
	switch f.Stream {
	case "", "stdout", "stderr", logs.StreamAccess:
	default:
		writeError(w, http.StatusBadRequest, CodeBadRequest, "stream must be stdout, stderr or access")
		return f, false
	}
	status, ok := requestStatus(q.Get("status"))
	if !ok {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "status must be 2xx, 3xx, 4xx or 5xx")
		return f, false
	}
	f.Status = status
	return f, true
}

// databaseLogFilter narrows f to a database's members when ?database= is
// given, and returns the resource to authorize instead of the project.
func (s *Server) databaseLogFilter(w http.ResponseWriter, r *http.Request, f *logs.Filter) (string, bool) {
	name := r.URL.Query().Get("database")
	if name == "" {
		return "", true
	}
	d, err := s.store.DatabaseByName(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "no database "+name)
		return "", false
	}
	f.Project, f.Environment, f.Service, f.NoProject = d.Project, d.Environment, "db-"+d.Name, d.Standalone()
	return dbSRN(d), true
}

// handleQueryLogs returns stored lines, oldest first (§9.2).
func (s *Server) handleQueryLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "logs are not enabled")
		return
	}
	q := r.URL.Query()
	since := time.Hour
	if v := q.Get("since"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > 31*24*time.Hour {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "since must be a duration like 15m, 1h or 24h (at most 744h)")
			return
		}
		since = d
	}
	limit := 500
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 5000 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "limit must be 1–5000")
			return
		}
		limit = n
	}
	f, ok := logFilter(w, r)
	if !ok {
		return
	}
	if v := q.Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "before must be an RFC 3339 time (a line's time)")
			return
		}
		f.Before = t
	}
	dbRes, ok := s.databaseLogFilter(w, r, &f)
	if !ok || !s.logScope(w, r, f, dbRes) {
		return
	}
	lines, err := s.logs.Query(r.Context(), f, since, limit)
	if errors.Is(err, logs.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
		return
	} else if err != nil {
		s.internalError(w, "query logs", err)
		return
	}
	if lines == nil {
		lines = []logs.Line{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": lines})
}

// handleTailLogs streams new lines as Server-Sent Events until the client
// disconnects. Each event's data is one Line as JSON.
func (s *Server) handleTailLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "logs are not enabled")
		return
	}
	f, ok := logFilter(w, r)
	if !ok {
		return
	}
	dbRes, ok := s.databaseLogFilter(w, r, &f)
	if !ok || !s.logScope(w, r, f, dbRes) {
		return
	}
	rc := http.NewResponseController(w) // unwraps middleware recorders
	c, cancel := s.logs.Tail(f)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": tailing\n\n")
	_ = rc.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			_ = rc.Flush()
		case l, ok := <-c:
			if !ok {
				return
			}
			b, _ := json.Marshal(l)
			fmt.Fprintf(w, "data: %s\n\n", b)
			_ = rc.Flush()
		}
	}
}

// logScope keeps log queries inside what the caller may read (§14): anyone
// without the action everywhere must name a project they are allowed on
// (and the environment and service narrow the check).
//
// A database's logs (?database=) are authorized on the database instead.
func (s *Server) logScope(w http.ResponseWriter, r *http.Request, f logs.Filter, dbRes string) bool {
	action, _ := r.Context().Value(authzActionKey).(string)
	p, _ := principal(r.Context())
	if action == "" || p.isRoot() || s.can(r, action, "srn:syncloud:*") {
		return true
	}
	if dbRes != "" {
		if d := s.decide(r, action, dbRes); !d.Allowed {
			s.denied(w, r, action, dbRes, d.Reason)
			return false
		}
		return true
	}
	if f.Project == "" {
		s.denied(w, r, action, "srn:syncloud:*", "choose a project you may read")
		return false
	}
	res := projectSRN(f.Project)
	if f.Environment != "" {
		res = envSRN(f.Project, f.Environment)
		if f.Service != "" {
			res += "/service/" + f.Service
		}
	}
	if d := s.decide(r, action, res); !d.Allowed {
		s.denied(w, r, action, res, d.Reason)
		return false
	}
	return true
}
