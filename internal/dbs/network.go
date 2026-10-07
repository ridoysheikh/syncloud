package dbs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"syncloud/internal/secgroup"
	"syncloud/internal/store"
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

// PublicRoute is one TLS endpoint Traefik serves for a database.
type PublicRoute struct {
	Name       string // router/service name
	Entrypoint string
	Host       string
	Servers    []string // host:port of members
	Allow      []string
}

// PublicRoutes are the TCP routes of every public database (none without a
// base domain). The read-write route targets the current primary.
func (m *Manager) PublicRoutes(ctx context.Context, base string) []PublicRoute {
	if base == "" {
		return nil
	}
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
		out = append(out,
			PublicRoute{Name: "db-" + short, Entrypoint: eng.Entrypoint, Host: PublicHost(d, base), Servers: rw, Allow: n.Public.Allow},
			PublicRoute{Name: "db-" + short + "-ro", Entrypoint: eng.Entrypoint, Host: PublicReadHost(d, base), Servers: ro, Allow: n.Public.Allow})
	}
	return out
}

// PublicHosts are the names that need certificates.
func (m *Manager) PublicHosts(ctx context.Context, base string) []string {
	var out []string
	for _, r := range m.PublicRoutes(ctx, base) {
		out = append(out, r.Host)
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
	m.publish(ctx, id)
	m.changed()
	m.networkChanged()
	d, _ = m.st.DatabaseByID(ctx, id)
	return m.View(ctx, d), nil
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
