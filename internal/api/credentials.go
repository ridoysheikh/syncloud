package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/store"
)

// Credentials of the signed-in user (§7.1). Managing other users' credentials
// comes with IAM users in Phase 7.

type accessKeyResponse struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	LastUsedIP  string     `json:"lastUsedIp"`
	// Secret is only returned once, when the key is created.
	Secret string `json:"secretAccessKey,omitempty"`
}

func toAccessKeyResponse(k store.AccessKey) accessKeyResponse {
	return accessKeyResponse{ID: k.ID, Description: k.Description, CreatedAt: k.CreatedAt.UTC(), LastUsedAt: utcPtr(k.LastUsedAt), LastUsedIP: k.LastUsedIP}
}

func (s *Server) handleListAccessKeys(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r.Context())
	keys, err := s.store.ListAccessKeys(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, "list access keys", err)
		return
	}
	out := make([]accessKeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAccessKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleCreateAccessKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description string `json:"description"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if len(req.Description) > 200 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "description must be at most 200 characters")
		return
	}
	u, _ := currentUser(r.Context())
	id, secret := auth.NewAccessKey()
	k := store.AccessKey{
		ID: id, UserID: u.ID, Description: req.Description, CreatedAt: s.now().Truncate(time.Second),
		SecretEnc: s.secrets.Seal([]byte(secret), []byte(id)),
	}
	if err := s.store.CreateAccessKey(r.Context(), k); errors.Is(err, store.ErrLimitReached) {
		writeError(w, http.StatusConflict, CodeConflict, "you already have 2 access keys; delete one before creating another")
		return
	} else if err != nil {
		s.internalError(w, "create access key", err)
		return
	}
	s.audit(r, u.ID, "iam:CreateAccessKey", "srn:syncloud:access-key/"+id, nil)
	resp := toAccessKeyResponse(k)
	resp.Secret = secret
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleDeleteAccessKey(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r.Context())
	id := r.PathValue("id")
	if err := s.store.DeleteAccessKey(r.Context(), u.ID, id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such access key")
		return
	} else if err != nil {
		s.internalError(w, "delete access key", err)
		return
	}
	s.audit(r, u.ID, "iam:DeleteAccessKey", "srn:syncloud:access-key/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

type tokenResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	LastUsedIP string     `json:"lastUsedIp"`
	// Token is only returned once, when it is created.
	Token string `json:"token,omitempty"`
}

func toTokenResponse(t store.APIToken) tokenResponse {
	return tokenResponse{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt.UTC(), ExpiresAt: utcPtr(t.ExpiresAt), LastUsedAt: utcPtr(t.LastUsedAt), LastUsedIP: t.LastUsedIP}
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r.Context())
	toks, err := s.store.ListAPITokens(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, "list tokens", err)
		return
	}
	out := make([]tokenResponse, 0, len(toks))
	for _, t := range toks {
		out = append(out, toTokenResponse(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// maxTokenDays bounds token lifetime; 0 means no expiry (allowed, but discouraged in the UI).
const maxTokenDays = 365

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name must be 1–100 characters")
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxTokenDays {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expiresInDays must be between 0 (no expiry) and 365")
		return
	}
	u, _ := currentUser(r.Context())
	now := s.now().Truncate(time.Second)
	token := auth.NewToken("syn_pat_")
	t := store.APIToken{ID: auth.NewID("tok_"), UserID: u.ID, Name: req.Name, TokenHash: auth.HashToken(token), CreatedAt: now}
	if req.ExpiresInDays > 0 {
		exp := now.AddDate(0, 0, req.ExpiresInDays)
		t.ExpiresAt = &exp
	}
	if err := s.store.CreateAPIToken(r.Context(), t); err != nil {
		s.internalError(w, "create token", err)
		return
	}
	s.audit(r, u.ID, "iam:CreateToken", "srn:syncloud:token/"+t.ID, nil)
	resp := toTokenResponse(t)
	resp.Token = token
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r.Context())
	id := r.PathValue("id")
	if err := s.store.DeleteAPIToken(r.Context(), u.ID, id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such token")
		return
	} else if err != nil {
		s.internalError(w, "delete token", err)
		return
	}
	s.audit(r, u.ID, "iam:DeleteToken", "srn:syncloud:token/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
