// Package ingress decides which Traefik replicas serve each project's public
// traffic (D22, Phase 23): by default the controller and edge pools, or the
// nodes a project chose as its load balancers. When a project's ingress
// changes, the previous one keeps serving each custom domain until that
// domain's DNS points at the new one.
package ingress

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/domain"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// MaxNodes is the most ingress nodes a project may have.
const MaxNodes = 16

// Target is who serves a route: the default ingress (controller and edge
// pools) and/or ingress nodes, by name.
type Target struct {
	Default bool     `json:"default"`
	Nodes   []string `json:"nodes"`
}

// DefaultTarget is the controller and edge pools.
var DefaultTarget = Target{Default: true}

// Served reports whether a replica serves the target: the controller and
// edge-pool nodes serve the default; any node serves targets naming it.
func (t Target) Served(node string, platform bool) bool {
	return t.Default && platform || slices.Contains(t.Nodes, node)
}

func (t Target) Union(o Target) Target {
	out := Target{Default: t.Default || o.Default, Nodes: slices.Clone(t.Nodes)}
	for _, n := range o.Nodes {
		if !slices.Contains(out.Nodes, n) {
			out.Nodes = append(out.Nodes, n)
		}
	}
	return out
}

func prevTarget(mv *store.IngressMove) Target {
	t := targetOf(mv.From)
	t.Default = t.Default || mv.FromController
	return t
}

func targetOf(nodes []string) Target {
	if len(nodes) == 0 {
		return DefaultTarget
	}
	return Target{Nodes: slices.Clone(nodes)}
}

// Manager keeps the projects' ingress settings in memory: routes ask for
// them every time Traefik polls.
type Manager struct {
	st  *store.Store
	log *slog.Logger
	// ControllerAddr is the controller's public IP (the default ingress).
	ControllerAddr func(ctx context.Context) string
	// Resolve looks a host up (nil: the system resolver).
	Resolve func(ctx context.Context, host string) ([]string, error)
	// OnChange runs after any project's ingress changed (routes, replicas
	// and firewalls follow).
	OnChange func()
	// NodeAddrs returns each node's public address by name (nil: their
	// WireGuard endpoints).
	NodeAddrs func(ctx context.Context) (map[string]string, error)

	mu       sync.RWMutex
	projects map[string]store.Project // by name
	addrs    map[string]string        // node name -> public address
}

func New(st *store.Store, log *slog.Logger) *Manager {
	return &Manager{st: st, log: log, projects: map[string]store.Project{}, addrs: map[string]string{}}
}

// Load reads every project's ingress and the nodes' public addresses.
func (m *Manager) Load(ctx context.Context) error {
	ps, err := m.st.ListProjects(ctx)
	if err != nil {
		return err
	}
	addrs, err := m.nodeAddrs(ctx)
	if err != nil {
		return err
	}
	projects := map[string]store.Project{}
	for _, p := range ps {
		projects[p.Name] = p
	}
	m.mu.Lock()
	m.projects, m.addrs = projects, addrs
	m.mu.Unlock()
	return nil
}

func (m *Manager) nodeAddrs(ctx context.Context) (map[string]string, error) {
	if m.NodeAddrs != nil {
		return m.NodeAddrs(ctx)
	}
	nets, err := m.st.ListNodeNetworks(ctx)
	if err != nil {
		return nil, err
	}
	addrs := map[string]string{}
	for _, n := range nets {
		if h, _, err := net.SplitHostPort(n.Endpoint); err == nil {
			addrs[n.NodeName] = h
		}
	}
	return addrs, nil
}

func (m *Manager) project(name string) (store.Project, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.projects[name]
	return p, ok
}

// Nodes are a project's ingress nodes (empty = the default ingress).
func (m *Manager) Nodes(project string) []string {
	p, _ := m.project(project)
	return p.IngressNodes
}

// Move is a project's ingress change in progress (nil when settled).
func (m *Manager) Move(project string) *store.IngressMove {
	p, _ := m.project(project)
	return p.IngressMove
}

