package gitprovider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// github is GitHub or GitHub Enterprise, with a token or as a GitHub App.
type github struct {
	h     *httpc
	token string
	app   *ghApp
}

type ghRepo struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

func (r ghRepo) repo() Repo {
	return Repo{FullName: r.FullName, CloneURL: r.CloneURL, WebURL: r.HTMLURL, DefaultBranch: r.DefaultBranch, Private: r.Private}
}

// auth returns the Authorization header to act on a repository ("" =
// the app or token itself).
func (g *github) auth(ctx context.Context, fullName string) ([2]string, error) {
	if g.app == nil {
		return [2]string{"Authorization", "Bearer " + g.token}, nil
	}
	if fullName == "" {
		jwt, err := g.app.jwt(time.Now())
		return [2]string{"Authorization", "Bearer " + jwt}, err
	}
	tok, err := g.installationToken(ctx, fullName)
	return [2]string{"Authorization", "Bearer " + tok}, err
}

func (g *github) Account(ctx context.Context) (string, error) {
	a, err := g.auth(ctx, "")
	if err != nil {
		return "", err
	}
	if g.app != nil {
		var out struct {
			Slug  string `json:"slug"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		}
		if err := g.h.do(ctx, "GET", "/app", a, nil, &out); err != nil {
			return "", err
		}
		return out.Owner.Login, nil
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := g.h.do(ctx, "GET", "/user", a, nil, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

func (g *github) Repos(ctx context.Context, query string) ([]Repo, error) {
	var out []Repo
	if g.app == nil {
		a, _ := g.auth(ctx, "")
		for page := 1; page <= maxRepos/100; page++ {
			var rs []ghRepo
			if err := g.h.do(ctx, "GET", fmt.Sprintf("/user/repos?per_page=100&sort=pushed&page=%d", page), a, nil, &rs); err != nil {
				return nil, err
			}
			for _, r := range rs {
				if matches(r.FullName, query) {
					out = append(out, r.repo())
				}
			}
			if len(rs) < 100 {
				break
			}
		}
		return out, nil
	}
	installs, err := g.installations(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range installs {
		tok, err := g.app.token(ctx, g.h, in.ID)
		if err != nil {
			return nil, err
		}
		for page := 1; page <= maxRepos/100; page++ {
			var rs struct {
				Repositories []ghRepo `json:"repositories"`
			}
			if err := g.h.do(ctx, "GET", fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), [2]string{"Authorization", "Bearer " + tok}, nil, &rs); err != nil {
				return nil, err
			}
			for _, r := range rs.Repositories {
				g.app.remember(r.FullName, in.ID)
				if matches(r.FullName, query) {
					out = append(out, r.repo())
				}
			}
			if len(rs.Repositories) < 100 || len(out) >= maxRepos {
				break
			}
		}
	}
	return out, nil
}

type ghInstallation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

func (g *github) installations(ctx context.Context) ([]ghInstallation, error) {
	a, err := g.auth(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []ghInstallation
	return out, g.h.do(ctx, "GET", "/app/installations?per_page=100", a, nil, &out)
}

// Installations lists the accounts a GitHub App is installed on.
func Installations(ctx context.Context, p Provider) ([]string, error) {
	g, ok := p.(*github)
	if !ok || g.app == nil {
		return nil, nil
	}
	ins, err := g.installations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ins))
	for _, in := range ins {
		out = append(out, in.Account.Login)
	}
	return out, nil
}

func (g *github) installationToken(ctx context.Context, fullName string) (string, error) {
	id, ok := g.app.installationOf(fullName)
	if !ok {
		jwt, err := g.app.jwt(time.Now())
		if err != nil {
			return "", err
		}
		var in ghInstallation
		if err := g.h.do(ctx, "GET", "/repos/"+pathEscapeRepo(fullName)+"/installation", [2]string{"Authorization", "Bearer " + jwt}, nil, &in); err != nil {
			if IsNotFound(err) {
				return "", fmt.Errorf("the GitHub App is not installed on %s: install it on that account or repository", fullName)
			}
			return "", err
		}
		id = in.ID
		g.app.remember(fullName, id)
	}
	return g.app.token(ctx, g.h, id)
}

func (g *github) Repo(ctx context.Context, fullName string) (Repo, error) {
	a, err := g.auth(ctx, fullName)
	if err != nil {
		return Repo{}, err
	}
	var r ghRepo
	err = g.h.do(ctx, "GET", "/repos/"+pathEscapeRepo(fullName), a, nil, &r)
	return r.repo(), err
}

func (g *github) Branches(ctx context.Context, fullName string) ([]string, error) {
	a, err := g.auth(ctx, fullName)
	if err != nil {
		return nil, err
	}
	var bs []struct {
		Name string `json:"name"`
	}
	if err := g.h.do(ctx, "GET", "/repos/"+pathEscapeRepo(fullName)+"/branches?per_page=100", a, nil, &bs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out, nil
}

func (g *github) Token(ctx context.Context, fullName string) (string, error) {
	if g.app == nil {
		return g.token, nil
	}
	return g.installationToken(ctx, fullName)
}

func (g *github) CreateHook(ctx context.Context, fullName, hookURL, secret string) (string, error) {
	if g.app != nil {
		return "", nil // the app's own webhook delivers every installed repository
	}
	a, _ := g.auth(ctx, fullName)
	var out struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{"name": "web", "active": true, "events": []string{"push"},
		"config": map[string]string{"url": hookURL, "content_type": "json", "secret": secret, "insecure_ssl": "0"}}
	if err := g.h.do(ctx, "POST", "/repos/"+pathEscapeRepo(fullName)+"/hooks", a, body, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func (g *github) DeleteHook(ctx context.Context, fullName, id string) error {
	if g.app != nil || id == "" {
		return nil
	}
	a, _ := g.auth(ctx, fullName)
	err := g.h.do(ctx, "DELETE", "/repos/"+pathEscapeRepo(fullName)+"/hooks/"+id, a, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (g *github) SetStatus(ctx context.Context, fullName, sha string, s Status) error {
	a, err := g.auth(ctx, fullName)
	if err != nil {
		return err
	}
	state := s.State
	if state == StateRunning {
		state = "pending"
	}
	body := map[string]string{"state": state, "context": s.Context, "description": truncate(s.Description, 140)}
	if s.TargetURL != "" {
		body["target_url"] = s.TargetURL
	}
	return g.h.do(ctx, "POST", "/repos/"+pathEscapeRepo(fullName)+"/statuses/"+sha, a, body, nil)
}

// ghApp signs app JWTs and caches installation tokens.
type ghApp struct {
	id  int64
	key *rsa.PrivateKey

	mu     sync.Mutex
	tokens map[int64]cachedToken
	repos  map[string]int64 // lower-case full name -> installation
}

type cachedToken struct {
	token   string
	expires time.Time
}

func newGHApp(id int64, keyPEM string) (*ghApp, error) {
	if id == 0 {
		return nil, errors.New("GitHub App ID missing")
	}
	key, err := parseRSAKey(keyPEM)
	if err != nil {
		return nil, err
	}
	return &ghApp{id: id, key: key, tokens: map[int64]cachedToken{}, repos: map[string]int64{}}, nil
}

func parseRSAKey(keyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("the GitHub App private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse the GitHub App private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the GitHub App private key is not RSA")
	}
	return rk, nil
}

// jwt is the app's own credential (RS256, valid for 9 minutes; issued a
// minute in the past against clock drift).
func (a *ghApp) jwt(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(a.id, 10)})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (a *ghApp) remember(fullName string, id int64) {
	a.mu.Lock()
	a.repos[strings.ToLower(fullName)] = id
	a.mu.Unlock()
}

func (a *ghApp) installationOf(fullName string) (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.repos[strings.ToLower(fullName)]
	return id, ok
}

// token returns an installation token valid for at least 10 more minutes.
func (a *ghApp) token(ctx context.Context, h *httpc, installation int64) (string, error) {
	a.mu.Lock()
	t, ok := a.tokens[installation]
	a.mu.Unlock()
	if ok && time.Until(t.expires) > 10*time.Minute {
		return t.token, nil
	}
	jwt, err := a.jwt(time.Now())
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := h.do(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", installation), [2]string{"Authorization", "Bearer " + jwt}, nil, &out); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.tokens[installation] = cachedToken{out.Token, out.ExpiresAt}
	a.mu.Unlock()
	return out.Token, nil
}

// ManifestConversion is what GitHub returns for a created app.
type ManifestConversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// ConvertManifest exchanges the code GitHub redirects back with for the
// new app's credentials (POST /app-manifests/{code}/conversions).
func ConvertManifest(ctx context.Context, apiURL, code string) (ManifestConversion, error) {
	h := &httpc{base: strings.TrimRight(apiURL, "/"), client: &httpClient, github: true}
	var out ManifestConversion
	err := h.do(ctx, "POST", "/app-manifests/"+url.PathEscape(code)+"/conversions", [2]string{}, nil, &out)
	return out, err
}
