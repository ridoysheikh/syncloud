package dbs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/secgroup"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Network is a database's access list and public endpoint (Phase 12e).
type Network = store.DatabaseNetwork

const (
	maxAccess = 50
	maxAllow  = 50
	// standaloneZone holds standalone databases' internal names.
	standaloneZone = "db." + Zone
)

// reserved names collide with API paths or endpoint names.
func reservedName(name string) error {
	if name == "engines" || name == "new" || strings.HasSuffix(name, "-ro") {
		return errors.New(`the names "engines" and "new", and names ending in "-ro", are reserved`)
	}
	return nil
}

// NormalizeNetwork validates n and canonicalizes its peers and CIDRs.
// exists reports whether a referenced project, environment, service or
// group exists.
func NormalizeNetwork(n *Network, exists func(p secgroup.Peer) bool) error {
	if n.Access == nil {
		n.Access = []string{}
	}
	if len(n.Access) > maxAccess {
		return fmt.Errorf("at most %d access entries", maxAccess)
	}
	seen := map[string]bool{}
	access := []string{}
	for _, a := range n.Access {
		if strings.TrimSpace(a) == "" {
			continue
		}
		p, canon, err := secgroup.ParsePeer(a, "")
		if err != nil {
			return err
		}
		switch p.Kind {
		case secgroup.KindSelf, secgroup.KindSelfEnv, secgroup.KindSelfProject:
			return fmt.Errorf("access %q: name the project, environment or service", a)
		case secgroup.KindProject, secgroup.KindEnvironment, secgroup.KindService, secgroup.KindGroup:
			if p.Project == "" {
				return fmt.Errorf("access %q: include the project, e.g. environment:shop/production", a)
			}
			if exists != nil && !exists(p) {
				return fmt.Errorf("access %q: no such %s", a, p.Kind)
			}
		}
		if !seen[canon] {
			seen[canon] = true
			access = append(access, canon)
		}
	}
	n.Access = access
	if len(n.Public.Allow) > maxAllow {
		return fmt.Errorf("at most %d allowed address ranges", maxAllow)
	}
	allow := []string{}
	seen = map[string]bool{}
	for _, c := range n.Public.Allow {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			a, aerr := netip.ParseAddr(c)
			if aerr != nil {
				return fmt.Errorf("allowed address %q: use an IP address or CIDR, e.g. 203.0.113.0/24", c)
			}
			pfx = netip.PrefixFrom(a, a.BitLen())
		}
		s := pfx.Masked().String()
		if !seen[s] {
			seen[s] = true
			allow = append(allow, s)
		}
	}
	if n.Public.Enabled && len(allow) == 0 {
		allow = []string{"0.0.0.0/0", "::/0"}
	}
	n.Public.Allow = allow
	return nil
}

// ── names ───────────────────────────────────────────────────────────────────

// Host is the database's internal read-write name: in its environment's
// zone for a project database, under db.syncloud.internal when standalone.
func Host(d store.Database) string {
	if d.Standalone() {
		return d.Name + "." + standaloneZone
	}
	return fmt.Sprintf("%s.%s.%s.%s", d.Name, d.Environment, d.Project, Zone)
}

// ReadHost is the internal read-only name.
func ReadHost(d store.Database) string {
	if d.Standalone() {
		return d.Name + "-ro." + standaloneZone
	}
	return fmt.Sprintf("%s-ro.%s.%s.%s", d.Name, d.Environment, d.Project, Zone)
}

// PublicHost is the public read-write name under the base domain.
func PublicHost(d store.Database, base string) string { return d.Name + ".db." + base }

// PublicReadHost is the public read-only name.
func PublicReadHost(d store.Database, base string) string { return d.Name + "-ro.db." + base }

// ── public endpoints ────────────────────────────────────────────────────────

// PublicRoute is one endpoint Traefik serves for a database: TLS by SNI
// on the engine's shared entrypoint (Host set), or any TLS or plain
// connection on a dedicated port (Port set, Host empty).
type PublicRoute struct {
	Name       string // router/service name
	Entrypoint string
	Host       string
	Port       int
	Plain      bool     // the plain (non-TLS) router of a dedicated port
	Servers    []string // host:port of members
	Allow      []string
}

