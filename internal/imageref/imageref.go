// Package imageref resolves an image tag to the digest it points to now,
// from any registry speaking the Docker Registry HTTP API v2 (Docker Hub,
// GHCR, ECR, GitLab, Harbor, …), anonymously or with stored credentials.
package imageref

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DockerHub is Docker Hub's name for credentials; its API is at dockerHubAPI.
const (
	DockerHub    = "docker.io"
	dockerHubAPI = "registry-1.docker.io"
)

// ErrNotFound means the registry has no such repository or tag.
var ErrNotFound = errors.New("image not found in its registry")

// Ref is a parsed image reference.
type Ref struct {
	Host   string // as credentials name it: "docker.io", "ghcr.io", "registry.example.com:5000"
	Repo   string // "library/nginx", "acme/api"
	Tag    string // "latest" when none
	Digest string // when the reference names one
}

// Parse splits an image reference the way Docker reads it: "nginx" is
// docker.io/library/nginx:latest.
func Parse(image string) (Ref, error) {
	var r Ref
	name := strings.TrimSpace(image)
	if name == "" || strings.ContainsAny(name, " \t") {
		return r, fmt.Errorf("invalid image reference %q", image)
	}
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name, r.Digest = name[:i], name[i+1:]
	}
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		name, r.Tag = name[:i], name[i+1:]
	}
	if r.Tag == "" {
		r.Tag = "latest"
	}
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Host, r.Repo = strings.ToLower(first), rest
	} else {
		r.Host, r.Repo = DockerHub, name
	}
	switch r.Host {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		r.Host = DockerHub
	}
	if r.Host == DockerHub && !strings.Contains(r.Repo, "/") {
		r.Repo = "library/" + r.Repo
	}
	if r.Repo == "" {
		return r, fmt.Errorf("invalid image reference %q", image)
	}
	return r, nil
}

// Credentials returns the username and password stored for a registry host.
type Credentials func(ctx context.Context, host string) (username, password string, ok bool)

// Resolver looks up digests.
type Resolver struct {
	HTTP  *http.Client
	Creds Credentials
	// PlainHTTP lists hosts reached over http:// (default: localhost only).
	PlainHTTP func(host string) bool
}

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// Digest returns the digest image's tag points to now ("sha256:…"): the
// one `docker pull` of the tag would get, for every platform.
func (r *Resolver) Digest(ctx context.Context, image string) (string, error) {
	ref, err := Parse(image)
	if err != nil {
		return "", err
	}
	if ref.Digest != "" {
		return ref.Digest, nil
	}
	api := ref.Host
	if api == DockerHub {
		api = dockerHubAPI
	}
	scheme := "https"
	if r.PlainHTTP != nil && r.PlainHTTP(ref.Host) || r.PlainHTTP == nil && strings.HasPrefix(ref.Host, "localhost") {
		scheme = "http"
	}
	u := scheme + "://" + api + "/v2/" + ref.Repo + "/manifests/" + url.PathEscape(ref.Tag)
	auth := ""
	for attempt := 0; ; attempt++ {
		resp, err := r.do(ctx, http.MethodHead, u, auth)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized && attempt == 0:
			if auth, err = r.authorize(ctx, ref, resp.Header.Get("WWW-Authenticate")); err != nil {
				return "", err
			}
			continue
		case resp.StatusCode == http.StatusNotFound:
			return "", fmt.Errorf("%w: %s", ErrNotFound, image)
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			return "", fmt.Errorf("%s denied access to %s (HTTP %d): add credentials under Registry › Upstreams", ref.Host, ref.Repo, resp.StatusCode)
		case resp.StatusCode >= 300:
			return "", fmt.Errorf("%s: HTTP %d for %s", ref.Host, resp.StatusCode, image)
		}
		if d := resp.Header.Get("Docker-Content-Digest"); strings.HasPrefix(d, "sha256:") {
			return d, nil
		}
		return r.digestOfBody(ctx, u, auth) // a registry that leaves the header out of HEAD
	}
}

func (r *Resolver) do(ctx context.Context, method, u, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	c := r.HTTP
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry unreachable: %w", err)
	}
	return resp, nil
}

func (r *Resolver) digestOfBody(ctx context.Context, u, auth string) (string, error) {
	resp, err := r.do(ctx, http.MethodGet, u, auth)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry: HTTP %d", resp.StatusCode)
	}
	if d := resp.Header.Get("Docker-Content-Digest"); strings.HasPrefix(d, "sha256:") {
		return d, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(resp.Body, 4<<20)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// authorize answers a 401 challenge: a bearer token from the registry's
// token service (with credentials when stored), or basic auth.
func (r *Resolver) authorize(ctx context.Context, ref Ref, challenge string) (string, error) {
	user, pass, haveCreds := "", "", false
	if r.Creds != nil {
		user, pass, haveCreds = r.Creds(ctx, ref.Host)
	}
	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "basic":
		if !haveCreds {
			return "", fmt.Errorf("%s needs a login for %s: add credentials under Registry › Upstreams", ref.Host, ref.Repo)
		}
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		req.SetBasicAuth(user, pass)
		return req.Header.Get("Authorization"), nil
	case "bearer":
	default:
		return "", fmt.Errorf("%s: unsupported authentication %q", ref.Host, scheme)
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("%s: token challenge without a realm", ref.Host)
	}
	q := url.Values{"scope": {"repository:" + ref.Repo + ":pull"}}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	sep := "?"
	if strings.Contains(realm, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+sep+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if haveCreds {
		req.SetBasicAuth(user, pass)
	}
	c := r.HTTP
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry token service unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s refused a token for %s (HTTP %d): check its credentials under Registry › Upstreams", ref.Host, ref.Repo, resp.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("%s: bad token response: %w", ref.Host, err)
	}
	t := tok.Token
	if t == "" {
		t = tok.AccessToken
	}
	if t == "" {
		return "", fmt.Errorf("%s: empty token", ref.Host)
	}
	return "Bearer " + t, nil
}

// parseChallenge reads `Bearer realm="…",service="…",scope="…"`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(v, `"`) {
			end := strings.IndexByte(v[1:], '"')
			if end < 0 {
				params[k] = v[1:]
				break
			}
			params[k], rest = v[1:end+1], v[end+2:]
		} else {
			val, after, _ := strings.Cut(v, ",")
			params[k], rest = strings.TrimSpace(val), after
		}
	}
	return scheme, params
}
