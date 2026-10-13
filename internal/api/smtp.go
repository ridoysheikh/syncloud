package api

import (
	"errors"
	"net/http"

	"github.com/ridoysheikh/syncloud/internal/alerts"
)

// handleGetSMTP shows the platform SMTP settings (no password, Phase 20a).
func (s *Server) handleGetSMTP(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	v, err := s.alerts.SMTP(r.Context())
	if err != nil {
		s.alertError(w, "get SMTP", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleSetSMTP(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	var in alerts.SMTP
	if !decodeJSON(w, r, &in) {
		return
	}
	v, err := s.alerts.SetSMTP(r.Context(), in)
	if err != nil {
		s.alertError(w, "set SMTP", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "settings:SetSMTP", "srn:syncloud:settings/smtp", map[string]any{"host": v.Host, "port": v.Port, "from": v.From})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteSMTP(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	if err := s.alerts.DeleteSMTP(r.Context()); err != nil {
		s.alertError(w, "delete SMTP", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "settings:DeleteSMTP", "srn:syncloud:settings/smtp", nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleTestSMTP sends a test email; a delivery failure is reported, not an error.
func (s *Server) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	err := s.alerts.TestSMTP(r.Context(), in.To)
	var inv alerts.ErrInvalid
	switch {
	case errors.As(err, &inv), errors.Is(err, alerts.ErrNoSMTP):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case err != nil:
		writeJSON(w, http.StatusOK, map[string]any{"sent": false, "error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"sent": true})
	}
}

// handleRestoreAlertDefaults recreates missing System rules and the Admins
// channel (with the root user's address).
func (s *Server) handleRestoreAlertDefaults(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlerts(w) {
		return
	}
	email := ""
	if root, err := s.store.RootUser(r.Context()); err == nil {
		email = root.Email
	}
	n, err := s.alerts.RestoreDefaults(r.Context(), email)
	if err != nil {
		s.alertError(w, "restore alert defaults", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "alert:RestoreDefaultRules", "srn:syncloud:alert/*", map[string]any{"added": n})
	writeJSON(w, http.StatusOK, map[string]int{"added": n})
}
