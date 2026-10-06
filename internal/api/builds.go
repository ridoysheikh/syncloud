package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"syncloud/internal/builds"
	"syncloud/internal/store"
)

func (s *Server) requireBuilds(w http.ResponseWriter) bool {
	if s.builds == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "builds are not enabled")
		return false
	}
	return true
}

func (s *Server) buildError(w http.ResponseWriter, what string, err error) {
	var inv builds.ErrInvalid
	if errors.As(err, &inv) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
		return
	}
	s.workloadError(w, what, err)
}

func srnOf(sv store.Service) string {
	return "srn:syncloud:service/" + sv.Project + "/" + sv.Environment + "/" + sv.Name
}

// buildView is a build with its service's names.
type buildView struct {
	store.Build
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
}

func (s *Server) buildViews(r *http.Request, bs []store.Build) []buildView {
	names := map[string]store.Service{}
	out := make([]buildView, 0, len(bs))
	for _, b := range bs {
		sv, ok := names[b.ServiceID]
		if !ok {
			sv, _ = s.store.ServiceByID(r.Context(), b.ServiceID)
			names[b.ServiceID] = sv
		}
		out = append(out, buildView{Build: b, Project: sv.Project, Environment: sv.Environment, Service: sv.Name})
	}
	return out
}

func (s *Server) handleGetGitSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuilds(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	src, err := s.builds.GetSource(r.Context(), sv.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "the service has no Git source")
		return
	} else if err != nil {
		s.internalError(w, "get git source", err)
		return
	}
	writeJSON(w, http.StatusOK, src)
}

func (s *Server) handleSetGitSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuilds(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var in builds.Source
	if !decodeJSON(w, r, &in) {
		return
	}
	src, err := s.builds.SetSource(r.Context(), sv, in)
	if err != nil {
		s.buildError(w, "set git source", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "service:SetGitSource", srnOf(sv), map[string]any{"url": src.URL, "branch": src.Branch})
	writeJSON(w, http.StatusOK, src)
}

func (s *Server) handleDeleteGitSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuilds(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteGitSource(r.Context(), sv.ID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "the service has no Git source")
		return
	} else if err != nil {
		s.internalError(w, "delete git source", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "service:DeleteGitSource", srnOf(sv), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleServiceBuilds(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuilds(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	bs, err := s.store.ServiceBuilds(r.Context(), sv.ID, 50)
	if err != nil {
		s.internalError(w, "list builds", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.buildViews(r, bs)})
}

func (s *Server) handleStartBuild(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuilds(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var in struct {
		SHA string `json:"sha"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
		return
	}
	b, err := s.builds.BuildNow(r.Context(), sv, in.SHA)
	if err != nil {
		s.buildError(w, "start build", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "service:Build", srnOf(sv), map[string]any{"sha": b.SHA})
	writeJSON(w, http.StatusAccepted, s.buildViews(r, []store.Build{b})[0])
}

func (s *Server) handleRecentBuilds(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuilds(w) {
		return
	}
	bs, err := s.store.RecentBuilds(r.Context(), 100)
	if err != nil {
		s.internalError(w, "list builds", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.buildViews(r, bs)})
}

func (s *Server) handleDeployBuild(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuilds(w) {
		return
	}
	b, err := s.store.BuildByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no build "+r.PathValue("id"))
		return
	} else if err != nil {
		s.internalError(w, "get build", err)
		return
	}
	if err := s.builds.Deploy(r.Context(), b); err != nil {
		s.buildError(w, "deploy build", err)
		return
	}
	b.Deployed = true
	_ = s.store.UpdateBuild(r.Context(), b)
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "build:Deploy", "srn:syncloud:build/"+b.ID, map[string]any{"image": b.Image})
	writeJSON(w, http.StatusOK, s.buildViews(r, []store.Build{b})[0])
}

// handleGitWebhook is the push hook for GitHub, Gitea/Forgejo and GitLab.
// It only triggers a poll, so the payload itself is never trusted beyond
// the signature check.
func (s *Server) handleGitWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.requireBuilds(w) {
		return
	}
	id := r.PathValue("id")
	secret, err := s.builds.WebhookSecret(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "unknown hook")
		return
	} else if err != nil {
		s.internalError(w, "webhook", err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "unreadable body")
		return
	}
	if !validHookSignature(r.Header, body, secret) {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "bad webhook signature")
		return
	}
	if r.Header.Get("X-GitHub-Event") == "ping" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	s.builds.Check(id)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "checking"})
}

func validHookSignature(h http.Header, body []byte, secret string) bool {
	if tok := h.Get("X-Gitlab-Token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) == 1
	}
	sig, ok := strings.CutPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
	if !ok {
		sig = h.Get("X-Gitea-Signature")
	}
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
