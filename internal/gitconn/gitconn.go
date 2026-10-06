// Package gitconn keeps the cluster's Git provider connections (§5.8):
// GitHub Apps created from a manifest in one click, and access tokens for
// GitHub, GitLab and Gitea/Forgejo. Services then pick a repository from a
// list; SynCloud mints clone tokens, creates the push webhook and reports
// commit statuses.
package gitconn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/gitprovider"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

// HookPathGitHubApp receives a GitHub App's webhook: /api/v1/hooks/github-app/{id}.
const HookPathGitHubApp = "/api/v1/hooks/github-app/"

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

// View is a connection as the API shows it (no secrets).
type View struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	APIURL    string    `json:"apiUrl"`
	WebURL    string    `json:"webUrl"`
	Account   string    `json:"account"`
	AppSlug   string    `json:"appSlug,omitempty"`
	AppID     int64     `json:"appId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// InstallURL adds the GitHub App to more accounts or repositories.
	InstallURL string `json:"installUrl,omitempty"`
	// Services built from this connection, as project/environment/service,
	// and the repository each builds.
	Services     []string          `json:"services"`
	ServiceRepos map[string]string `json:"serviceRepos"`
}

type Manager struct {
	st  *store.Store
	box *secrets.Box
	log *slog.Logger
	// DashboardURL is where Git hosts send webhooks and users come back.
	DashboardURL func() string

	mu        sync.Mutex
	providers map[string]gitprovider.Provider
	pending   map[string]manifestState
	now       func() time.Time
}

type manifestState struct {
	id      string // the connection's ID, chosen up front for the webhook URL
	userID  string
	name    string
	webURL  string
	apiURL  string
	expires time.Time
}

func New(st *store.Store, box *secrets.Box, dashboardURL func() string, log *slog.Logger) *Manager {
	return &Manager{st: st, box: box, log: log, DashboardURL: dashboardURL, providers: map[string]gitprovider.Provider{},
		pending: map[string]manifestState{}, now: time.Now}
}

func aad(id string) []byte { return []byte("gitconn:" + id) }

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (m *Manager) view(ctx context.Context, c store.GitConnection) View {
	v := View{ID: c.ID, Kind: c.Kind, Name: c.Name, APIURL: c.APIURL, WebURL: c.WebURL, Account: c.Account, AppSlug: c.AppSlug, AppID: c.AppID,
		CreatedAt: c.CreatedAt, Services: []string{}, ServiceRepos: map[string]string{}}
	if c.Kind == gitprovider.KindGitHubApp && c.AppSlug != "" {
		v.InstallURL = strings.TrimRight(c.WebURL, "/") + "/apps/" + c.AppSlug + "/installations/new"
	}
	if gs, err := m.st.GitSourcesByConnection(ctx, c.ID); err == nil {
		for _, g := range gs {
			if sv, err := m.st.ServiceByID(ctx, g.ServiceID); err == nil {
				key := sv.Project + "/" + sv.Environment + "/" + sv.Name
				v.Services = append(v.Services, key)
				v.ServiceRepos[key] = g.Repo
			}
		}
	}
	return v
}

func (m *Manager) List(ctx context.Context) ([]View, error) {
	cs, err := m.st.ListGitConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(cs))
	for _, c := range cs {
		out = append(out, m.view(ctx, c))
	}
	return out, nil
}

func (m *Manager) Get(ctx context.Context, idOrName string) (View, error) {
	c, err := m.st.GitConnection(ctx, idOrName)
	if err != nil {
		return View{}, err
	}
	return m.view(ctx, c), nil
}

func (m *Manager) secrets(c store.GitConnection) (gitprovider.Secrets, error) {
	var s gitprovider.Secrets
	b, err := m.box.Open(c.SecretsEnc, aad(c.ID))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// Provider returns a connection and its API client (cached, so GitHub App
// installation tokens are reused until they near expiry).
func (m *Manager) Provider(ctx context.Context, idOrName string) (store.GitConnection, gitprovider.Provider, error) {
	c, err := m.st.GitConnection(ctx, idOrName)
	if err != nil {
		return c, nil, err
	}
	m.mu.Lock()
	p, ok := m.providers[c.ID]
	m.mu.Unlock()
	if ok {
		return c, p, nil
	}
	sec, err := m.secrets(c)
	if err != nil {
		return c, nil, fmt.Errorf("open connection secrets: %w", err)
	}
	p, err = gitprovider.New(gitprovider.Conn{Kind: c.Kind, APIURL: c.APIURL, AppID: c.AppID, Secrets: sec})
	if err != nil {
		return c, nil, err
	}
	m.mu.Lock()
	m.providers[c.ID] = p
	m.mu.Unlock()
	return c, p, nil
}

// AppWebhookSecret is a GitHub App connection's webhook secret.
func (m *Manager) AppWebhookSecret(ctx context.Context, id string) (string, error) {
	c, err := m.st.GitConnection(ctx, id)
	if err != nil {
		return "", err
	}
	if c.Kind != gitprovider.KindGitHubApp {
		return "", store.ErrNotFound
	}
	sec, err := m.secrets(c)
	return sec.WebhookSecret, err
}

// TokenInput connects GitHub, GitLab or Gitea with an access token.
type TokenInput struct {
	Kind  string `json:"kind"` // github | gitlab | gitea
	Name  string `json:"name"`
	URL   string `json:"url"` // the server ("" = github.com / gitlab.com)
	Token string `json:"token"`
}

// AddToken verifies a token against the provider and saves it.
func (m *Manager) AddToken(ctx context.Context, in TokenInput) (View, error) {
	if !nameRE.MatchString(in.Name) {
		return View{}, ErrInvalid{errors.New("name must be lowercase letters, digits and dashes (at most 40)")}
	}
	if strings.TrimSpace(in.Token) == "" {
		return View{}, ErrInvalid{errors.New("token is required")}
	}
	base := strings.TrimRight(strings.TrimSpace(in.URL), "/")
	var apiURL, webURL string
	switch in.Kind {
	case gitprovider.KindGitHub:
		if base == "" {
			base = "https://github.com"
		}
		apiURL, webURL = gitprovider.GitHubAPIFor(base), base
	case gitprovider.KindGitLab:
		if base == "" {
			base = "https://gitlab.com"
		}
		apiURL, webURL = base, base
	case gitprovider.KindGitea:
		if base == "" {
			return View{}, ErrInvalid{errors.New("url of the Gitea or Forgejo server is required")}
		}
		apiURL, webURL = base, base
	default:
		return View{}, ErrInvalid{errors.New("kind must be github, gitlab or gitea (use the GitHub App flow for github-app)")}
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return View{}, ErrInvalid{errors.New("url must be http(s)://host[:port] without credentials")}
	}
	sec := gitprovider.Secrets{Token: strings.TrimSpace(in.Token)}
	p, err := gitprovider.New(gitprovider.Conn{Kind: in.Kind, APIURL: apiURL, Secrets: sec})
	if err != nil {
		return View{}, ErrInvalid{err}
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	account, err := p.Account(cctx)
	if err != nil {
		return View{}, ErrInvalid{fmt.Errorf("the token was not accepted by %s: %w", webURL, err)}
	}
	return m.save(ctx, store.GitConnection{Kind: in.Kind, Name: in.Name, APIURL: apiURL, WebURL: webURL, Account: account}, sec)
}

func (m *Manager) save(ctx context.Context, c store.GitConnection, sec gitprovider.Secrets) (View, error) {
	if c.ID == "" {
		c.ID = auth.NewID("gc_")
	}
	c.CreatedAt = m.now().UTC().Truncate(time.Second)
	b, _ := json.Marshal(sec)
	c.SecretsEnc = m.box.Seal(b, aad(c.ID))
	if err := m.st.CreateGitConnection(ctx, c); err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			return View{}, ErrInvalid{fmt.Errorf("a connection named %s already exists", c.Name)}
		}
		return View{}, err
	}
	m.log.Info("git connection added", "name", c.Name, "kind", c.Kind, "account", c.Account)
	return m.view(ctx, c), nil
}

// Delete removes a connection no service builds from.
func (m *Manager) Delete(ctx context.Context, idOrName string) error {
	c, err := m.st.GitConnection(ctx, idOrName)
	if err != nil {
		return err
	}
	if err := m.st.DeleteGitConnection(ctx, c.ID); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.providers, c.ID)
	m.mu.Unlock()
	return nil
}

// ManifestInput starts creating a GitHub App.
type ManifestInput struct {
	Name string `json:"name"` // the connection name (default "github")
	// Org creates the app under an organization ("" = the signed-in user).
	Org string `json:"org"`
	// GitHubURL is a GitHub Enterprise server ("" = github.com).
	GitHubURL string `json:"githubUrl"`
}

// Manifest is what the browser posts to GitHub.
type Manifest struct {
	// PostURL is the form action; Manifest is the "manifest" field.
	PostURL  string `json:"postUrl"`
	Manifest string `json:"manifest"`
	State    string `json:"state"`
}

var orgRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// Manifest prepares the GitHub App manifest flow: the dashboard posts the
// manifest to GitHub, the person confirms there, and GitHub redirects to
// the callback with a code (valid for an hour) and this state (10 minutes).
func (m *Manager) Manifest(userID string, in ManifestInput) (Manifest, error) {
	if in.Name == "" {
		in.Name = "github"
	}
	if !nameRE.MatchString(in.Name) {
		return Manifest{}, ErrInvalid{errors.New("name must be lowercase letters, digits and dashes (at most 40)")}
	}
	if _, err := m.st.GitConnection(context.Background(), in.Name); err == nil {
		return Manifest{}, ErrInvalid{fmt.Errorf("a connection named %s already exists", in.Name)}
	}
	if in.Org != "" && !orgRE.MatchString(in.Org) {
		return Manifest{}, ErrInvalid{errors.New("invalid organization name")}
	}
	web := strings.TrimRight(strings.TrimSpace(in.GitHubURL), "/")
	if web == "" {
		web = "https://github.com"
	}
	if u, err := url.Parse(web); err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		return Manifest{}, ErrInvalid{errors.New("githubUrl must be https://host of a GitHub Enterprise server")}
	}
	dash := strings.TrimRight(m.DashboardURL(), "/")
	if dash == "" {
		return Manifest{}, ErrInvalid{errors.New("the dashboard has no URL yet")}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	host := dash
	if u, err := url.Parse(dash); err == nil {
		host = u.Hostname()
	}
	// GitHub cannot deliver to a private dashboard: keep the webhook off
	// (polling builds instead) rather than failing every delivery.
	public := true
	if ip := net.ParseIP(host); host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && (ip.IsPrivate() || ip.IsLoopback())) {
		public = false
	}
	// App names are unique on GitHub: add a few random characters.
	appName := truncateName("SynCloud "+host, 29) + " " + state[:4]
	id := auth.NewID("gc_")
	manifest := map[string]any{
		"name":            appName,
		"url":             dash,
		"hook_attributes": map[string]any{"url": dash + HookPathGitHubApp + id, "active": public},
		"redirect_url":    dash + "/api/v1/integrations/github/callback",
		"setup_url":       dash + "/integrations?installed=github",
		"setup_on_update": true,
		"public":          false,
		"default_permissions": map[string]string{
			"contents": "read", "metadata": "read", "statuses": "write", "pull_requests": "read",
		},
		"default_events": []string{"push", "pull_request"},
	}
	mb, _ := json.Marshal(manifest)
	post := web + "/settings/apps/new?state=" + state
	if in.Org != "" {
		post = web + "/organizations/" + url.PathEscape(in.Org) + "/settings/apps/new?state=" + state
	}
	m.mu.Lock()
	for k, p := range m.pending {
		if m.now().After(p.expires) {
			delete(m.pending, k)
		}
	}
	m.pending[state] = manifestState{id: id, userID: userID, name: in.Name, webURL: web, apiURL: gitprovider.GitHubAPIFor(web), expires: m.now().Add(10 * time.Minute)}
	m.mu.Unlock()
	return Manifest{PostURL: post, Manifest: string(mb), State: state}, nil
}

func truncateName(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// CompleteManifest exchanges GitHub's code for the app's credentials and
// saves the connection. It returns where to install the app.
func (m *Manager) CompleteManifest(ctx context.Context, userID, code, state string) (View, error) {
	m.mu.Lock()
	p, ok := m.pending[state]
	delete(m.pending, state)
	m.mu.Unlock()
	if !ok || m.now().After(p.expires) || p.userID != userID {
		return View{}, ErrInvalid{errors.New("this GitHub App setup expired or belongs to another session: start again")}
	}
	if code == "" || len(code) > 200 {
		return View{}, ErrInvalid{errors.New("GitHub sent no code")}
	}
	conv, err := gitprovider.ConvertManifest(ctx, p.apiURL, code)
	if err != nil {
		return View{}, ErrInvalid{fmt.Errorf("GitHub did not complete the app: %w", err)}
	}
	c := store.GitConnection{ID: p.id, Kind: gitprovider.KindGitHubApp, Name: p.name, APIURL: p.apiURL, WebURL: p.webURL, Account: conv.Owner.Login,
		AppID: conv.ID, AppSlug: conv.Slug}
	sec := gitprovider.Secrets{PrivateKey: conv.PEM, ClientID: conv.ClientID, ClientSecret: conv.ClientSecret, WebhookSecret: conv.WebhookSecret}
	return m.save(ctx, c, sec)
}
