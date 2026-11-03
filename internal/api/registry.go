package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/domain"
	"github.com/ridoysheikh/syncloud/internal/registry"
	"github.com/ridoysheikh/syncloud/internal/regmaint"
	"github.com/ridoysheikh/syncloud/internal/store"
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
	ip := clientIP(r)
	if s.registryLimiter.Blocked(ip, now) {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many failed registry logins, try again later")
		return
	}
	u, credID, err := s.registryUser(r, user, pass, now)
	if errors.Is(err, store.ErrNotFound) {
		s.registryLimiter.Fail(ip, now)
		s.audit(r, "", "registry:Login", "srn:syncloud:registry", map[string]any{"result": "denied", "username": user})
		w.Header().Set("WWW-Authenticate", `Basic realm="SynCloud registry"`)
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid registry credentials")
		return
	} else if err != nil {
		s.internalError(w, "registry auth", err)
		return
	}

	cred := CredAccessKey
	if strings.HasPrefix(pass, "syn_pat_") {
		cred = CredToken
	}
	iu, err := s.userFor(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid registry credentials")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), principalKey, s.principalFor(iu, cred, credID)))
	var access []registry.Access
	for _, raw := range q["scope"] {
		for _, sc := range strings.Fields(raw) { // several scopes may share one parameter
			a, err := registry.ParseScope(sc)
			if err != nil {
				writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
				return
			}
			if granted := s.grantRegistry(r, a); len(granted.Actions) > 0 {
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

// registryIAM maps registry token actions to IAM actions.
var registryIAM = map[string]string{"pull": "registry:Pull", "push": "registry:Push", "delete": "registry:Delete"}

// grantRegistry keeps the requested actions IAM allows on the repository
// (srn:syncloud:registry/<name>); "*" needs all three.
func (s *Server) grantRegistry(r *http.Request, req registry.Access) registry.Access {
	out := registry.Access{Type: req.Type, Name: req.Name, Actions: []string{}}
	res := "srn:syncloud:registry/" + req.Name
	if req.Type == "registry" { // registry:catalog:*
		res = "srn:syncloud:registry/*"
	}
	for _, a := range req.Actions {
		if !registryActions[a] {
			continue
		}
		ok := true
		if a == "*" {
			for _, x := range registryIAM {
				ok = ok && s.can(r, x, res)
			}
		} else if req.Type == "registry" {
			ok = s.can(r, "registry:ListRepositories", res)
		} else {
			ok = s.can(r, registryIAM[a], res)
		}
		if ok {
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
	out := map[string]any{
		"host":  host,
		"alias": "@registry",
		"login": "docker login " + host + " -u <access-key-id> -p <secret>   (or any user name and a personal access token)",
	}
	// On a private network the certificate is self-signed: Docker clients
	// must be told to trust it (nodes are handled automatically).
	if s.certs != nil && s.domains != nil {
		if base := s.domains.Endpoints().BaseDomain; base != "" {
			if pemText := s.certs.SelfSignedBundle(domain.RegistryHost(base), base); pemText != "" {
				out["selfSigned"] = true
				out["certificate"] = pemText
				out["trust"] = "sudo mkdir -p /etc/docker/certs.d/" + host + " && sudo tee /etc/docker/certs.d/" + host + "/ca.crt >/dev/null <<'EOF'\n" +
					strings.TrimSpace(pemText) + "\nEOF"
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
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
		Lifecycle bool `json:"lifecycle"`
		// LifecycleDefault: no policy of its own, the registry-wide default applies.
		LifecycleDefault bool       `json:"lifecycleDefault"`
		Pulls            int        `json:"pulls"`
		LastPushedAt     *time.Time `json:"lastPushedAt"`
		LastPulledAt     *time.Time `json:"lastPulledAt"`
	}
	byRepo := map[string]*repoView{}
	out := make([]repoView, 0, len(repos))
	for _, rp := range repos {
		own := hasPolicy[rp.Name]
		out = append(out, repoView{Repository: rp, Lifecycle: own || hasPolicy[regmaint.DefaultRepo], LifecycleDefault: !own && hasPolicy[regmaint.DefaultRepo]})
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
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, out, itemRepo)})
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
	if registry.IsSystemRepo(repo) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the platform's own images cannot be deleted")
		return
	}
	// A running service would fail its next pull: say so, and let the caller
	// insist.
	if q.Get("force") == "" {
		if imgs, err := s.registryBrowser.Images(r.Context(), repo); err == nil {
			views, err := s.imageViews(r.Context(), repo, imgs)
			if err != nil {
				s.internalError(w, "registry image users", err)
				return
			}
			for _, v := range views {
				if v.Tag != tag || len(v.InUseBy) == 0 {
					continue
				}
				var names []string
				for _, u := range v.InUseBy {
					names = append(names, u.Project+"/"+u.Environment+"/"+u.Service)
				}
				writeError(w, http.StatusConflict, CodeConflict, repo+":"+tag+" is run by "+strings.Join(names, ", ")+"; deleting it breaks their next pull or rollback (use force to delete anyway)")
				return
			}
		}
	}
	if err := s.registryBrowser.DeleteTag(r.Context(), repo, tag); err != nil {
		s.log.Warn("registry delete failed", "repository", repo, "tag", tag, "err", err)
		s.registryErr(w, err)
		return
	}
	if s.regMaint != nil {
		s.regMaint.InvalidateUsage()
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "registry:DeleteImage", "srn:syncloud:registry/"+repo+":"+tag, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteRepository deletes a whole repository: every tag, the build
// cache and its lifecycle policy. Like a single image it is refused while a
// service runs one of the tags, unless forced.
func (s *Server) handleDeleteRepository(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrowser(w) {
		return
	}
	repo := r.URL.Query().Get("repository")
	if !repoNameRE.MatchString(repo) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repository must be a registry path like shop/api")
		return
	}
	if registry.IsSystemRepo(repo) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the platform's own images cannot be deleted")
		return
	}
	if r.URL.Query().Get("force") == "" {
		imgs, err := s.registryBrowser.Images(r.Context(), repo)
		if err != nil {
			s.registryErr(w, err)
			return
		}
		views, err := s.imageViews(r.Context(), repo, imgs)
		if err != nil {
			s.internalError(w, "registry image users", err)
			return
		}
		users := map[string]bool{}
		var names []string
		for _, v := range views {
			for _, u := range v.InUseBy {
				if n := u.Project + "/" + u.Environment + "/" + u.Service; !users[n] {
					users[n] = true
					names = append(names, n)
				}
			}
		}
		if len(names) > 0 {
			writeError(w, http.StatusConflict, CodeConflict, repo+" is run by "+strings.Join(names, ", ")+"; deleting it breaks their next pull or rollback (use force to delete anyway)")
			return
		}
	}
	removed, err := s.registryBrowser.DeleteRepository(r.Context(), repo)
	if err != nil {
		s.log.Warn("registry repository delete failed", "repository", repo, "removed", removed, "err", err)
		s.registryErr(w, err)
		return
	}
	// A repository pushed again later starts without the old rules.
	if err := s.store.DeleteLifecyclePolicy(r.Context(), repo); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("registry lifecycle policy delete failed", "repository", repo, "err", err)
	}
	if s.regMaint != nil {
		s.regMaint.InvalidateUsage()
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "registry:DeleteRepository", "srn:syncloud:registry/"+repo, map[string]any{"tags": removed})
	w.WriteHeader(http.StatusNoContent)
}

// handleInspectImage describes one tag in depth: layers with the steps that
// made them, platforms and the image's settings.
func (s *Server) handleInspectImage(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrowser(w) {
		return
	}
	repo, tag := r.URL.Query().Get("repository"), r.URL.Query().Get("tag")
	if !repoNameRE.MatchString(repo) || tag == "" || strings.ContainsAny(tag, "/ ") {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repository and tag are required")
		return
	}
	in, err := s.registryBrowser.Inspect(r.Context(), repo, tag)
	if err != nil {
		s.registryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// handleTagImage gives an existing image another tag, so a release can be
// named without a rebuild.
func (s *Server) handleTagImage(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrowser(w) {
		return
	}
	var req struct {
		Repository string `json:"-"`
		From       string `json:"from"`
		Tag        string `json:"tag"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Repository = r.URL.Query().Get("repository")
	if !repoNameRE.MatchString(req.Repository) || !tagRE.MatchString(req.Tag) || req.From == "" || strings.ContainsAny(req.From, "/ ") {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repository, from and a valid tag are required")
		return
	}
	if registry.IsSystemRepo(req.Repository) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the platform's own images cannot be retagged")
		return
	}
	if registry.IsCacheTag(req.Tag) || registry.IsCacheTag(req.From) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the build cache tag belongs to the builders")
		return
	}
	if err := s.registryBrowser.Retag(r.Context(), req.Repository, req.From, req.Tag); err != nil {
		s.registryErr(w, err)
		return
	}
	if s.regMaint != nil {
		s.regMaint.InvalidateUsage()
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "registry:TagImage", "srn:syncloud:registry/"+req.Repository+":"+req.Tag, map[string]any{"from": req.From})
	w.WriteHeader(http.StatusNoContent)
}

var tagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