// DefaultPublicPorts is where dedicated public database ports come from.
var DefaultPublicPorts = [2]int{21000, 21999}

// PortEntrypoint is the Traefik entrypoint of a dedicated port.
func PortEntrypoint(port int) string { return fmt.Sprintf("db-%d", port) }

// PublicPort is a dedicated port to open, with its allowed sources.
type PublicPort struct {
	Port  int
	Allow []string
}

// PublicRoutes are the TCP routes of every public database. The SNI routes
// need a base domain; the dedicated ports work without one. The read-write
// routes target the current primary.
func (m *Manager) PublicRoutes(ctx context.Context, base string) []PublicRoute {
	dbs, err := m.st.ListDatabases(ctx)
	if err != nil {
		return nil
	}
	var out []PublicRoute
	for _, d := range dbs {
		n := d.ParseNetwork()
		if !n.Public.Enabled || d.Deleting {
			continue
		}
		eng, _ := EngineByName(d.Engine)
		primary, replicas := m.endpoints(ctx, d)
		var rw []string
		if primary != "" {
			rw = []string{addr(primary, eng.Port)}
		}
		var ro []string
		for _, ip := range replicas {
			ro = append(ro, addr(ip, eng.Port))
		}
		if len(ro) == 0 {
			ro = rw
		}
		short := strings.TrimPrefix(d.ID, "db_")
		if base != "" {
			out = append(out,
				PublicRoute{Name: "db-" + short, Entrypoint: eng.Entrypoint, Host: PublicHost(d, base), Servers: rw, Allow: n.Public.Allow},
				PublicRoute{Name: "db-" + short + "-ro", Entrypoint: eng.Entrypoint, Host: PublicReadHost(d, base), Servers: ro, Allow: n.Public.Allow})
		}
		for _, p := range []struct {
			port    int
			suffix  string
			servers []string
		}{{n.Public.Port, "-port", rw}, {n.Public.ReadPort, "-ro-port", ro}} {
			if p.port == 0 {
				continue
			}
			r := PublicRoute{Name: "db-" + short + p.suffix, Entrypoint: PortEntrypoint(p.port), Port: p.port, Servers: p.servers, Allow: n.Public.Allow}
			out = append(out, r)
			if !n.Public.RequireTLS {
				r.Name, r.Plain = r.Name+"-plain", true
				out = append(out, r)
			}
		}
	}
	return out
}

// PublicPorts are the dedicated ports of every public database, for the
// Traefik entrypoints and the firewall. They need no base domain: plain
// connections work without one.
func (m *Manager) PublicPorts(ctx context.Context) []PublicPort {
	dbs, err := m.st.ListDatabases(ctx)
	if err != nil {
		return nil
	}
	var out []PublicPort
	for _, d := range dbs {
		n := d.ParseNetwork()
		if !n.Public.Enabled || d.Deleting {
			continue
		}
		for _, p := range []int{n.Public.Port, n.Public.ReadPort} {
			if p > 0 {
				out = append(out, PublicPort{Port: p, Allow: n.Public.Allow})
			}
		}
	}
	return out
}

func (m *Manager) publicPortRange() (int, int) {
	if r := m.PublicPortRange; r[0] > 0 && r[1] >= r[0] {
		return r[0], r[1]
	}
	return DefaultPublicPorts[0], DefaultPublicPorts[1]
}

