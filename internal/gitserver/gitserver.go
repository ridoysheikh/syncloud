// Package gitserver runs the optional built-in Git server (§5.8): Forgejo as
// a system task on the controller node, served at git.<base-domain>. When it
// is turned on, the controller creates its administrator, mints an API token
// and registers it as the Git connection "git", so services can build from
// its repositories with webhooks and commit statuses like any other host.
package gitserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"syncloud/internal/domain"
	"syncloud/internal/execrelay"
	"syncloud/internal/gitconn"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
	"syncloud/internal/system"
)

const (
	settingKey = "gitserver.config"
	// ConnectionName is the Git connection of the built-in server.
	ConnectionName = "git"
	// AdminUser is its administrator ("admin" is reserved by Forgejo).
	AdminUser = "syncloud"
)

// ErrInUse means services still build from the built-in server.
var ErrInUse = errors.New("services still build from the built-in Git server: disconnect them first")

// state is what is stored (sealed) in the settings table.
type state struct {
	Enabled       bool   `json:"enabled"`
	SecretKey     string `json:"secretKey"`
	InternalToken string `json:"internalToken"`
	AdminPassword string `json:"adminPassword"`
}

// Status is the server as the API shows it.
type Status struct {
	Enabled    bool   `json:"enabled"`
	State      string `json:"state"` // off | starting | provisioning | ready | failed
	URL        string `json:"url"`
	Image      string `json:"image"`
	AdminUser  string `json:"adminUser"`
	Connection string `json:"connection"`
	Problem    string `json:"problem,omitempty"`
}

type Manager struct {
	st  *store.Store
	box *secrets.Box
	log *slog.Logger
	// Conns, Exec and Sys are set before Run.
	Conns *gitconn.Manager
	Exec  *execrelay.Relay
	Sys   *system.Manager
	// Endpoints returns the dashboard's current addresses.
	Endpoints func() domain.Endpoints
	// OnChange re-renders the system tasks and certificate hosts.
	OnChange func()
	// API is where the controller reaches the server (loopback).
	API string

	mu      sync.Mutex
	s       state
	ready   bool
	problem string
	kick    chan struct{}
	http    *http.Client
}

func New(st *store.Store, box *secrets.Box, endpoints func() domain.Endpoints, log *slog.Logger) *Manager {
	return &Manager{st: st, box: box, log: log, Endpoints: endpoints, API: "http://" + system.GitServerAddr,
		kick: make(chan struct{}, 1), http: &http.Client{Timeout: 15 * time.Second}}
}

var aad = []byte("gitserver")

// Load reads the stored state (call before the system tasks start).
func (m *Manager) Load(ctx context.Context) error {
	v, ok, err := m.st.GetSetting(ctx, settingKey)
	if err != nil || !ok {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return err
	}
	b, err := m.box.Open(raw, aad)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return json.Unmarshal(b, &m.s)
}

func (m *Manager) save(ctx context.Context, s state) error {
	b, _ := json.Marshal(s)
	return m.st.SetSetting(ctx, settingKey, base64.StdEncoding.EncodeToString(m.box.Seal(b, aad)))
}

// RootURL is the server's public address.
func (m *Manager) RootURL() string {
	ep := m.Endpoints()
	if ep.BaseDomain == "" {
		return m.API + "/"
	}
	u, err := url.Parse(ep.DashboardURL)
	if err != nil {
		return m.API + "/"
	}
	host := domain.GitHost(ep.BaseDomain)
	if p := u.Port(); p != "" {
		host += ":" + p // development HTTPS port
	}
	return u.Scheme + "://" + host + "/"
}

// Host is git.<base-domain> while the server is on and a domain is set.
func (m *Manager) Host() string {
	m.mu.Lock()
	on := m.s.Enabled
	m.mu.Unlock()
	if base := m.Endpoints().BaseDomain; on && base != "" {
		return domain.GitHost(base)
	}
	return ""
}

// Config is the system task configuration (nil when off).
func (m *Manager) Config() *system.GitServerConfig {
	m.mu.Lock()
	s := m.s
	m.mu.Unlock()
	if !s.Enabled {
		return nil
	}
	return &system.GitServerConfig{RootURL: m.RootURL(), SecretKey: s.SecretKey, InternalToken: s.InternalToken}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SetEnabled turns the server on or off. Turning it off keeps its data
// volume, so turning it on again brings the repositories back.
func (m *Manager) SetEnabled(ctx context.Context, on bool) error {
	m.mu.Lock()
	s := m.s
	m.mu.Unlock()
	if s.Enabled == on {
		return nil
	}
	if !on {
		if c, err := m.Conns.Get(ctx, ConnectionName); err == nil && len(c.Services) > 0 {
			return ErrInUse
		}
	}
	if s.SecretKey == "" {
		s.SecretKey, s.InternalToken, s.AdminPassword = randomHex(32), randomHex(40), randomHex(12)
	}
	s.Enabled = on
	if err := m.save(ctx, s); err != nil {
		return err
	}
	m.mu.Lock()
	m.s, m.ready, m.problem = s, false, ""
	m.mu.Unlock()
	if !on {
		if err := m.Conns.Delete(ctx, ConnectionName); err != nil && !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("remove built-in git connection", "err", err)
		}
	}
	if m.OnChange != nil {
		m.OnChange()
	}
	m.Kick()
	m.log.Info("built-in git server", "enabled", on)
	return nil
}

