// Package lbprotect is a project's traffic protection on its load balancers
// (Phase 25): Traefik rate limits (shared through a Valkey, with a fallback
// to per-node counters while it is down), concurrent-request and body
// limits, block and bypass lists, and a network guard on the project's own
// load-balancer nodes.
package lbprotect

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/dbs"
	"github.com/ridoysheikh/syncloud/internal/firewall"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/traefik"
)

// Spec is a project's protection.
type Spec struct {
	// Enabled off keeps the settings but applies none of them.
	Enabled   bool      `json:"enabled"`
	RateLimit RateLimit `json:"rateLimit"`
	ClientIP  ClientIP  `json:"clientIp"`
	// MaxConcurrent caps a client's requests in flight (0 = no cap).
	MaxConcurrent int `json:"maxConcurrent"`
	// MaxBodyMB caps request bodies (0 = no cap).
	MaxBodyMB int `json:"maxBodyMb"`
	// Block are client IPs/CIDRs answered with 403.
	Block  []string `json:"block"`
	Bypass Bypass   `json:"bypass"`
	// Redis shares the rate-limit counters between load balancers.
	Redis Redis `json:"redis"`
	// Guard limits connections per source on the project's own nodes.
	Guard Guard `json:"guard"`
}

// RateLimit is requests per Period and client (Average 0 = off).
type RateLimit struct {
	Average int    `json:"average"`
	Burst   int    `json:"burst"`
	Period  string `json:"period"` // 1s | 1m | 1h
}

// ClientIP says how clients are told apart.
type ClientIP struct {
	// Source: connection (its address), forwarded (X-Forwarded-For at
	// Depth, behind a proxy or CDN) or header (e.g. CF-Connecting-IP).
	Source string `json:"source"`
	Depth  int    `json:"depth"`
	Header string `json:"header"`
}

// Bypass are requests that skip the limits (never the block list).
type Bypass struct {
	IPs    []string `json:"ips"`
	Header string   `json:"header"` // header name; its value is the secret
	// HasSecret is set when a secret is stored (it is never returned).
	HasSecret bool     `json:"hasSecret"`
	Paths     []string `json:"paths"`
}

// Redis is the Valkey that keeps shared counters ("" = per node).
type Redis struct {
	DatabaseID string `json:"databaseId"`
}

// Guard is the network guard on the project's own load-balancer nodes.
type Guard struct {
	// Rate is new connections per second and source (0 = no limit).
	Rate           int `json:"rate"`
	Burst          int `json:"burst"`
	MaxConnections int `json:"maxConnections"` // concurrent per source (0 = no cap)
	// BanMinutes blocks a source over the guard on that node (0 = no ban).
	BanMinutes int `json:"banMinutes"`
}

// Default is off, with sensible values ready to switch on.
func Default() Spec {
	return Spec{
		RateLimit: RateLimit{Average: 100, Burst: 200, Period: "1s"},
		ClientIP:  ClientIP{Source: "connection"},
		Block:     []string{},
		Bypass:    Bypass{IPs: []string{}, Header: "X-Syncloud-Bypass", Paths: []string{}},
	}
}

var (
	headerRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	pathRE   = regexp.MustCompile(`^(/[A-Za-z0-9._~!$&'()*+,;=:@%-]+)+/?$`)
)

