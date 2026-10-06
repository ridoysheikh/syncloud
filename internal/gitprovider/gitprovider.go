// Package gitprovider talks to Git hosting APIs (§5.8): GitHub (as a GitHub
// App or with a token), GitLab and Gitea/Forgejo. It lists repositories and
// branches, mints tokens for cloning, creates push webhooks and reports
// commit statuses, so connecting a repository needs no manual setup.
package gitprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Kinds of connection.
const (
	KindGitHubApp = "github-app"
	KindGitHub    = "github"
	KindGitLab    = "gitlab"
	KindGitea     = "gitea"
)

// Repo is a repository the connection can read.
type Repo struct {
	FullName      string `json:"fullName"` // owner/name (GitLab: group/subgroup/name)
	CloneURL      string `json:"cloneUrl"`
	WebURL        string `json:"webUrl"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
}

// Commit states for SetStatus.
const (
	StatePending = "pending" // queued
	StateRunning = "running" // building or deploying
	StateSuccess = "success"
	StateFailure = "failure"
)

// Status is a commit status (a check next to the commit on the Git host).
type Status struct {
	State       string
	Context     string // e.g. syncloud/shop/production/web
	Description string
	TargetURL   string
}

// Provider is one connection's API.
type Provider interface {
	// Account verifies the credentials and returns who they act as.
	Account(ctx context.Context) (string, error)
	// Repos lists repositories whose name contains query ("" = all, newest
	// activity first, capped).
	Repos(ctx context.Context, query string) ([]Repo, error)
	Repo(ctx context.Context, fullName string) (Repo, error)
	Branches(ctx context.Context, fullName string) ([]string, error)
	// Token is a password for git over HTTPS (short-lived for GitHub Apps).
	Token(ctx context.Context, fullName string) (string, error)
	// CreateHook adds a push webhook and returns its ID; "" with no error
	// means the provider delivers pushes another way (a GitHub App's own
	// webhook).
	CreateHook(ctx context.Context, fullName, hookURL, secret string) (string, error)
	DeleteHook(ctx context.Context, fullName, id string) error
	SetStatus(ctx context.Context, fullName, sha string, s Status) error
}

// Secrets are the sealed part of a connection.
type Secrets struct {
	Token         string `json:"token,omitempty"`
	PrivateKey    string `json:"privateKey,omitempty"` // GitHub App (PEM)
	ClientID      string `json:"clientId,omitempty"`
	ClientSecret  string `json:"clientSecret,omitempty"`
	WebhookSecret string `json:"webhookSecret,omitempty"`
}

// Conn is what New needs to build a Provider.
type Conn struct {
	Kind    string
	APIURL  string
	AppID   int64
	Secrets Secrets
}

// New returns the provider for a connection.
func New(c Conn) (Provider, error) {
	h := &httpc{base: strings.TrimRight(c.APIURL, "/"), client: &httpClient, github: c.Kind == KindGitHub || c.Kind == KindGitHubApp}
	switch c.Kind {
	case KindGitHub:
		return &github{h: h, token: c.Secrets.Token}, nil
	case KindGitHubApp:
		app, err := newGHApp(c.AppID, c.Secrets.PrivateKey)
		if err != nil {
			return nil, err
		}
		return &github{h: h, app: app}, nil
	case KindGitLab:
		h.base += "/api/v4"
		return &gitlab{h: h, token: c.Secrets.Token}, nil
	case KindGitea:
		h.base += "/api/v1"
		return &gitea{h: h, token: c.Secrets.Token}, nil
	}
	return nil, fmt.Errorf("unknown connection kind %q", c.Kind)
}

// DefaultAPIURL is the API base of a hosted service ("" when it must be given).
func DefaultAPIURL(kind string) string {
	switch kind {
	case KindGitHub, KindGitHubApp:
		return "https://api.github.com"
	case KindGitLab:
		return "https://gitlab.com"
	}
	return ""
}

// GitHubAPIFor is the API URL of github.com or a GitHub Enterprise server.
func GitHubAPIFor(webURL string) string {
	webURL = strings.TrimRight(webURL, "/")
	if webURL == "" || webURL == "https://github.com" {
		return "https://api.github.com"
	}
	return webURL + "/api/v3"
}

// WebURLFor is the browser URL of an API URL.
func WebURLFor(kind, apiURL string) string {
	apiURL = strings.TrimRight(apiURL, "/")
	if apiURL == "https://api.github.com" {
		return "https://github.com"
	}
	return strings.TrimSuffix(apiURL, "/api/v3")
}

// APIError is a non-2xx answer.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// IsNotFound reports a 404 from the provider.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

var httpClient = http.Client{Timeout: 20 * time.Second}

type httpc struct {
	base   string
	client *http.Client
	github bool // send GitHub's media type and API version
}

// do sends a JSON request; auth is a full header line value pair.
func (h *httpc) do(ctx context.Context, method, path string, auth [2]string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		u = h.base + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "syncloud")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth[0] != "" {
		req.Header.Set(auth[0], auth[1])
	}
	if h.github {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message any    `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error
		if e.Message != nil {
			msg = strings.TrimSpace(fmt.Sprint(e.Message))
		}
		if msg == "" && resp.StatusCode != http.StatusNotFound {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func pathEscapeRepo(full string) string {
	parts := strings.Split(full, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// ValidRepoName accepts owner/name paths (GitLab may nest groups).
func ValidRepoName(full string) bool {
	parts := strings.Split(full, "/")
	if len(parts) < 2 || len(parts) > 10 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, " ?#%\\") {
			return false
		}
	}
	return true
}

func matches(name, query string) bool {
	return query == "" || strings.Contains(strings.ToLower(name), strings.ToLower(query))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

const maxRepos = 300
