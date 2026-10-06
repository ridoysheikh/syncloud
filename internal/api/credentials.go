package api

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/store"
)

// Credentials (§7.1): everyone manages their own; ?userId= manages another
// user's (e.g. a service account), which needs the action on that user.

// credentialOwner returns whose credentials a request manages.
func (s *Server) credentialOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	u, _ := currentUser(r.Context())
	id := r.URL.Query().Get("userId")
	if id == "" || id == u.ID {
		return u.ID, true
	}
	_, path, _ := strings.Cut(r.Pattern, " ")
	action := ActionFor(r.Method, path)
	res := "srn:syncloud:user/" + id
	if d := s.decide(r, action, res); !d.Allowed {
		s.denied(w, r, action, res, d.Reason)
		return "", false
	}
	if _, err := s.store.UserByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such user")
		return "", false
	}
	return id, true
}

// validIPs checks an allow-list of addresses or CIDRs.
func validIPs(ips []string) error {
	if len(ips) > 20 {
		return errors.New("at most 20 allowed addresses")
	}
	for _, x := range ips {
		if _, err := netip.ParsePrefix(x); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(x); err != nil {
			return errors.New(x + " is not an IP address or CIDR")
		}
	}
	return nil
}

type accessKeyResponse struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	LastUsedIP  string     `json:"lastUsedIp"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	AllowedIPs  []string   `json:"allowedIps"`
	// Secret is only returned once, when the key is created.
	Secret string `json:"secretAccessKey,omitempty"`
}

func toAccessKeyResponse(k store.AccessKey) accessKeyResponse {
	return accessKeyResponse{ID: k.ID, Description: k.Description, CreatedAt: k.CreatedAt.UTC(), LastUsedAt: utcPtr(k.LastUsedAt), LastUsedIP: k.LastUsedIP,
		ExpiresAt: utcPtr(k.ExpiresAt), AllowedIPs: nonNil(k.AllowedIPs)}
}

func (s *Server) handleListAccessKeys(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.credentialOwner(w, r)
	if !ok {
		return
	}
	keys, err := s.store.ListAccessKeys(r.Context(), owner)
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
		Description   string   `json:"description"`
		ExpiresInDays int      `json:"expiresInDays"`
		AllowedIPs    []string `json:"allowedIps"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if len(req.Description) > 200 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "description must be at most 200 characters")
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxTokenDays {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expiresInDays must be between 0 (no expiry) and 365")
		return
	}
	if err := validIPs(req.AllowedIPs); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	owner, ok := s.credentialOwner(w, r)
	if !ok {
		return
	}
	u, _ := currentUser(r.Context())
	id, secret := auth.NewAccessKey()
	now := s.now().Truncate(time.Second)
	k := store.AccessKey{
		ID: id, UserID: owner, Description: req.Description, CreatedAt: now,
		SecretEnc: s.secrets.Seal([]byte(secret), []byte(id)), AllowedIPs: req.AllowedIPs,
	}
	if req.ExpiresInDays > 0 {
		exp := now.AddDate(0, 0, req.ExpiresInDays)
		k.ExpiresAt = &exp
	}
	if err := s.store.CreateAccessKey(r.Context(), k); errors.Is(err, store.ErrLimitReached) {
		writeError(w, http.StatusConflict, CodeConflict, "this user already has 2 access keys; delete one before creating another")
		return
	} else if err != nil {
		s.internalError(w, "create access key", err)
		return
	}
	s.audit(r, u.ID, "iam:CreateAccessKey", "srn:syncloud:access-key/"+id, map[string]any{"user": owner})
	resp := toAccessKeyResponse(k)
	resp.Secret = secret
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleDeleteAccessKey(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.credentialOwner(w, r)
	if !ok {
		return
	}
	u, _ := currentUser(r.Context())
	id := r.PathValue("id")
	if err := s.store.DeleteAccessKey(r.Context(), owner, id); errors.Is(err, store.ErrNotFound) {
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
	AllowedIPs []string   `json:"allowedIps"`
	// Token is only returned once, when it is created.
	Token string `json:"token,omitempty"`
}

func toTokenResponse(t store.APIToken) tokenResponse {
	return tokenResponse{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt.UTC(), ExpiresAt: utcPtr(t.ExpiresAt), LastUsedAt: utcPtr(t.LastUsedAt), LastUsedIP: t.LastUsedIP, AllowedIPs: nonNil(t.AllowedIPs)}
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.credentialOwner(w, r)
	if !ok {
		return
	}
	toks, err := s.store.ListAPITokens(r.Context(), owner)
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
		Name          string   `json:"name"`
		ExpiresInDays int      `json:"expiresInDays"`
		AllowedIPs    []string `json:"allowedIps"`
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
	if err := validIPs(req.AllowedIPs); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	owner, ok := s.credentialOwner(w, r)
	if !ok {
		return
	}
	u, _ := currentUser(r.Context())
	now := s.now().Truncate(time.Second)
	token := auth.NewToken("syn_pat_")
	t := store.APIToken{ID: auth.NewID("tok_"), UserID: owner, Name: req.Name, TokenHash: auth.HashToken(token), CreatedAt: now, AllowedIPs: req.AllowedIPs}
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
	owner, ok := s.credentialOwner(w, r)
	if !ok {
		return
	}
	u, _ := currentUser(r.Context())
	id := r.PathValue("id")
	if err := s.store.DeleteAPIToken(r.Context(), owner, id); errors.Is(err, store.ErrNotFound) {
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