func validSources(what string, xs []string, limit int) ([]string, error) {
	if len(xs) > limit {
		return nil, fmt.Errorf("%s: at most %d entries", what, limit)
	}
	out := []string{}
	for _, s := range xs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, fmt.Errorf("%s: %q is not an IP or a CIDR", what, s)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		c := p.Masked().String()
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// Validate normalizes and checks a spec.
func (s *Spec) Validate() error {
	r := &s.RateLimit
	if r.Average < 0 || r.Average > 1_000_000 || r.Burst < 0 || r.Burst > 1_000_000 {
		return errors.New("rate limit: average and burst must be 0–1000000")
	}
	if r.Period == "" {
		r.Period = "1s"
	}
	if !slices.Contains([]string{"1s", "1m", "1h"}, r.Period) {
		return errors.New("rate limit: period must be 1s, 1m or 1h")
	}
	if r.Average > 0 && r.Burst == 0 {
		r.Burst = r.Average
	}
	c := &s.ClientIP
	switch c.Source {
	case "", "connection":
		c.Source, c.Depth, c.Header = "connection", 0, ""
	case "forwarded":
		if c.Depth < 1 || c.Depth > 10 {
			return errors.New("client IP: depth must be 1–10 (1 = the address the last proxy saw)")
		}
		c.Header = ""
	case "header":
		if !headerRE.MatchString(c.Header) {
			return errors.New("client IP: a header name like CF-Connecting-IP is required")
		}
		c.Depth = 0
	default:
		return errors.New("client IP: source must be connection, forwarded or header")
	}
	if s.MaxConcurrent < 0 || s.MaxConcurrent > 100_000 {
		return errors.New("concurrent requests: 0–100000")
	}
	if s.MaxBodyMB < 0 || s.MaxBodyMB > 10_240 {
		return errors.New("body limit: 0–10240 MB")
	}
	var err error
	if s.Block, err = validSources("block list", s.Block, 500); err != nil {
		return err
	}
	b := &s.Bypass
	if b.IPs, err = validSources("bypass IPs", b.IPs, 100); err != nil {
		return err
	}
	if b.Header == "" {
		b.Header = "X-Syncloud-Bypass"
	}
	if !headerRE.MatchString(b.Header) {
		return errors.New("bypass header: letters, digits and dashes only")
	}
	if b.Paths == nil {
		b.Paths = []string{}
	}
	if len(b.Paths) > 50 {
		return errors.New("bypass paths: at most 50")
	}
	for _, p := range b.Paths {
		if !pathRE.MatchString(p) {
			return fmt.Errorf("bypass path %q: must start with / and use URL characters", p)
		}
	}
	g := &s.Guard
	if g.Rate < 0 || g.Rate > 100_000 || g.Burst < 0 || g.Burst > 100_000 || g.MaxConnections < 0 || g.MaxConnections > 100_000 {
		return errors.New("network guard: values must be 0–100000")
	}
	if g.BanMinutes < 0 || g.BanMinutes > 7*24*60 {
		return errors.New("network guard: ban minutes must be 0–10080")
	}
	if g.BanMinutes > 0 && g.Rate == 0 && g.MaxConnections == 0 {
		return errors.New("network guard: a ban needs a rate or a connection limit")
	}
	if g.Rate > 0 && g.Burst == 0 {
		g.Burst = g.Rate * 2
	}
	return nil
}

// NewSecret makes a bypass secret.
func NewSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

type entry struct {
	projectID string
	spec      Spec
	secret    string
}

// Manager keeps every project's protection in memory: Traefik's config
// asks for it every poll.
type Manager struct {
	st  *store.Store
	box *secrets.Box
	log *slog.Logger
	// Valkey looks a Valkey up (nil: no shared counters).
	Valkey func(ctx context.Context, id string) (dbs.ValkeyTarget, error)
	// IngressNodes are a project's own load-balancer nodes (by project name).
	IngressNodes func(project string) []string
	// Assigned maps each load-balancer node to the projects it serves.
	Assigned func() map[string][]string
	// OnChange runs after a change that firewalls must follow (guards).
	OnChange func()

	mu       sync.RWMutex
	projects map[string]entry            // by project name
	redis    map[string]dbs.ValkeyTarget // by project name, refreshed every 10 s
	fallback map[string]bool             // projects whose Valkey is down
}

func New(st *store.Store, box *secrets.Box, log *slog.Logger) *Manager {
	return &Manager{st: st, box: box, log: log, projects: map[string]entry{}, redis: map[string]dbs.ValkeyTarget{}, fallback: map[string]bool{}}
}

func aad(projectID string) []byte { return []byte("project-protection:" + projectID) }

// Load reads every project's protection.
func (m *Manager) Load(ctx context.Context) error {
	ps, err := m.st.ListProjects(ctx)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, p := range ps {
		names[p.ID] = p.Name
	}
	rows, err := m.st.ListProjectProtection(ctx)
	if err != nil {
		return err
	}
	out := map[string]entry{}
	for _, r := range rows {
		name := names[r.ProjectID]
		if name == "" {
			continue
		}
		e := entry{projectID: r.ProjectID, spec: Default()}
		if err := json.Unmarshal([]byte(r.Spec), &e.spec); err != nil {
			m.log.Error("protection: parse", "project", name, "err", err)
			continue
		}
		if len(r.Secret) > 0 {
			if b, err := m.box.Open(r.Secret, aad(r.ProjectID)); err == nil {
				e.secret = string(b)
			}
		}
		e.spec.Bypass.HasSecret = e.secret != ""
		out[name] = e
	}
	m.mu.Lock()
	m.projects = out
	m.mu.Unlock()
	return nil
}

// Get returns a project's protection (the default when none is saved).
func (m *Manager) Get(project string) Spec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.projects[project]; ok {
		return e.spec
	}
	return Default()
}

// SecretUpdate changes the bypass secret: Keep it, Set a new one, or
// Clear it.
type SecretUpdate struct {
	Set   string
	Clear bool
}

