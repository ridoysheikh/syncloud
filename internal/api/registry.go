package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/registry"
	"syncloud/internal/store"
)

// handleRegistryToken implements the Docker registry token endpoint (§5.9).
// `docker login registry.<base-domain>` sends Basic credentials:
//   - username = access key ID, password = its secret, or
//   - any username, password = personal access token (syn_pat_…).
func (s *Server) handleRegistryToken(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "registry is not enabled")
		return
	}
	q := r.URL.Query()
	if svc := q.Get("service"); svc != "" && svc != registry.Service {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "unknown service "+svc)
		return
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="SynCloud registry"`)
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "registry credentials required: an access key (ID as username, secret as password) or a personal access token")
		return
	}
	now := s.now()
	if !s.loginLimiter.Allow(clientIP(r), now) {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts, try again later")
		return
	}
	u, credID, err := s.registryUser(r, user, pass, now)
	if errors.Is(err, store.ErrNotFound) {
		s.audit(r, "", "registry:Login", "srn:syncloud:registry", map[string]any{"result": "denied", "username": user})
		w.Header().Set("WWW-Authenticate", `Basic realm="SynCloud registry"`)
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid registry credentials")
		return
	} else if err != nil {
		s.internalError(w, "registry auth", err)
		return
	}

	var access []registry.Access
	for _, raw := range q["scope"] {
		for _, sc := range strings.Fields(raw) { // several scopes may share one parameter
			a, err := registry.ParseScope(sc)
			if err != nil {
				writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
				return
			}
			if granted := grantRegistry(u, a); len(granted.Actions) > 0 {
				access = append(access, granted)
			}
		}
	}
	tok, err := s.registry.Issue(u.ID, access, now)
	if err != nil {
		s.internalError(w, "issue registry token", err)
		return
	}
	s.log.Debug("registry token", "user", u.Email, "cred", credID, "access", access)
	writeJSON(w, http.StatusOK, map[string]any{
		"token": tok, "access_token": tok,
		"expires_in": int(registry.TokenTTL / time.Second), "issued_at": now.UTC().Format(time.RFC3339),
	})
}

// registryUser checks Basic credentials. ErrNotFound means "invalid".
func (s *Server) registryUser(r *http.Request, user, pass string, now time.Time) (store.User, string, error) {
	if strings.HasPrefix(pass, "syn_pat_") {
		t, err := s.store.APITokenByHash(r.Context(), auth.HashToken(pass), now)
		if err != nil {
			return store.User{}, "", err
		}
		u, err := s.store.UserByID(r.Context(), t.UserID)
		return u, t.ID, err
	}
	if strings.HasPrefix(user, "SYNAK") {
		k, err := s.store.AccessKeyByID(r.Context(), user)
		if err != nil {
			return store.User{}, "", err
		}
		secret, err := s.secrets.Open(k.SecretEnc, []byte(k.ID))
		if err != nil {
			return store.User{}, "", err
		}
		if subtle.ConstantTimeCompare(secret, []byte(pass)) != 1 {
			return store.User{}, "", store.ErrNotFound
		}
		u, err := s.store.UserByID(r.Context(), k.UserID)
		return u, k.ID, err
	}
	return store.User{}, "", store.ErrNotFound
}

var registryActions = map[string]bool{"pull": true, "push": true, "delete": true, "*": true}

// grantRegistry decides which requested actions u gets. Until IAM policies
// arrive (Phase 7, registry:Push/Pull per repository), the root account gets
// everything and other accounts get nothing.
func grantRegistry(u store.User, req registry.Access) registry.Access {
	out := registry.Access{Type: req.Type, Name: req.Name, Actions: []string{}}
	if !u.IsRoot {
		return out
	}
	for _, a := range req.Actions {
		if registryActions[a] {
			out.Actions = append(out.Actions, a)
		}
	}
	return out
}