// Current is who serves a project's generated addresses and public ports.
func (m *Manager) Current(project string) Target {
	return targetOf(m.Nodes(project))
}

// Previous is who served the project before its move (ok = moving).
func (m *Manager) Previous(project string) (Target, bool) {
	mv := m.Move(project)
	if mv == nil {
		return Target{}, false
	}
	return prevTarget(mv), true
}

// Domain is who serves one of a project's custom domains: the current
// ingress, plus the previous one until the domain's DNS has moved.
func (m *Manager) Domain(project, host string) Target {
	p, _ := m.project(project)
	t := targetOf(p.IngressNodes)
	if mv := p.IngressMove; mv != nil && !slices.Contains(mv.Moved, host) {
		t = t.Union(prevTarget(mv))
	}
	return t
}

// Ports is who opens a project's public ports: the current ingress, plus
// the previous one while it moves.
func (m *Manager) Ports(project string) Target {
	t := m.Current(project)
	if prev, ok := m.Previous(project); ok {
		t = t.Union(prev)
	}
	return t
}

// Base is the base domain of a project's generated addresses: the cluster's,
// or the first ingress node's sslip.io name.
func (m *Manager) Base(project, base string) string {
	return m.baseOf(m.Nodes(project), base)
}

// PreviousBase is the base the project's generated addresses had before
// its move (ok = moving).
func (m *Manager) PreviousBase(project, base string) (string, bool) {
	mv := m.Move(project)
	if mv == nil {
		return "", false
	}
	return m.baseOf(mv.From, base), true
}

func (m *Manager) baseOf(nodes []string, base string) string {
	if len(nodes) == 0 || base == "" {
		return base // no base domain yet: plain HTTP on localhost names
	}
	m.mu.RLock()
	ip := m.addrs[nodes[0]]
	m.mu.RUnlock()
	if a, err := netip.ParseAddr(ip); err != nil || !a.Is4() {
		return base // no IPv4 address known yet
	}
	return domain.Wildcard(ip, "sslip.io")
}

// Addr is a node's public address ("" when unknown).
func (m *Manager) Addr(node string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.addrs[node]
}

// Expected are the addresses a project's custom domains must resolve to:
// every ingress node's, or the controller's.
func (m *Manager) Expected(ctx context.Context, project string) []string {
	return m.expected(ctx, m.Nodes(project))
}

func (m *Manager) expected(ctx context.Context, nodes []string) []string {
	var out []string
	if len(nodes) == 0 {
		if m.ControllerAddr != nil {
			if ip := m.ControllerAddr(ctx); ip != "" {
				out = append(out, ip)
			}
		}
		return out
	}
	for _, n := range nodes {
		if ip := m.Addr(n); ip != "" && !slices.Contains(out, ip) {
			out = append(out, ip)
		}
	}
	return out
}

// Assigned maps each ingress node to the projects it serves (currently, or
// as the previous ingress of a move).
func (m *Manager) Assigned() map[string][]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string][]string{}
	add := func(n, p string) {
		if !slices.Contains(out[n], p) {
			out[n] = append(out[n], p)
		}
	}
	for name, p := range m.projects {
		for _, n := range p.IngressNodes {
			add(n, name)
		}
		if p.IngressMove != nil {
			for _, n := range p.IngressMove.From {
				add(n, name)
			}
		}
	}
	for _, ps := range out {
		sort.Strings(ps)
	}
	return out
}

// ErrNoChange means the project already has this ingress.
var ErrNoChange = errors.New("no change")