// Put saves a project's protection.
func (m *Manager) Put(ctx context.Context, p store.Project, s Spec, sec SecretUpdate) (Spec, error) {
	if err := s.Validate(); err != nil {
		return s, err
	}
	if s.Redis.DatabaseID != "" {
		if m.Valkey == nil {
			return s, errors.New("managed databases are not enabled")
		}
		t, err := m.Valkey(ctx, s.Redis.DatabaseID)
		if err != nil {
			return s, fmt.Errorf("redis: %w", err)
		}
		if t.Project != p.Name {
			return s, errors.New("redis: pick a Valkey of this project")
		}
	}
	m.mu.RLock()
	old := m.projects[p.Name]
	m.mu.RUnlock()
	secret := old.secret
	switch {
	case sec.Clear:
		secret = ""
	case sec.Set != "":
		if !traefik.SecretSafe.MatchString(sec.Set) {
			return s, errors.New("bypass secret: 16–128 letters, digits, '.', '_', '~' or '-'")
		}
		secret = sec.Set
	}
	s.Bypass.HasSecret = secret != ""
	b, _ := json.Marshal(s)
	row := store.ProjectProtection{ProjectID: p.ID, Spec: string(b), UpdatedAt: time.Now().UTC()}
	if secret != "" {
		row.Secret = m.box.Seal([]byte(secret), aad(p.ID))
	}
	if err := m.st.PutProjectProtection(ctx, row); err != nil {
		return s, err
	}
	m.mu.Lock()
	m.projects[p.Name] = entry{projectID: p.ID, spec: s, secret: secret}
	m.mu.Unlock()
	m.refreshRedis(ctx)
	if m.OnChange != nil {
		m.OnChange()
	}
	return s, nil
}

func mwName(projectID, kind string) string {
	return "lb-" + strings.TrimPrefix(projectID, "prj_") + "-" + kind
}

func (s Spec) criterion() *traefik.SourceCriterion {
	switch s.ClientIP.Source {
	case "forwarded":
		return &traefik.SourceCriterion{IPStrategy: &traefik.IPStrategy{Depth: s.ClientIP.Depth}}
	case "header":
		return &traefik.SourceCriterion{RequestHeaderName: s.ClientIP.Header}
	}
	return nil // the connection's address
}

// Middlewares are every project's protection middlewares.
func (m *Manager) Middlewares() map[string]traefik.Middleware {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]traefik.Middleware{}
	for name, e := range m.projects {
		s := e.spec
		if !s.Enabled {
			continue
		}
		if s.RateLimit.Average > 0 {
			rl := &traefik.RateLimit{Average: s.RateLimit.Average, Burst: s.RateLimit.Burst, Period: s.RateLimit.Period, SourceCriterion: s.criterion()}
			if t, ok := m.redis[name]; ok && !m.fallback[name] {
				rl.Redis = &traefik.RateLimitRedis{Endpoints: []string{t.Endpoint}, Password: t.Password}
			}
			out[mwName(e.projectID, "rate")] = traefik.Middleware{RateLimit: rl}
		}
		if s.MaxConcurrent > 0 {
			out[mwName(e.projectID, "inflight")] = traefik.Middleware{InFlightReq: &traefik.InFlightReq{Amount: s.MaxConcurrent, SourceCriterion: s.criterion()}}
		}
		if s.MaxBodyMB > 0 {
			out[mwName(e.projectID, "body")] = traefik.Middleware{Buffering: &traefik.Buffering{MaxRequestBodyBytes: int64(s.MaxBodyMB) << 20}}
		}
	}
	return out
}

// Protection is what a project's routes carry (nil: none).
func (m *Manager) Protection(project string) *traefik.Protection {
	m.mu.RLock()
	e, ok := m.projects[project]
	m.mu.RUnlock()
	if !ok || !e.spec.Enabled {
		return nil
	}
	s := e.spec
	p := &traefik.Protection{Deny: s.Block, BypassIPs: s.Bypass.IPs, BypassPaths: s.Bypass.Paths}
	if e.secret != "" {
		p.BypassHeader, p.BypassSecret = s.Bypass.Header, e.secret
	}
	if s.MaxBodyMB > 0 {
		p.Always = append(p.Always, mwName(e.projectID, "body"))
	}
	if s.RateLimit.Average > 0 {
		p.Limits = append(p.Limits, mwName(e.projectID, "rate"))
	}
	if s.MaxConcurrent > 0 {
		p.Limits = append(p.Limits, mwName(e.projectID, "inflight"))
	}
	if len(p.Always) == 0 && len(p.Limits) == 0 && len(p.Deny) == 0 {
		return nil
	}
	return p
}

// guardOf is a project's network guard when it applies (on, with limits).
func (m *Manager) guardOf(project string) (Guard, []string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.projects[project]
	if !ok || !e.spec.Enabled {
		return Guard{}, nil, false
	}
	g := e.spec.Guard
	return g, e.spec.Bypass.IPs, g.Rate > 0 || g.MaxConnections > 0
}

