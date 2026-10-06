package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
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

var repoNameRE = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*$`)

func (s *Server) requireBrowser(w http.ResponseWriter) bool {
	if s.registryBrowser == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "the registry is not enabled")
		return false
	}
	return true
}

func (s *Server) registryErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, registry.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
	case strings.HasPrefix(err.Error(), "not found"):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	default:
		writeError(w, http.StatusBadGateway, CodeInternal, err.Error())
	}
}

// handleRegistryInfo tells clients how to push (§5.10 push commands).
func (s *Server) handleRegistryInfo(w http.ResponseWriter, r *http.Request) {
	host := ""
	if s.domains != nil {
		host = s.domains.Endpoints().RegistryHost
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":  host,
		"alias": "@registry",
		"login": "docker login " + host + " -u <access-key-id> -p <secret>   (or any user name and a personal access token)",
	})
}

func (s *Server) handleListRepositories(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrowser(w) {
		return
	}
	repos, err := s.registryBrowser.Repositories(r.Context())
	if err != nil {
		s.registryErr(w, err)
		return
	}
	policies, err := s.store.ListLifecyclePolicies(r.Context())
	if err != nil {
		s.internalError(w, "list lifecycle policies", err)
		return
	}
	hasPolicy := map[string]bool{}
	for _, p := range policies {
		hasPolicy[p.Repository] = true
	}
	stats, err := s.store.ImageStatsFor(r.Context(), "")
	if err != nil {
		s.internalError(w, "registry stats", err)
		return
	}
	type repoView struct {
		registry.Repository
		Lifecycle    bool       `json:"lifecycle"`
		Pulls        int        `json:"pulls"`
		LastPushedAt *time.Time `json:"lastPushedAt"`
		LastPulledAt *time.Time `json:"lastPulledAt"`
	}
	byRepo := map[string]*repoView{}
	out := make([]repoView, 0, len(repos))
	for _, rp := range repos {
		out = append(out, repoView{Repository: rp, Lifecycle: hasPolicy[rp.Name]})
	}
	for i := range out {
		byRepo[out[i].Name] = &out[i]
	}
	for _, st := range stats {
		if v := byRepo[st.Repository]; v != nil {
			v.Pulls += st.Pulls
			v.LastPushedAt = latest(v.LastPushedAt, st.LastPushedAt)
			v.LastPulledAt = latest(v.LastPulledAt, st.LastPulledAt)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrowser(w) {
		return
	}
	repo := r.URL.Query().Get("repository")
	if !repoNameRE.MatchString(repo) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repository must be a registry path like shop/api")
		return
	}
	imgs, err := s.registryBrowser.Images(r.Context(), repo)
	if err != nil {
		s.registryErr(w, err)
		return
	}
	out, err := s.imageViews(r.Context(), repo, imgs)
	if err != nil {
		s.internalError(w, "registry image stats", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleDeleteImage(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrowser(w) {
		return
	}
	q := r.URL.Query()
	repo, tag := q.Get("repository"), q.Get("tag")
	if !repoNameRE.MatchString(repo) || tag == "" || strings.ContainsAny(tag, "/ ") {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repository and tag are required")
		return
	}
	if err := s.registryBrowser.DeleteTag(r.Context(), repo, tag); err != nil {
		s.registryErr(w, err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "registry:DeleteImage", "srn:syncloud:registry/"+repo+":"+tag, nil)
	w.WriteHeader(http.StatusNoContent)
}