// Set changes a project's ingress nodes (nil = the default ingress). The
// previous ingress keeps serving until DNS moves; going back to it cancels
// the move.
func (m *Manager) Set(ctx context.Context, projectID string, nodes []string) (store.Project, error) {
	p, err := m.st.ProjectByID(ctx, projectID)
	if err != nil {
		return p, err
	}
	if slices.Equal(p.IngressNodes, nodes) {
		return p, ErrNoChange
	}
	move := p.IngressMove
	switch {
	case move != nil && !move.FromController && slices.Equal(move.From, nodes):
		move = nil // back where it was
	case move == nil:
		move = &store.IngressMove{From: slices.Clone(p.IngressNodes), Moved: []string{}, StartedAt: time.Now().UTC()}
	default:
		// Moving again: domains may point at the oldest ingress or the
		// half-way one, so both keep serving until DNS moves.
		prev := prevTarget(move).Union(targetOf(p.IngressNodes))
		move.From = prev.Nodes
		if move.From == nil {
			move.From = []string{}
		}
		move.FromController = prev.Default && len(prev.Nodes) > 0
		move.Moved = []string{}
	}
	if move != nil && !m.hasDomains(ctx, p.Name) {
		move = nil // nothing whose DNS has to move
	}
	if err := m.st.SetProjectIngress(ctx, p.ID, nodes, move); err != nil {
		return p, err
	}
	p.IngressNodes, p.IngressMove = nodes, move
	m.changed(ctx)
	return p, nil
}

// CutOver ends a project's move now: the previous ingress stops serving it.
func (m *Manager) CutOver(ctx context.Context, projectID string) (store.Project, error) {
	p, err := m.st.ProjectByID(ctx, projectID)
	if err != nil || p.IngressMove == nil {
		return p, err
	}
	if err := m.st.SetProjectIngress(ctx, p.ID, p.IngressNodes, nil); err != nil {
		return p, err
	}
	p.IngressMove = nil
	m.changed(ctx)
	return p, nil
}

// Using lists the projects whose ingress (current or previous) has node,
// and those for which it is the last ingress node.
func (m *Manager) Using(node string) (all, last []string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name, p := range m.projects {
		in := slices.Contains(p.IngressNodes, node)
		if in || p.IngressMove != nil && slices.Contains(p.IngressMove.From, node) {
			all = append(all, name)
		}
		if in && len(p.IngressNodes) == 1 {
			last = append(last, name)
		}
	}
	sort.Strings(all)
	sort.Strings(last)
	return all, last
}

// RemoveNode takes a node out of every project's ingress, at once (it is
// leaving the cluster). A project left with none goes back to the default.
func (m *Manager) RemoveNode(ctx context.Context, node string) error {
	ps, err := m.st.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		mv := p.IngressMove
		if mv != nil && slices.Contains(mv.From, node) {
			mv.From = slices.DeleteFunc(slices.Clone(mv.From), func(n string) bool { return n == node })
			if len(mv.From) == 0 {
				mv = nil // it was the whole previous ingress: nothing left to serve from
			}
		} else if !slices.Contains(p.IngressNodes, node) {
			continue
		}
		nodes := slices.DeleteFunc(slices.Clone(p.IngressNodes), func(n string) bool { return n == node })
		if err := m.st.SetProjectIngress(ctx, p.ID, nodes, mv); err != nil {
			return err
		}
	}
	m.changed(ctx)
	return nil
}

// RenameNode follows a node's new name.
func (m *Manager) RenameNode(ctx context.Context, from, to string) error {
	ps, err := m.st.ListProjects(ctx)
	if err != nil {
		return err
	}
	rename := func(ns []string) ([]string, bool) {
		i := slices.Index(ns, from)
		if i < 0 {
			return ns, false
		}
		ns = slices.Clone(ns)
		ns[i] = to
		return ns, true
	}
	for _, p := range ps {
		nodes, a := rename(p.IngressNodes)
		mv, b := p.IngressMove, false
		if mv != nil {
			mv.From, b = rename(mv.From)
		}
		if a || b {
			if err := m.st.SetProjectIngress(ctx, p.ID, nodes, mv); err != nil {
				return err
			}
		}
	}
	m.changed(ctx)
	return nil
}

func (m *Manager) changed(ctx context.Context) {
	if err := m.Load(ctx); err != nil {
		m.log.Error("ingress: reload", "err", err)
	}
	if m.OnChange != nil {
		m.OnChange()
	}
}

