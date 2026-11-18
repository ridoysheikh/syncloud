// Package ports keeps the port table (Phase 26): every public TCP/UDP port
// on every load-balancer node it is served from, so a port is never given
// out twice on one node. Ports used by Traefik and the platform can never
// be taken. A port number may be reused on different load balancers (a
// project's own nodes and the shared one).
package ports

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/ingress"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Platform ports that can never be chosen.
type reserved struct {
	Protocol string // tcp, udp or "" (both)
	Port     int
	Reason   string
}

var reservedPorts = []reserved{
	{"tcp", 80, "Traefik (HTTP)"},
	{"tcp", 443, "Traefik (HTTPS)"},
	{"udp", 443, "Traefik (HTTP/3)"},
	{"tcp", 22, "SSH"},
	{"udp", 51820, "WireGuard"},
	{"", 53, "the agent's DNS"},
	{"tcp", 7443, "the agent gateway"},
	{"tcp", 7444, "the agent gateway"},
	{"tcp", 8082, "the Traefik admin port"},
	{"tcp", 5432, "databases by name (PostgreSQL)"},
	{"tcp", 6379, "databases by name (Valkey)"},
}

// ControllerNode is the controller's node name (mesh.ControllerNode): the
// shared load balancer always includes it.
const ControllerNode = "ctl-0"

// Reserved is one row of the reserved list, for the dashboard.
type Reserved struct {
	Protocol string `json:"protocol"` // tcp | udp | tcp+udp
	Ports    string `json:"ports"`    // a number or LOW-HIGH
	Reason   string `json:"reason"`
}

// ErrInvalid is a refused request (a bad, reserved or taken port).
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }
func (e ErrInvalid) Unwrap() error { return e.Err }

// Owner is who holds a public port.
type Owner struct {
	Kind string // store.PortOwnerService | store.PortOwnerDatabase
	ID   string
	// Project is the owner's project ("" for standalone databases): its
	// load balancer decides the nodes.
	Project string
	// Shared keeps the owner on the shared load balancer whatever its
	// project chose (databases, Phase 18).
	Shared bool
}

// Want is one public port an owner holds or asks for.
type Want struct {
	PortName string // the service's port, or rw/ro
	Protocol string // tcp | udp
	Port     int
	Mode     string // store.PortAuto | store.PortChosen
}

// Source lists every public port that exists, by owner (the services' and
// the databases'), for the resync.
type Source func(ctx context.Context) map[Owner][]Want

// Ledger keeps the port table.
type Ledger struct {
	st  *store.Store
	log *slog.Logger
	mu  sync.Mutex

	// Target is who serves a project's public ports (nil: the shared
	// load balancer for every project).
	Target func(project string) ingress.Target
	// Shared are the nodes of the shared load balancer: the controller and
	// the edge-pool nodes.
	Shared func() []string
	// Holders are ports something other than Traefik listens on, on one
	// node ("tcp/25" -> process), from the agents' listener reports.
	Holders func(node string) map[string]string
	// Ranges are the automatic ranges (services, databases): kept for
	// automatic use only.
	Ranges [][2]int
	// Sources list the existing public ports for the resync.
	Sources []Source
	// OnConflict runs for each port a resync could not claim on a node.
	OnConflict func(c store.PortClaim)

	conflicts []store.PortClaim
}

func New(st *store.Store, log *slog.Logger) *Ledger {
	return &Ledger{st: st, log: log}
}

// Lock serializes everything that picks or claims ports; the primary key
// is the last guard.
func (l *Ledger) Lock()   { l.mu.Lock() }
func (l *Ledger) Unlock() { l.mu.Unlock() }

