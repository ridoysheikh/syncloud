package api

import (
	"errors"
	"net/http"
	"strconv"

	"syncloud/internal/alerts"
	"syncloud/internal/store"
)

func (s *Server) requireAlerts(w http.ResponseWriter) bool {
	if s.alerts == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "alerts are not enabled")
		return false
	}
	return true
}

func (s *Server) alertError(w http.ResponseWriter, what string, err error) {
	var inv alerts.ErrInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "not found")
	default:
		s.internalError(w, what, err)
	}
}

type channelInput struct {
	Name   string               `json:"name"`
	Type   string               `json:"type"`
	Config alerts.ChannelConfig `json:"config"`
}

func (s *Server) handleListAlertChannels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	cs, err := s.alerts.Channels(r.Context())
	if err != nil {
		s.alertError(w, "list channels", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cs})
}

func (s *Server) putAlertChannel(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireAlerts(w) {
		return
	}
	var in channelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	c, err := s.alerts.PutChannel(r.Context(), id, in.Name, in.Type, in.Config)
	if err != nil {
		s.alertError(w, "save channel", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "alerts:PutChannel", "srn:syncloud:alerts/channel/"+c.ID, map[string]any{"name": c.Name, "type": c.Type})
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, c)
}

func (s *Server) handleCreateAlertChannel(w http.ResponseWriter, r *http.Request) {
	s.putAlertChannel(w, r, "")
}
func (s *Server) handleUpdateAlertChannel(w http.ResponseWriter, r *http.Request) {
	s.putAlertChannel(w, r, r.PathValue("id"))
}

func (s *Server) handleDeleteAlertChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	if err := s.alerts.DeleteChannel(r.Context(), r.PathValue("id")); err != nil {
		s.alertError(w, "delete channel", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "alerts:DeleteChannel", "srn:syncloud:alerts/channel/"+r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleTestAlertChannel sends a test notification.
func (s *Server) handleTestAlertChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	if err := s.alerts.TestChannel(r.Context(), r.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such channel")
		return
	} else if err != nil {
		writeError(w, http.StatusBadGateway, CodeInternal, "delivery failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivered": true})
}

func (s *Server) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	rs, err := s.alerts.Rules(r.Context())
	if err != nil {
		s.alertError(w, "list rules", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rs, "types": alerts.RuleTypes, "metrics": alerts.RuleMetrics})
}

func (s *Server) putAlertRule(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireAlerts(w) {
		return
	}
	in := alerts.Rule{Enabled: true}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.ID = id
	if id != "" {
		rs, err := s.alerts.Rules(r.Context())
		if err != nil {
			s.alertError(w, "list rules", err)
			return
		}
		found := false
		for _, x := range rs {
			if x.ID == id {
				found, in.CreatedAt = true, x.CreatedAt
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such rule")
			return
		}
	}
	out, err := s.alerts.PutRule(r.Context(), in)
	if err != nil {
		s.alertError(w, "save rule", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "alerts:PutRule", "srn:syncloud:alerts/rule/"+out.ID, map[string]any{"name": out.Name, "type": out.Type})
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

func (s *Server) handleCreateAlertRule(w http.ResponseWriter, r *http.Request) {
	s.putAlertRule(w, r, "")
}
func (s *Server) handleUpdateAlertRule(w http.ResponseWriter, r *http.Request) {
	s.putAlertRule(w, r, r.PathValue("id"))
}

func (s *Server) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	if err := s.alerts.DeleteRule(r.Context(), r.PathValue("id")); err != nil {
		s.alertError(w, "delete rule", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "alerts:DeleteRule", "srn:syncloud:alerts/rule/"+r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleActiveAlerts lists alerts whose condition holds (pending or firing).
func (s *Server) handleActiveAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	states, err := s.alerts.Active(r.Context())
	if err != nil {
		s.alertError(w, "active alerts", err)
		return
	}
	rules, _ := s.alerts.Rules(r.Context())
	byID := map[string]alerts.Rule{}
	for _, x := range rules {
		byID[x.ID] = x
	}
	type view struct {
		store.AlertState
		Rule     string `json:"rule"`
		Severity string `json:"severity"`
	}
	out := make([]view, 0, len(states))
	for _, st := range states {
		x := byID[st.RuleID]
		out = append(out, view{AlertState: st, Rule: x.Name, Severity: x.Severity})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleAlertEvents lists notifications, newest first.
func (s *Server) handleAlertEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	limit, before, ok := pageParams(w, r, 100, 1000)
	if !ok {
		return
	}
	evs, err := s.store.ListAlertEvents(r.Context(), limit, before)
	if err != nil {
		s.internalError(w, "alert events", err)
		return
	}
	writePage(w, evs, limit, func(e store.AlertEvent) string { return strconv.FormatInt(e.ID, 10) })
}
