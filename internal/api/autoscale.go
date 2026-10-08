package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ridoysheikh/syncloud/internal/autoscale"
	"github.com/ridoysheikh/syncloud/internal/metrics"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func (s *Server) requireAutoscaler(w http.ResponseWriter) bool {
	if s.autoscaler == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "autoscaling is not enabled")
		return false
	}
	return true
}

type autoscalingView struct {
	Policy  *store.ScalingPolicy `json:"policy"` // null: not autoscaled
	Current autoscale.Current    `json:"current"`
	Units   map[string]string    `json:"units"`
}

// handleGetAutoscaling returns a service's target tracking policy and the
// latest measurement (§5.5).
func (s *Server) handleGetAutoscaling(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutoscaler(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	v := autoscalingView{Current: s.autoscaler.CurrentValue(sv.ID), Units: autoscale.Metrics}
	p, err := s.store.ScalingPolicy(r.Context(), sv.ID)
	if err == nil {
		v.Policy = &p
	} else if !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, "get scaling policy", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handlePutAutoscaling(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutoscaler(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	in := store.ScalingPolicy{Enabled: true}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := autoscale.Validate(&in); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	u, _ := currentUser(r.Context())
	in.ServiceID, in.UpdatedAt, in.UpdatedBy = sv.ID, s.now().UTC().Truncate(1e9), u.Email
	if err := s.store.PutScalingPolicy(r.Context(), in); err != nil {
		s.internalError(w, "save scaling policy", err)
		return
	}
	s.audit(r, u.ID, "service:PutScalingPolicy", srnOf(sv), map[string]any{"min": in.Min, "max": in.Max, "metric": in.Metric, "target": in.Target, "enabled": in.Enabled})
	writeJSON(w, http.StatusOK, autoscalingView{Policy: &in, Current: s.autoscaler.CurrentValue(sv.ID), Units: autoscale.Metrics})
}

func (s *Server) handleDeleteAutoscaling(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutoscaler(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteScalingPolicy(r.Context(), sv.ID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "the service is not autoscaled")
		return
	} else if err != nil {
		s.internalError(w, "delete scaling policy", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "service:DeleteScalingPolicy", srnOf(sv), nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleScalingEvents lists the autoscaler's changes, newest first.
func (s *Server) handleScalingEvents(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "limit must be 1–500")
			return
		}
		limit = n
	}
	evs, err := s.store.ScalingEvents(r.Context(), sv.ID, limit)
	if err != nil {
		s.internalError(w, "list scaling events", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": evs})
}

// handleScalingCharts charts desired and running tasks and the policy's
// metric against its target.
func (s *Server) handleScalingCharts(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "metrics are not enabled")
		return
	}
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	res, err := s.metrics.Scaling(r.Context(), sv.ID, metrics.Ranges[rng], s.now())
	if err != nil {
		s.metricsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