// ReservedList is the fixed list plus the automatic ranges.
func (l *Ledger) ReservedList() []Reserved {
	var out []Reserved
	for _, r := range reservedPorts {
		p := r.Protocol
		if p == "" {
			p = "tcp+udp"
		}
		out = append(out, Reserved{Protocol: p, Ports: strconv.Itoa(r.Port), Reason: r.Reason})
	}
	out = append(out, Reserved{Protocol: "tcp+udp", Ports: "any port sshd listens on", Reason: "SSH"})
	for _, rg := range l.Ranges {
		out = append(out, Reserved{Protocol: "tcp+udp", Ports: fmt.Sprintf("%d-%d", rg[0], rg[1]), Reason: "automatic ports only"})
	}
	return out
}

// reason is why a port can't be chosen ("" when it can).
func (l *Ledger) reason(protocol string, port int, auto bool) string {
	for _, r := range reservedPorts {
		if r.Port == port && (r.Protocol == "" || r.Protocol == protocol) {
			return "reserved for " + r.Reason
		}
	}
	if !auto {
		for _, rg := range l.Ranges {
			if port >= rg[0] && port <= rg[1] {
				return fmt.Sprintf("%d–%d is kept for automatic ports; leave the port empty to get one", rg[0], rg[1])
			}
		}
	}
	return ""
}

// Nodes are the load-balancer nodes an owner's ports are claimed on.
func (l *Ledger) Nodes(o Owner) []string {
	t := ingress.DefaultTarget
	if !o.Shared && o.Project != "" && l.Target != nil {
		t = l.Target(o.Project)
	}
	return l.expand(t)
}

