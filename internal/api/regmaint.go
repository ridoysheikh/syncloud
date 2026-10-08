package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ridoysheikh/syncloud/internal/registry"
	"github.com/ridoysheikh/syncloud/internal/regmaint"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func (s *Server) requireMaint(w http.ResponseWriter) bool {
	if s.regMaint == nil || s.registryBrowser == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "the registry is not enabled")
		return false
	}
	return true
}

func (s *Server) maintError(w http.ResponseWriter, what string, err error) {
	var inv regmaint.ErrInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "no lifecycle policy for this repository")
	case errors.Is(err, regmaint.ErrBusy):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, regmaint.ErrNoRegistry):
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
	case errors.Is(err, registry.ErrUnavailable):
		s.registryErr(w, err)
	default:
		s.internalError(w, what, err)
	}
}

// repoParam reads and checks ?repository=.
func repoParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	repo := r.URL.Query().Get("repository")
	if !repoNameRE.MatchString(repo) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repository must be a registry path like shop/api")
		return "", false
	}
	return repo, true
}

func (s *Server) handleGetLifecycle(w http.ResponseWriter, r *http.Request) {
	if !s.requireMaint(w) {
		return
	}
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	p, err := s.regMaint.GetPolicy(r.Context(), repo)
	if err != nil {
		s.maintError(w, "get lifecycle policy", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePutLifecycle(w http.ResponseWriter, r *http.Request) {
	if !s.requireMaint(w) {
		return
	}
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	var req struct {
		Rules []registry.Rule `json:"rules"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, _ := currentUser(r.Context())
	p, err := s.regMaint.SetPolicy(r.Context(), repo, req.Rules, u.ID)
	if err != nil {
		s.maintError(w, "set lifecycle policy", err)
		return
	}
	s.audit(r, u.ID, "registry:PutLifecyclePolicy", "srn:syncloud:registry/"+repo, nil)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteLifecycle(w http.ResponseWriter, r *http.Request) {
	if !s.requireMaint(w) {
		return
	}
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteLifecyclePolicy(r.Context(), repo); err != nil {
		s.maintError(w, "delete lifecycle policy", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "registry:DeleteLifecyclePolicy", "srn:syncloud:registry/"+repo, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handlePreviewLifecycle is the dry run: what rules would delete right now.
func (s *Server) handlePreviewLifecycle(w http.ResponseWriter, r *http.Request) {
	if !s.requireMaint(w) {
		return
	}
	var req struct {
		Repository string          `json:"repository"`
		Rules      []registry.Rule `json:"rules"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !repoNameRE.MatchString(req.Repository) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repository must be a registry path like shop/api")
		return
	}
	ds, err := s.regMaint.Preview(r.Context(), req.Repository, req.Rules)
	if err != nil {
		s.maintError(w, "preview lifecycle policy", err)
		return
	}
	expire := 0
	for _, d := range ds {
		if d.Expire {
			expire++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": ds, "expire": expire})
}

func (s *Server) handleGCRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireMaint(w) {
		return
	}
	runs, err := s.store.GCRuns(r.Context(), 30)
	if err != nil {
		s.internalError(w, "list registry cleanups", err)
		return
	}
	type runView struct {
		store.GCRun
		Details []regmaint.Expired `json:"details"`
	}
	out := make([]runView, 0, len(runs))
	for _, run := range runs {
		v := runView{GCRun: run, Details: []regmaint.Expired{}}
		_ = jsonUnmarshal(run.Details, &v.Details)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "running": s.regMaint.Running(), "everyHours": int(s.regMaint.Every.Hours())})
}

func (s *Server) handleStartGC(w http.ResponseWriter, r *http.Request) {
	if !s.requireMaint(w) {
		return
	}
	run, err := s.regMaint.Start("manual")
	if err != nil {
		s.maintError(w, "start registry cleanup", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "registry:GarbageCollect", "srn:syncloud:registry", nil)
	writeJSON(w, http.StatusAccepted, run)
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
