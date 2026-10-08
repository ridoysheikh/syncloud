package api

import (
	"errors"
	"net/http"

	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/upstream"
)

func (s *Server) requireUpstreams(w http.ResponseWriter) bool {
	if s.upstreams == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "upstream credentials are not enabled")
		return false
	}
	return true
}

func (s *Server) handleListUpstreams(w http.ResponseWriter, r *http.Request) {
	if !s.requireUpstreams(w) {
		return
	}
	cs, err := s.store.ListUpstreamCredentials(r.Context())
	if err != nil {
		s.internalError(w, "list upstream credentials", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(cs)})
}

func (s *Server) handlePutUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.requireUpstreams(w) {
		return
	}
	var req struct {
		Host     string `json:"host"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	c, err := s.upstreams.Put(r.Context(), req.Host, req.Username, req.Password)
	var inv upstream.ErrInvalid
	if errors.As(err, &inv) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
		return
	} else if err != nil {
		s.internalError(w, "save upstream credential", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "registry:ManageUpstreamCredentials", "srn:syncloud:upstream/"+c.Host, map[string]any{"username": c.Username})
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.requireUpstreams(w) {
		return
	}
	if err := s.store.DeleteUpstreamCredential(r.Context(), r.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such credential")
		return
	} else if err != nil {
		s.internalError(w, "delete upstream credential", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "registry:ManageUpstreamCredentials", "srn:syncloud:upstream/"+r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}
