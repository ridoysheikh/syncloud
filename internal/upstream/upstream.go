// Package upstream stores credentials for third-party registries (§5.9).
// Nodes pull public images directly from their registries; with a stored
// credential (e.g. a Docker Hub account against rate limits, or a private
// GHCR image) the controller passes it along with the pull.
package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// DockerHub is the canonical host for Docker Hub images.
const DockerHub = "docker.io"

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

type Manager struct {
	st  *store.Store
	box *secrets.Box
	now func() time.Time
}

func New(st *store.Store, box *secrets.Box) *Manager {
	return &Manager{st: st, box: box, now: time.Now}
}

var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

// NormalizeHost maps Docker Hub's aliases to docker.io and lowercases.
func NormalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	h = strings.TrimSuffix(h, "/")
	switch h {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com", "hub.docker.com":
		return DockerHub
	}
	return h
}

// ImageHost returns the registry host of an image reference, the way
// Docker resolves it: "nginx" and "acme/app" are on Docker Hub.
func ImageHost(image string) string {
	first, _, found := strings.Cut(image, "/")
	if !found || (!strings.ContainsAny(first, ".:") && first != "localhost") {
		return DockerHub
	}
	return NormalizeHost(first)
}

func (m *Manager) Put(ctx context.Context, host, username, password string) (store.UpstreamCredential, error) {
	host = NormalizeHost(host)
	switch {
	case !hostRE.MatchString(host):
		return store.UpstreamCredential{}, ErrInvalid{errors.New("host must be a registry host such as docker.io, ghcr.io or registry.example.com:5000")}
	case strings.TrimSpace(username) == "" || password == "":
		return store.UpstreamCredential{}, ErrInvalid{errors.New("username and password are required")}
	}
	now := m.now().UTC().Truncate(time.Second)
	c := store.UpstreamCredential{ID: auth.NewID("upc_"), Host: host, Username: strings.TrimSpace(username),
		PasswordEnc: m.box.Seal([]byte(password), []byte("upstream:"+host)), CreatedAt: now, UpdatedAt: now}
	if err := m.st.PutUpstreamCredential(ctx, c); err != nil {
		return store.UpstreamCredential{}, err
	}
	for _, x := range m.list(ctx) {
		if x.Host == host {
			return x.UpstreamCredential, nil
		}
	}
	return c, nil
}

type cred struct {
	store.UpstreamCredential
	password string
}

func (m *Manager) list(ctx context.Context) []cred {
	cs, err := m.st.ListUpstreamCredentials(ctx)
	if err != nil {
		return nil
	}
	out := make([]cred, 0, len(cs))
	for _, c := range cs {
		pw, err := m.box.Open(c.PasswordEnc, []byte("upstream:"+c.Host))
		if err != nil {
			continue
		}
		out = append(out, cred{c, string(pw)})
	}
	return out
}

// RegistryAuth is the X-Registry-Auth header value for pulling image, or ""
// when no credential matches its registry.
func (m *Manager) RegistryAuth(ctx context.Context, image string) string {
	host := ImageHost(image)
	for _, c := range m.list(ctx) {
		if c.Host == host {
			server := host
			if host == DockerHub {
				server = "https://index.docker.io/v1/"
			}
			b, _ := json.Marshal(map[string]string{"username": c.Username, "password": c.password, "serveraddress": server})
			return base64.URLEncoding.EncodeToString(b)
		}
	}
	return ""
}

// DockerConfigAuths are the "auths" of a Docker config.json holding every
// credential (for BuildKit pulling private FROM images).
func (m *Manager) DockerConfigAuths(ctx context.Context) map[string]any {
	out := map[string]any{}
	for _, c := range m.list(ctx) {
		key := c.Host
		if key == DockerHub {
			key = "https://index.docker.io/v1/"
		}
		out[key] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.password))}
	}
	return out
}