// Run reloads the settings and moves every custom domain whose DNS points
// at its project's new ingress, every minute.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.mu.RLock()
		before := m.addrs
		m.mu.RUnlock()
		if err := m.Load(ctx); err != nil {
			m.log.Error("ingress: reload", "err", err)
			continue
		}
		m.mu.RLock()
		moved := !maps.Equal(before, m.addrs) // generated addresses follow a node's new IP
		m.mu.RUnlock()
		if m.Check(ctx) || moved {
			m.changed(ctx)
		}
	}
}

// Check moves the custom domains whose DNS has moved, and settles projects
// with none left. It reports whether anything changed.
func (m *Manager) Check(ctx context.Context) bool {
	m.mu.RLock()
	var moving []store.Project
	for _, p := range m.projects {
		if p.IngressMove != nil {
			moving = append(moving, p)
		}
	}
	m.mu.RUnlock()
	if len(moving) == 0 {
		return false
	}
	hosts, err := m.domainHosts(ctx)
	if err != nil {
		m.log.Error("ingress: list domains", "err", err)
		return false
	}
	changed := false
	for _, p := range moving {
		want := m.expected(ctx, p.IngressNodes)
		mv := *p.IngressMove
		mv.Moved = slices.Clone(mv.Moved)
		pending := 0
		for _, h := range hosts[p.Name] {
			if slices.Contains(mv.Moved, h) {
				continue
			}
			if m.pointsAt(ctx, h, want) {
				mv.Moved = append(mv.Moved, h)
				m.log.Info("ingress: domain moved", "project", p.Name, "host", h)
			} else {
				pending++
			}
		}
		next := &mv
		if pending == 0 {
			next = nil
			m.log.Info("ingress: move done", "project", p.Name)
		} else if len(mv.Moved) == len(p.IngressMove.Moved) {
			continue
		}
		if err := m.st.SetProjectIngress(ctx, p.ID, p.IngressNodes, next); err != nil {
			m.log.Error("ingress: save move", "project", p.Name, "err", err)
			continue
		}
		changed = true
	}
	return changed
}

// Pending lists a moving project's custom domains whose DNS has not moved.
func (m *Manager) Pending(ctx context.Context, project string) []string {
	mv := m.Move(project)
	if mv == nil {
		return nil
	}
	hosts, err := m.domainHosts(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, h := range hosts[project] {
		if !slices.Contains(mv.Moved, h) {
			out = append(out, h)
		}
	}
	return out
}

// pointsAt reports whether host resolves only to addresses in want.
func (m *Manager) pointsAt(ctx context.Context, host string, want []string) bool {
	if len(want) == 0 {
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resolve := m.Resolve
	if resolve == nil {
		resolve = net.DefaultResolver.LookupHost
	}
	addrs, err := resolve(rctx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !slices.Contains(want, a) {
			return false
		}
	}
	return true
}

func (m *Manager) hasDomains(ctx context.Context, project string) bool {
	hosts, err := m.domainHosts(ctx)
	return err != nil || len(hosts[project]) > 0 // unsure: keep serving from both
}

// domainHosts are the custom domains (hosts) of every project.
func (m *Manager) domainHosts(ctx context.Context) (map[string][]string, error) {
	svcs, err := m.st.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	project := map[string]string{}
	for _, sv := range svcs {
		project[sv.ID] = sv.Project
	}
	ds, err := m.st.ListDomains(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, d := range ds {
		p := project[d.ServiceID]
		if p != "" && !slices.Contains(out[p], d.Host) {
			out[p] = append(out[p], d.Host)
		}
	}
	for _, hs := range out {
		sort.Strings(hs)
	}
	return out, nil
}

// SwapBase replaces a generated host's base domain ("" when it has another).
func SwapBase(host, from, to string) string {
	if !strings.HasSuffix(host, "."+from) {
		return ""
	}
	return strings.TrimSuffix(host, from) + to
}