// assignPorts gives every public database its two dedicated ports and frees
// those of databases no longer public. It reports whether anything changed.
func (m *Manager) assignPorts(ctx context.Context) (bool, error) {
	m.portMu.Lock()
	defer m.portMu.Unlock()
	dbs, err := m.st.ListDatabases(ctx)
	if err != nil {
		return false, err
	}
	used := map[int]bool{}
	for _, d := range dbs {
		if n := d.ParseNetwork(); n.Public.Enabled && !d.Deleting {
			used[n.Public.Port], used[n.Public.ReadPort] = true, true
		}
	}
	lo, hi := m.publicPortRange()
	next := lo
	free := func() int {
		for ; next <= hi; next++ {
			if !used[next] {
				used[next] = true
				return next
			}
		}
		return 0
	}
	changed := false
	for _, d := range dbs {
		n := d.ParseNetwork()
		want := n
		if n.Public.Enabled && !d.Deleting {
			if want.Public.Port == 0 {
				want.Public.Port = free()
			}
			if want.Public.ReadPort == 0 {
				want.Public.ReadPort = free()
			}
			if want.Public.Port == 0 || want.Public.ReadPort == 0 {
				m.log.Warn("no free public database port", "database", d.Name, "range", fmt.Sprintf("%d-%d", lo, hi))
			}
		} else {
			want.Public.Port, want.Public.ReadPort = 0, 0
		}
		if want.Public.Port == n.Public.Port && want.Public.ReadPort == n.Public.ReadPort {
			continue
		}
		b, _ := json.Marshal(want)
		if err := m.st.SetDatabaseNetwork(ctx, d.ID, string(b), m.now().UTC()); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// PublicHosts are the names that need certificates.
func (m *Manager) PublicHosts(ctx context.Context, base string) []string {
	var out []string
	for _, r := range m.PublicRoutes(ctx, base) {
		if r.Host != "" {
			out = append(out, r.Host)
		}
	}
	return out
}

// PublicEngines are the engines with at least one public database (their
// entrypoint ports are opened on the controller and edge nodes).
func (m *Manager) PublicEngines(ctx context.Context) []string {
	dbs, err := m.st.ListDatabases(ctx)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range dbs {
		if d.ParseNetwork().Public.Enabled && !d.Deleting && !seen[d.Engine] {
			seen[d.Engine] = true
			out = append(out, d.Engine)
		}
	}
	return out
}

// endpoints returns the running primary's IP and the replicas' IPs.
func (m *Manager) endpoints(ctx context.Context, d store.Database) (string, []string) {
	members, err := m.st.DatabaseMembers(ctx, d.ID)
	if err != nil {
		return "", nil
	}
	st := parseState(d.State)
	var primary string
	var replicas []string
	for _, mb := range members {
		if mb.Kind != KindData || mb.IP == "" || mb.State != store.TaskRunning {
			continue
		}
		if mb.Ordinal == st.Primary {
			primary = mb.IP
		} else if l := m.liveOf(mb.ID); l == nil || l.Role != "master" {
			replicas = append(replicas, mb.IP) // a restarted old primary joins as a replica soon
		}
	}
	return primary, replicas
}

// SetNetwork changes a database's access list and public endpoint.
func (m *Manager) SetNetwork(ctx context.Context, id string, n Network, exists func(p secgroup.Peer) bool, actor string) (View, error) {
	d, err := m.st.DatabaseByID(ctx, id)
	if err != nil {
		return View{}, err
	}
	if err := NormalizeNetwork(&n, exists); err != nil {
		return View{}, ErrInvalid{err}
	}
	old := d.ParseNetwork()
	n.Public.Port, n.Public.ReadPort = 0, 0 // the controller assigns them
	if n.Public.Enabled {
		n.Public.Port, n.Public.ReadPort = old.Public.Port, old.Public.ReadPort
	}
	b, _ := json.Marshal(n)
	if err := m.st.SetDatabaseNetwork(ctx, id, string(b), m.now().UTC()); err != nil {
		return View{}, err
	}
	if old.Public.Enabled != n.Public.Enabled {
		m.event(ctx, id, "network", onOff(old.Public.Enabled), onOff(n.Public.Enabled), "public endpoint", actor)
	}
	if strings.Join(old.Access, ",") != strings.Join(n.Access, ",") {
		m.event(ctx, id, "network", strings.Join(old.Access, " "), strings.Join(n.Access, " "), "access list", actor)
	}
	if old.Public.RequireTLS != n.Public.RequireTLS {
		m.event(ctx, id, "network", tlsMode(old.Public.RequireTLS), tlsMode(n.Public.RequireTLS), "public connections", actor)
	}
	if _, err := m.assignPorts(ctx); err != nil {
		return View{}, err
	}
	m.publish(ctx, id)
	m.changed()
	m.networkChanged()
	d, _ = m.st.DatabaseByID(ctx, id)
	return m.View(ctx, d), nil
}

func tlsMode(requireTLS bool) string {
	if requireTLS {
		return "TLS only"
	}
	return "TLS or plain"
}

func onOff(b bool) string {
	if b {
		return "public"
	}
	return "private"
}

func (m *Manager) networkChanged() {
	if m.OnNetworkChange != nil {
		m.OnNetworkChange()
	}
}