// Kick re-checks the server now (after a domain change, for example).
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Credentials returns the administrator's sign-in.
func (m *Manager) Credentials() (string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return AdminUser, m.s.AdminPassword, m.s.Enabled
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	s, ready, problem := m.s, m.ready, m.problem
	m.mu.Unlock()
	st := Status{Enabled: s.Enabled, State: "off", Image: system.ImageForgejo, AdminUser: AdminUser, Connection: ConnectionName, Problem: problem}
	if !s.Enabled {
		return st
	}
	st.URL = m.RootURL()
	t, _ := m.Sys.Task(system.GitServerTaskID)
	switch {
	case ready:
		st.State = "ready"
	case t.State == "running":
		st.State = "provisioning"
	case t.State == "failed" || t.State == "exited":
		st.State, st.Problem = "failed", t.Error
	default:
		st.State = "starting"
	}
	return st
}

// Run provisions the server whenever it is on and not ready yet, and keeps
// the connection's address in step with the domain.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		m.mu.Lock()
		on, ready := m.s.Enabled, m.ready
		m.mu.Unlock()
		if on && !ready {
			err := m.provision(ctx)
			m.mu.Lock()
			if err != nil {
				m.problem = err.Error()
			} else {
				m.ready, m.problem = true, ""
			}
			m.mu.Unlock()
			if err == nil {
				m.log.Info("built-in git server ready", "url", m.RootURL())
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
			m.mu.Lock()
			m.ready = false // re-check (domain changes move the address)
			m.mu.Unlock()
		}
	}
}

// provision creates the administrator and the "git" connection once the
// server answers.
func (m *Manager) provision(ctx context.Context) error {
	if t, _ := m.Sys.Task(system.GitServerTaskID); t.State != "running" {
		return errors.New("waiting for the Git server to start")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := m.get(cctx, "/api/v1/version"); err != nil {
		return fmt.Errorf("waiting for the Git server to answer: %w", err)
	}
	_, password, _ := m.Credentials()
	out, code, err := m.run(cctx, []string{"forgejo", "admin", "user", "create", "--admin", "--username", AdminUser, "--password", password,
		"--email", AdminUser + "@syncloud.invalid", "--must-change-password=false"})
	if err != nil {
		return fmt.Errorf("create the administrator: %w", err)
	}
	if code != 0 && !strings.Contains(out, "already exists") {
		return fmt.Errorf("create the administrator: %s", strings.TrimSpace(out))
	}
	root := m.RootURL()
	if c, err := m.Conns.Get(cctx, ConnectionName); err == nil {
		if c.WebURL != strings.TrimRight(root, "/") {
			return m.Conns.SetWebURL(cctx, c.ID, root)
		}
		return nil
	}
	tok, err := m.token(cctx, password)
	if err != nil {
		return err
	}
	if _, err := m.Conns.AddBuiltin(cctx, ConnectionName, m.API, root, tok); err != nil {
		return fmt.Errorf("register the Git connection: %w", err)
	}
	return nil
}

func (m *Manager) get(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.API+path, nil)
	if err != nil {
		return err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// token mints an API token for the administrator.
func (m *Manager) token(ctx context.Context, password string) (string, error) {
	body, _ := json.Marshal(map[string]any{"name": "syncloud-" + randomHex(4),
		"scopes": []string{"write:repository", "write:user", "write:organization"}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.API+"/api/v1/users/"+AdminUser+"/tokens", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(AdminUser, password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create an API token: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		SHA1 string `json:"sha1"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.SHA1 == "" {
		return "", errors.New("create an API token: no token in the answer")
	}
	return out.SHA1, nil
}

// run executes a command in the server's container.
func (m *Manager) run(ctx context.Context, cmd []string) (string, int, error) {
	s, err := m.Exec.Open(m.Sys.NodeID(), system.GitServerTaskID, cmd, false, 0, 0)
	if err != nil {
		return "", 0, err
	}
	defer s.Close()
	var out bytes.Buffer
	for {
		select {
		case <-ctx.Done():
			return out.String(), 0, ctx.Err()
		case o, ok := <-s.Out:
			if !ok {
				return out.String(), 0, errors.New("the exec session ended unexpectedly")
			}
			if o.GetError() != "" {
				return out.String(), 0, errors.New(o.GetError())
			}
			if out.Len() < 1<<16 {
				out.Write(o.GetData())
			}
			if o.GetExited() {
				return out.String(), int(o.GetExitCode()), nil
			}
		}
	}
}
