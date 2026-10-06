package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"syncloud/internal/metrics"
)

// metricsRange reads ?range= (default 1h).
func metricsRange(w http.ResponseWriter, r *http.Request) (string, bool) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "1h"
	}
	if _, ok := metrics.Ranges[rng]; !ok {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "range must be one of 15m, 1h, 6h, 24h, 7d")
		return "", false
	}
	return rng, true
}

func (s *Server) writeMetrics(w http.ResponseWriter, r *http.Request, sc metrics.Scope, rng string) {
	if s.metrics == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "metrics are not enabled")
		return
	}
	res, err := s.metrics.Query(r.Context(), sc, metrics.Ranges[rng], s.now())
	if errors.Is(err, metrics.ErrDisabled) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleServiceMetrics charts a service's tasks: CPU, memory, network, disk.
func (s *Server) handleServiceMetrics(w http.ResponseWriter, r *http.Request) {
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	s.writeMetrics(w, r, metrics.Scope{ServiceID: sv.ID}, rng)
}

// handleEnvironmentMetrics charts every service of a project environment.
func (s *Server) handleEnvironmentMetrics(w http.ResponseWriter, r *http.Request) {
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	s.writeMetrics(w, r, metrics.Scope{Project: r.PathValue("project"), Environment: e.Name}, rng)
}

// handleNetworkThroughput charts node and mesh throughput and ranks the
// busiest services and tasks (§8.4).
func (s *Server) handleNetworkThroughput(w http.ResponseWriter, r *http.Request) {
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	if s.metrics == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "metrics are not enabled")
		return
	}
	res, err := s.metrics.Network(r.Context(), metrics.Ranges[rng], s.now())
	if errors.Is(err, metrics.ErrDisabled) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleExploreMetrics runs a PromQL range query (Monitoring › Metrics).
// Queries can read every project's series, so the action is checked on the
// whole metrics store (srn:syncloud:metrics).
func (s *Server) handleExploreMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "metrics are not enabled")
		return
	}
	q := r.URL.Query()
	rng := q.Get("range")
	if rng == "" {
		rng = "1h"
	}
	d, ok := metrics.Ranges[rng]
	if !ok {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "range must be one of 15m, 1h, 6h, 24h, 7d")
		return
	}
	step, _ := strconv.Atoi(q.Get("step"))
	end := s.now()
	series, truncated, err := s.metrics.Explore(r.Context(), q.Get("query"), end.Add(-d), end, step)
	switch {
	case errors.Is(err, metrics.ErrDisabled):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case err != nil && strings.Contains(err.Error(), "unreachable"):
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"series": series, "truncated": truncated})
	}
}

func (s *Server) handleMetricNames(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "metrics are not enabled")
		return
	}
	names, err := s.metrics.MetricNames(r.Context())
	if errors.Is(err, metrics.ErrDisabled) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": names})
}