// node is the merged guard of a load-balancer node: the most permissive of
// the projects it serves, so no project is limited by another's settings
// (none when a project on it has no guard).
func (m *Manager) node(name string) (Guard, []string, bool) {
	if m.Assigned == nil {
		return Guard{}, nil, false
	}
	projects := m.Assigned()[name]
	if len(projects) == 0 {
		return Guard{}, nil, false
	}
	var out Guard
	var exempt []string
	for i, p := range projects {
		g, ex, ok := m.guardOf(p)
		if !ok {
			return Guard{}, nil, false
		}
		exempt = append(exempt, ex...)
		if i == 0 {
			out = g
			continue
		}
		out.Rate, out.Burst = loosest(out.Rate, g.Rate), loosest(out.Burst, g.Burst)
		out.MaxConnections = loosest(out.MaxConnections, g.MaxConnections)
		out.BanMinutes = min(out.BanMinutes, g.BanMinutes)
	}
	slices.Sort(exempt)
	return out, slices.Compact(exempt), true
}

// loosest of two limits where 0 means none.
func loosest(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	return max(a, b)
}

// Guards are the network guards of a node's 80/443 (its projects').
func (m *Manager) Guards(node string) []*agentv1.FirewallGuard {
	g, exempt, ok := m.node(node)
	if !ok {
		return nil
	}
	return []*agentv1.FirewallGuard{{Id: "builtin:lb-guard", Protocol: "tcp", Ports: "80,443", Rate: uint32(g.Rate), Unit: "second",
		Burst: uint32(max(g.Burst, 1)), MaxConnections: uint32(g.MaxConnections), Exempt: exempt}}
}

// AutoBan is the ban of a source over a node's guard on 80/443.
func (m *Manager) AutoBan(node string, port uint32) (time.Duration, string, bool) {
	if port != 80 && port != 443 {
		return 0, "", false
	}
	g, _, ok := m.node(node)
	if !ok || g.BanMinutes == 0 {
		return 0, "", false
	}
	return time.Duration(g.BanMinutes) * time.Minute, "load balancer: over the network guard's limit", true
}

// RedisState is a project's shared-counter state.
type RedisState struct {
	Name     string `json:"name,omitempty"`
	Health   string `json:"health,omitempty"`
	Active   bool   `json:"active"`   // counters are shared now
	Fallback bool   `json:"fallback"` // its Valkey is down: per-node counters
}

// Redis returns a project's shared-counter state.
func (m *Manager) Redis(project string) RedisState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.redis[project]
	if !ok {
		return RedisState{}
	}
	return RedisState{Name: t.Name, Health: t.Health, Active: !m.fallback[project], Fallback: m.fallback[project]}
}

// Run refreshes the Valkeys' health every 10 seconds, switching projects to
// per-node counters while theirs is down (Traefik answers 500 when it cannot
// reach Redis) and back once it is healthy.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		m.refreshRedis(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Manager) refreshRedis(ctx context.Context) {
	if m.Valkey == nil {
		return
	}
	m.mu.RLock()
	want := map[string]string{}
	for name, e := range m.projects {
		if e.spec.Enabled && e.spec.RateLimit.Average > 0 && e.spec.Redis.DatabaseID != "" {
			want[name] = e.spec.Redis.DatabaseID
		}
	}
	m.mu.RUnlock()
	redis, fallback := map[string]dbs.ValkeyTarget{}, map[string]bool{}
	for name, id := range want {
		t, err := m.Valkey(ctx, id)
		if err != nil {
			t = dbs.ValkeyTarget{ID: id, Health: "down"}
		}
		redis[name] = t
		fallback[name] = !t.Healthy()
	}
	m.mu.Lock()
	for name := range fallback {
		if fallback[name] != m.fallback[name] {
			if fallback[name] {
				m.log.Warn("protection: the rate-limit Valkey is down; load balancers count per node", "project", name, "valkey", redis[name].Name)
			} else {
				m.log.Info("protection: the rate-limit Valkey is back; counters are shared", "project", name)
			}
		}
	}
	m.redis, m.fallback = redis, fallback
	m.mu.Unlock()
}

// Components report each project whose shared counters fell back, for the
// system component alert.
func (m *Manager) Components() []ComponentState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ComponentState
	for name, t := range m.redis {
		c := ComponentState{ID: "lb-redis:" + name, Name: "rate-limit Valkey of " + name, State: "running"}
		if m.fallback[name] {
			c.State, c.Error = "down", fmt.Sprintf("Valkey %s is %s: each load balancer counts on its own until it is back", t.Name, t.Health)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ComponentState mirrors alerts.ComponentState (this package does not
// depend on alerts).
type ComponentState struct{ ID, Name, State, Error string }

// ValidFirewallGuard reports whether a guard renders (for tests).
func ValidFirewallGuard(g *agentv1.FirewallGuard) error { return firewall.ValidateGuard(g) }
