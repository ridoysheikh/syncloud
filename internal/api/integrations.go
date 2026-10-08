package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/gitconn"
	"github.com/ridoysheikh/syncloud/internal/gitprovider"
	"github.com/ridoysheikh/syncloud/internal/gitserver"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func (s *Server) requireGitConns(w http.ResponseWriter) bool {
	if s.gitConns == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "Git connections are not enabled")
		return false
	}
	return true
}

func (s *Server) gitConnError(w http.ResponseWriter, what string, err error) {
	var inv gitconn.ErrInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such Git connection")
	case errors.Is(err, store.ErrInUse):
		writeError(w, http.StatusConflict, CodeConflict, "services still build from this connection: disconnect them first")
	default:
		s.internalError(w, what, err)
	}
}

func (s *Server) handleListGitConnections(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) {
		return
	}
	list, err := s.gitConns.List(r.Context())
	if err != nil {
		s.internalError(w, "list git connections", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// handleCreateGitConnection connects GitHub, GitLab or Gitea with a token.
func (s *Server) handleCreateGitConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) {
		return
	}
	var in gitconn.TokenInput
	if !decodeJSON(w, r, &in) {
		return
	}
	v, err := s.gitConns.AddToken(r.Context(), in)
	if err != nil {
		s.gitConnError(w, "add git connection", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "integration:CreateGitConnection", "srn:syncloud:integration/git/"+v.Name, map[string]any{"kind": v.Kind, "account": v.Account})
	writeJSON(w, http.StatusCreated, v)
}

type gitConnectionDetail struct {
	gitconn.View
	// Installations are the accounts a GitHub App is installed on.
	Installations []string `json:"installations"`
	// Problem is set when the provider cannot be reached with the stored
	// credentials.
	Problem string `json:"problem,omitempty"`
}

func (s *Server) handleGetGitConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) {
		return
	}
	v, err := s.gitConns.Get(r.Context(), r.PathValue("connection"))
	if err != nil {
		s.gitConnError(w, "get git connection", err)
		return
	}
	d := gitConnectionDetail{View: v, Installations: []string{}}
	if _, p, err := s.gitConns.Provider(r.Context(), v.ID); err != nil {
		d.Problem = err.Error()
	} else if _, err := p.Account(r.Context()); err != nil {
		d.Problem = err.Error()
	} else if ins, err := gitprovider.Installations(r.Context(), p); err != nil {
		d.Problem = err.Error()
	} else if ins != nil {
		d.Installations = ins
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteGitConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) {
		return
	}
	name := r.PathValue("connection")
	if err := s.gitConns.Delete(r.Context(), name); err != nil {
		s.gitConnError(w, "delete git connection", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "integration:DeleteGitConnection", "srn:syncloud:integration/git/"+name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListGitRepos(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) {
		return
	}
	_, p, err := s.gitConns.Provider(r.Context(), r.PathValue("connection"))
	if err != nil {
		s.gitConnError(w, "git connection", err)
		return
	}
	repos, err := p.Repos(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		writeError(w, http.StatusBadGateway, CodeBadRequest, "the Git host refused: "+err.Error())
		return
	}
	if repos == nil {
		repos = []gitprovider.Repo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": repos})
}

func (s *Server) handleListGitBranches(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) {
		return
	}
	repo := r.URL.Query().Get("repo")
	if !gitprovider.ValidRepoName(repo) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "repo must be owner/name")
		return
	}
	_, p, err := s.gitConns.Provider(r.Context(), r.PathValue("connection"))
	if err != nil {
		s.gitConnError(w, "git connection", err)
		return
	}
	branches, err := p.Branches(r.Context(), repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, CodeBadRequest, "the Git host refused: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": branches})
}

// handleGitHubManifest starts creating a GitHub App: the dashboard posts the
// returned manifest to GitHub.
func (s *Server) handleGitHubManifest(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) {
		return
	}
	var in gitconn.ManifestInput
	if !decodeJSON(w, r, &in) {
		return
	}
	u, _ := currentUser(r.Context())
	m, err := s.gitConns.Manifest(u.ID, in)
	if err != nil {
		s.gitConnError(w, "github manifest", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleGitHubCallback is where GitHub sends the browser after the app was
// created: save it, then continue to installing it on repositories.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) {
		return
	}
	u, _ := currentUser(r.Context())
	q := r.URL.Query()
	v, err := s.gitConns.CompleteManifest(r.Context(), u.ID, q.Get("code"), q.Get("state"))
	if err != nil {
		var inv gitconn.ErrInvalid
		msg := "could not save the GitHub App"
		if errors.As(err, &inv) {
			msg = inv.Error()
		} else {
			s.log.Error("github app callback", "err", err)
		}
		http.Redirect(w, r, "/integrations?error="+url.QueryEscape(msg), http.StatusFound)
		return
	}
	s.audit(r, u.ID, "integration:CompleteGitHubApp", "srn:syncloud:integration/git/"+v.Name, map[string]any{"app": v.AppSlug, "account": v.Account})
	http.Redirect(w, r, v.InstallURL, http.StatusFound)
}

// handleGitHubAppWebhook receives a GitHub App's events: a push checks every
// service building that repository.
func (s *Server) handleGitHubAppWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitConns(w) || !s.requireBuilds(w) {
		return
	}
	secret, err := s.gitConns.AppWebhookSecret(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "unknown hook")
		return
	} else if err != nil {
		s.internalError(w, "app webhook", err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "unreadable body")
		return
	}
	sig, _ := strings.CutPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
	got, _ := hex.DecodeString(sig)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if len(got) == 0 || !hmac.Equal(got, mac.Sum(nil)) {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "bad webhook signature")
		return
	}
	if r.Header.Get("X-GitHub-Event") != "push" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	var ev struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &ev); err != nil || ev.Repository.FullName == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "not a push event")
		return
	}
	n := s.builds.CheckRepo(r.Context(), r.PathValue("id"), ev.Repository.FullName)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "checking", "sources": n})
}

func (s *Server) requireGitServer(w http.ResponseWriter) bool {
	if s.gitServer == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "the built-in Git server is not available")
		return false
	}
	return true
}

func (s *Server) handleGetGitServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitServer(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.gitServer.Status())
}

// handleSetGitServer turns the built-in Git server (Forgejo) on or off.
func (s *Server) handleSetGitServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitServer(w) {
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.gitServer.SetEnabled(r.Context(), in.Enabled); errors.Is(err, gitserver.ErrInUse) {
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
		return
	} else if err != nil {
		s.internalError(w, "git server", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "gitserver:SetGitServer", "srn:syncloud:gitserver", map[string]any{"enabled": in.Enabled})
	writeJSON(w, http.StatusOK, s.gitServer.Status())
}

// handleGitServerCredentials reveals the Git server administrator's sign-in.
func (s *Server) handleGitServerCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.requireGitServer(w) {
		return
	}
	user, password, on := s.gitServer.Credentials()
	if !on {
		writeError(w, http.StatusNotFound, CodeNotFound, "the built-in Git server is off")
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "gitserver:GetGitServerCredentials", "srn:syncloud:gitserver", nil)
	writeJSON(w, http.StatusOK, map[string]string{"username": user, "password": password, "url": s.gitServer.RootURL()})
}
