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

func logFilter(r *http.Request) logs.Filter {
	q := r.URL.Query()
	return logs.Filter{Project: q.Get("project"), Environment: q.Get("environment"), Service: q.Get("service"),
		TaskID: q.Get("task"), Node: q.Get("node"), Text: q.Get("q")}
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
	lines, err := s.logs.Query(r.Context(), logFilter(r), since, limit)
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
	rc := http.NewResponseController(w) // unwraps middleware recorders
	c, cancel := s.logs.Tail(logFilter(r))
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