// expand turns a target into node names.
func (l *Ledger) expand(t ingress.Target) []string {
	var out []string
	if t.Default {
		if l.Shared != nil {
			out = append(out, l.Shared()...)
		}
		if len(out) == 0 {
			out = append(out, ControllerNode) // before any node is known
		}
	}
	for _, n := range t.Nodes {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// State is a port's state on one node.
type State struct {
	Node   string `json:"node"`
	State  string `json:"state"`            // free | reserved | host | taken
	Reason string `json:"reason,omitempty"` // reserved: what for; host: the process
	// Owner of a taken port (shown when the caller may see it).
	OwnerKind string `json:"ownerKind,omitempty"`
	OwnerID   string `json:"ownerId,omitempty"`
	PortName  string `json:"portName,omitempty"`
}

// Check reports a port's state on every node an owner would claim it on.
// Ports the owner already holds are free for it.
func (l *Ledger) Check(ctx context.Context, o Owner, protocol string, port int, auto bool) ([]State, error) {
	claims, err := l.st.ListPortClaims(ctx)
	if err != nil {
		return nil, err
	}
	return l.check(claims, l.Nodes(o), o, protocol, port, auto), nil
}

func (l *Ledger) check(claims []store.PortClaim, nodes []string, o Owner, protocol string, port int, auto bool) []State {
	why := l.reason(protocol, port, auto)
	out := make([]State, 0, len(nodes))
	for _, n := range nodes {
		s := State{Node: n, State: "free"}
		switch {
		case why != "":
			s.State, s.Reason = "reserved", why
		default:
			for _, c := range claims {
				if c.Node == n && c.Protocol == protocol && c.Port == port && (c.OwnerKind != o.Kind || c.OwnerID != o.ID) {
					s.State, s.OwnerKind, s.OwnerID, s.PortName = "taken", c.OwnerKind, c.OwnerID, c.PortName
				}
			}
			if s.State == "free" && l.Holders != nil && !l.holds(claims, n, protocol, port) {
				if proc, ok := l.Holders(n)[protocol+"/"+strconv.Itoa(port)]; ok {
					s.State, s.Reason = "host", proc
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// holds reports a port claimed on a node (Traefik listens on it, which is
// not a clash).
func (l *Ledger) holds(claims []store.PortClaim, node, protocol string, port int) bool {
	for _, c := range claims {
		if c.Node == node && c.Protocol == protocol && c.Port == port {
			return true
		}
	}
	return false
}

// Free reports whether a port can be claimed by an owner (nil), or why not.
func Free(states []State, protocol string, port int, describe func(kind, id string) string) error {
	for _, s := range states {
		switch s.State {
		case "reserved":
			return ErrInvalid{fmt.Errorf("%s %d is %s", protocol, port, s.Reason)}
		case "host":
			return ErrInvalid{fmt.Errorf("%s %d is in use on %s by %s", protocol, port, s.Node, s.Reason)}
		case "taken":
			who := "another service"
			if describe != nil {
				who = describe(s.OwnerKind, s.OwnerID)
			}
			return ErrInvalid{fmt.Errorf("%s %d is taken on load balancer %s by %s; choose another port or give the project its own load balancer", protocol, port, s.Node, who)}
		}
	}
	return nil
}

// Claims are an owner's rows for its ports on its nodes, after they have
// been checked; the caller writes them with its own settings.
func (l *Ledger) Claims(o Owner, wants []Want) []store.PortClaim {
	nodes := l.Nodes(o)
	var out []store.PortClaim
	now := time.Now().UTC()
	for _, w := range wants {
		if w.Port == 0 {
			continue
		}
		for _, n := range nodes {
			out = append(out, store.PortClaim{Node: n, Protocol: w.Protocol, Port: w.Port, OwnerKind: o.Kind, OwnerID: o.ID, PortName: w.PortName, Mode: w.Mode, CreatedAt: now})
		}
	}
	return out
}

// Pick returns the lowest port in lo–hi free for an owner on all its nodes
// (0: none), leaving out ports in skip (picked earlier in the same change).
func (l *Ledger) Pick(ctx context.Context, o Owner, protocol string, lo, hi int, skip map[int]bool) (int, error) {
	claims, err := l.st.ListPortClaims(ctx)
	if err != nil {
		return 0, err
	}
	nodes := l.Nodes(o)
	for p := lo; p <= hi; p++ {
		if skip[p] {
			continue
		}
		// An automatic number is free for both protocols, as it always was:
		// tcp 20000 and udp 20000 never go to two different ports.
		if Free(l.check(claims, nodes, o, "tcp", p, true), "tcp", p, nil) == nil &&
			Free(l.check(claims, nodes, o, "udp", p, true), "udp", p, nil) == nil {
			return p, nil
		}
	}
	return 0, nil
}

// CheckTarget reports the ports a project would clash on if it were served
// by target (an ingress change, refused when it is not empty).
func (l *Ledger) CheckTarget(ctx context.Context, project string, target ingress.Target) ([]Clash, error) {
	claims, err := l.st.ListPortClaims(ctx)
	if err != nil {
		return nil, err
	}
	nodes := l.expand(target)
	mine := map[string]store.PortClaim{} // the project's ports, by owner/protocol/port
	others := []store.PortClaim{}
	owners := l.ownersOf(ctx, project)
	for _, c := range claims {
		if owners[c.OwnerKind+"/"+c.OwnerID] {
			mine[c.OwnerID+"/"+c.Protocol+"/"+strconv.Itoa(c.Port)] = c
		} else {
			others = append(others, c)
		}
	}
	var out []Clash
	for _, c := range mine {
		for _, o := range others {
			if o.Protocol == c.Protocol && o.Port == c.Port && slices.Contains(nodes, o.Node) {
				out = append(out, Clash{Node: o.Node, Protocol: c.Protocol, Port: c.Port, OwnerKind: o.OwnerKind, OwnerID: o.OwnerID})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Node < out[j].Node
	})
	return out, nil
}

// Clash is a port another owner holds on a node.
type Clash struct {
	Node      string `json:"node"`
	Protocol  string `json:"protocol"`
	Port      int    `json:"port"`
	OwnerKind string `json:"ownerKind"`
	OwnerID   string `json:"ownerId"`
}

// ErrClash refuses an ingress change that would put two holders of a port
// on one node.
type ErrClash struct{ Clashes []Clash }

func (e ErrClash) Error() string {
	var parts []string
	for _, c := range e.Clashes {
		parts = append(parts, fmt.Sprintf("%s %d on %s", c.Protocol, c.Port, c.Node))
	}
	return "these public ports are taken on the new load balancer: " + strings.Join(parts, ", ")
}

// ownersOf are the owners in a project (by kind/id), from the sources.
func (l *Ledger) ownersOf(ctx context.Context, project string) map[string]bool {
	out := map[string]bool{}
	for _, src := range l.Sources {
		for o := range src(ctx) {
			if o.Project == project && !o.Shared {
				out[o.Kind+"/"+o.ID] = true
			}
		}
	}
	return out
}

// Sync rewrites the table from the sources: each owner's ports on the
// nodes that serve them now (load balancers change as edge pools and
// project ingress change). A port that would get a second holder on a
// node stays with its older holder and is reported as a conflict.
func (l *Ledger) Sync(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sync(ctx)
}

func (l *Ledger) sync(ctx context.Context) error {
	old, err := l.st.ListPortClaims(ctx)
	if err != nil {
		return err
	}
	since := map[string]time.Time{}
	for _, c := range old {
		k := c.OwnerKind + "/" + c.OwnerID + "/" + c.PortName + "/" + c.Protocol + "/" + strconv.Itoa(c.Port)
		if t, ok := since[k]; !ok || c.CreatedAt.Before(t) {
			since[k] = c.CreatedAt
		}
	}
	now := time.Now().UTC()
	var want []store.PortClaim
	for _, src := range l.Sources {
		for o, ws := range src(ctx) {
			for _, c := range l.Claims(o, ws) {
				k := c.OwnerKind + "/" + c.OwnerID + "/" + c.PortName + "/" + c.Protocol + "/" + strconv.Itoa(c.Port)
				if t, ok := since[k]; ok {
					c.CreatedAt = t
				} else {
					c.CreatedAt = now
				}
				want = append(want, c)
			}
		}
	}
	// Older holders first: they keep a contested node.
	sort.SliceStable(want, func(i, j int) bool {
		if !want[i].CreatedAt.Equal(want[j].CreatedAt) {
			return want[i].CreatedAt.Before(want[j].CreatedAt)
		}
		return want[i].OwnerID < want[j].OwnerID
	})
	if same(old, want) {
		l.conflicts = nil
		return nil
	}
	skipped, err := l.st.ReplaceAllPortClaims(ctx, want)
	if err != nil {
		return err
	}
	for _, c := range skipped {
		l.log.Warn("public port clash: the port is already held on this node", "node", c.Node, "protocol", c.Protocol, "port", c.Port, "owner", c.OwnerKind+"/"+c.OwnerID)
		if l.OnConflict != nil {
			l.OnConflict(c)
		}
	}
	l.conflicts = skipped
	return nil
}

// Conflicts are the ports the last resync could not claim on a node.
func (l *Ledger) Conflicts() []store.PortClaim {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.conflicts)
}

func same(old, want []store.PortClaim) bool {
	if len(old) != len(want) {
		return false
	}
	key := func(c store.PortClaim) string {
		return fmt.Sprintf("%s/%s/%d/%s/%s/%s/%s", c.Node, c.Protocol, c.Port, c.OwnerKind, c.OwnerID, c.PortName, c.Mode)
	}
	seen := map[string]bool{}
	for _, c := range old {
		seen[key(c)] = true
	}
	for _, c := range want {
		if !seen[key(c)] {
			return false
		}
	}
	return true
}

// Run resyncs every minute (edge nodes come and go).
func (l *Ledger) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := l.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
			l.log.Error("port table", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Table is every claim.
func (l *Ledger) Table(ctx context.Context) ([]store.PortClaim, error) {
	return l.st.ListPortClaims(ctx)
}
