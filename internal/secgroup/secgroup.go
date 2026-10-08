// Package secgroup implements security groups (§8.3): AWS-style allow rules
// between containers, attached to services. It validates rules, compiles
// every group into the sets and rules agents enforce with nftables, and
// answers reachability questions with the same semantics.
package secgroup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/firewall"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// Rule allows traffic to (inbound) or from (outbound) a group's members.
type Rule struct {
	Protocol    string   `json:"protocol"` // tcp | udp | icmp | any
	Ports       string   `json:"ports"`    // "", "5432", "8000-8100"
	Peers       []string `json:"peers"`    // sources (inbound) or destinations (outbound)
	Description string   `json:"description"`
}

// Peer kinds.
const (
	KindAny         = "any"         // anywhere
	KindCluster     = "cluster"     // the cluster nodes' mesh addresses
	KindCIDR        = "cidr"        // an IPv4 address or CIDR
	KindGroup       = "group"       // members of a security group
	KindService     = "service"     // tasks of a service
	KindEnvironment = "environment" // every task in an environment
	KindProject     = "project"     // every task in a project
	KindSelfEnv     = "self-env"    // the member's own environment
	KindSelfProject = "self-project"
	KindSelf        = "self" // members of the rule's own group
)

// Peer is a parsed rule peer.
type Peer struct {
	Kind               string
	Project, Env, Name string
	CIDR               netip.Prefix
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// ParsePeer parses a peer written relative to project (the group's project)
// and returns it with its canonical spelling:
//
//	any, cluster, 10.0.0.0/8, group:[project/]name, service:[project/]env/name,
//	environment:self|[project/]env, project:self|name
func ParsePeer(s, project string) (Peer, string, error) {
	s = strings.TrimSpace(s)
	switch s {
	case KindAny, "0.0.0.0/0":
		return Peer{Kind: KindAny}, KindAny, nil
	case KindCluster:
		return Peer{Kind: KindCluster}, KindCluster, nil
	case "environment:self":
		return Peer{Kind: KindSelfEnv}, s, nil
	case "project:self":
		return Peer{Kind: KindSelfProject}, s, nil
	case KindSelf:
		return Peer{Kind: KindSelf}, KindSelf, nil
	}
	kind, v, ok := strings.Cut(s, ":")
	if !ok {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return Peer{}, "", fmt.Errorf("peer %q: use any, cluster, an IPv4 address or CIDR, group:NAME, service:ENV/NAME, environment:ENV|self or project:NAME|self", s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if !p.Addr().Is4() {
			return Peer{}, "", fmt.Errorf("peer %q: container addresses are IPv4", s)
		}
		p = p.Masked()
		if p.Bits() == 0 {
			return Peer{Kind: KindAny}, KindAny, nil
		}
		return Peer{Kind: KindCIDR, CIDR: p}, p.String(), nil
	}
	parts := strings.Split(v, "/")
	for _, x := range parts {
		if !nameRE.MatchString(x) {
			return Peer{}, "", fmt.Errorf("peer %q: invalid name %q", s, x)
		}
	}
	p := Peer{Kind: kind}
	switch kind {
	case KindGroup:
		switch len(parts) {
		case 1:
			p.Project, p.Name = project, parts[0]
		case 2:
			p.Project, p.Name = parts[0], parts[1]
		default:
			return Peer{}, "", fmt.Errorf("peer %q: use group:NAME or group:PROJECT/NAME", s)
		}
		return p, "group:" + p.Project + "/" + p.Name, nil
	case KindService:
		switch len(parts) {
		case 2:
			p.Project, p.Env, p.Name = project, parts[0], parts[1]
		case 3:
			p.Project, p.Env, p.Name = parts[0], parts[1], parts[2]
		default:
			return Peer{}, "", fmt.Errorf("peer %q: use service:ENV/NAME or service:PROJECT/ENV/NAME", s)
		}
		return p, "service:" + p.Project + "/" + p.Env + "/" + p.Name, nil
	case KindEnvironment:
		switch len(parts) {
		case 1:
			p.Project, p.Env = project, parts[0]
		case 2:
			p.Project, p.Env = parts[0], parts[1]
		default:
			return Peer{}, "", fmt.Errorf("peer %q: use environment:self, environment:ENV or environment:PROJECT/ENV", s)
		}
		return p, "environment:" + p.Project + "/" + p.Env, nil
	case KindProject:
		if len(parts) != 1 {
			return Peer{}, "", fmt.Errorf("peer %q: use project:NAME or project:self", s)
		}
		p.Project = parts[0]
		return p, "project:" + p.Project, nil
	}
	return Peer{}, "", fmt.Errorf("peer %q: unknown kind %q", s, kind)
}

// ── model ───────────────────────────────────────────────────────────────────

// Service is a service with the addresses of its tasks and job runs.
type Service struct {
	ID, Project, Env, Name string
	IPs                    []string
}

// Standalone is a job run that belongs to no service.
type Standalone struct {
	Project, Env, IP string
}

// Group is a security group with parsed rules.
type Group struct {
	ID, Project, Name string
	Default           bool
	Inbound, Outbound []Rule
	Services          []string // attached service IDs
}

// Model is everything compilation and evaluation need.
type Model struct {
	Envs        map[string][]string // project -> environment names
	Services    []Service
	Standalone  []Standalone
	Groups      []Group
	ClusterIPs  []string
	PlatformIPs []string

	byID     map[string]*Service
	attached map[string][]*Group // service ID -> explicitly attached groups
	defaults map[string]*Group   // project -> default group
	byName   map[string]*Group   // "project/name"
}

// ResetIndex must be called after changing a model's groups or services.
func (m *Model) ResetIndex() { m.byID, m.attached, m.defaults, m.byName = nil, nil, nil, nil }

func (m *Model) index() {
	if m.byID != nil {
		return
	}
	m.byID, m.attached, m.defaults, m.byName = map[string]*Service{}, map[string][]*Group{}, map[string]*Group{}, map[string]*Group{}
	for i := range m.Services {
		m.byID[m.Services[i].ID] = &m.Services[i]
	}
	for i := range m.Groups {
		g := &m.Groups[i]
		m.byName[g.Project+"/"+g.Name] = g
		if g.Default {
			m.defaults[g.Project] = g
		}
		for _, sv := range g.Services {
			m.attached[sv] = append(m.attached[sv], g)
		}
	}
}

// GroupsOf returns the groups a service uses: those attached to it, or its
// project's default group.
func (m *Model) GroupsOf(serviceID string) []*Group {
	m.index()
	if gs := m.attached[serviceID]; len(gs) > 0 {
		return gs
	}
	if sv := m.byID[serviceID]; sv != nil && m.defaults[sv.Project] != nil {
		return []*Group{m.defaults[sv.Project]}
	}
	return nil
}

func (m *Model) isMember(g *Group, serviceID string) bool {
	for _, x := range m.GroupsOf(serviceID) {
		if x == g {
			return true
		}
	}
	return false
}

// Members returns the services using g.
func (m *Model) Members(g *Group) []*Service {
	m.index()
	var out []*Service
	for i := range m.Services {
		if m.Services[i].Project == g.Project && m.isMember(g, m.Services[i].ID) {
			out = append(out, &m.Services[i])
		}
	}
	return out
}

// ── compilation ─────────────────────────────────────────────────────────────

type compiler struct {
	m    *Model
	sets map[string]*agentv1.SecuritySet // by key
	pol  *agentv1.SecurityPolicy
}

func (c *compiler) set(key string, ips []string, match []string) string {
	if s, ok := c.sets[key]; ok {
		return s.GetName()
	}
	sum := sha256.Sum256([]byte(key))
	uniq := map[string]bool{}
	for _, ip := range ips {
		if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
			uniq[a.String()] = true
		}
	}
	list := make([]string, 0, len(uniq))
	for ip := range uniq {
		list = append(list, ip)
	}
	sort.Strings(list)
	s := &agentv1.SecuritySet{Name: "s_" + hex.EncodeToString(sum[:])[:12], Ips: list, Match: match}
	c.sets[key] = s
	c.pol.Sets = append(c.pol.Sets, s)
	return s.GetName()
}

// members is the set of g's members, optionally only in one environment.
func (c *compiler) members(g *Group, env string) string {
	var ips, match []string
	for _, sv := range c.m.Members(g) {
		if env != "" && sv.Env != env {
			continue
		}
		ips = append(ips, sv.IPs...)
		match = append(match, "svc:"+sv.ID)
	}
	if g.Default {
		for _, r := range c.m.Standalone {
			if r.Project == g.Project && (env == "" || r.Env == env) {
				ips = append(ips, r.IP)
			}
		}
		if env == "" {
			match = append(match, "standalone:"+g.Project)
		} else {
			match = append(match, "standalone:"+g.Project+"/"+env)
		}
	}
	return c.set("members:"+g.ID+":"+env, ips, match)
}

func (c *compiler) envSet(project, env string) string {
	var ips []string
	for _, sv := range c.m.Services {
		if sv.Project == project && sv.Env == env {
			ips = append(ips, sv.IPs...)
		}
	}
	for _, r := range c.m.Standalone {
		if r.Project == project && r.Env == env {
			ips = append(ips, r.IP)
		}
	}
	return c.set("env:"+project+"/"+env, ips, []string{"env:" + project + "/" + env})
}

func (c *compiler) projectSet(project string) string {
	var ips []string
	for _, sv := range c.m.Services {
		if sv.Project == project {
			ips = append(ips, sv.IPs...)
		}
	}
	for _, r := range c.m.Standalone {
		if r.Project == project {
			ips = append(ips, r.IP)
		}
	}
	return c.set("project:"+project, ips, []string{"proj:" + project})
}

func (c *compiler) serviceSet(project, env, name string) string {
	for _, sv := range c.m.Services {
		if sv.Project == project && sv.Env == env && sv.Name == name {
			return c.set("svc:"+sv.ID, sv.IPs, []string{"svc:" + sv.ID})
		}
	}
	return c.set("svc:missing:"+project+"/"+env+"/"+name, nil, nil)
}

// envsOf lists the environments g's members live in (for environment:self).
func (c *compiler) envsOf(g *Group) []string {
	seen := map[string]bool{}
	for _, sv := range c.m.Members(g) {
		seen[sv.Env] = true
	}
	if g.Default {
		for _, e := range c.m.Envs[g.Project] {
			seen[e] = true
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Compile turns every group into the policy agents enforce.
func Compile(m *Model) *agentv1.SecurityPolicy {
	m.index()
	c := &compiler{m: m, sets: map[string]*agentv1.SecuritySet{}, pol: &agentv1.SecurityPolicy{PlatformIps: m.PlatformIPs}}
	for i := range m.Groups {
		g := &m.Groups[i]
		for _, dir := range []string{"in", "out"} {
			rules := g.Inbound
			if dir == "out" {
				rules = g.Outbound
			}
			for n, r := range rules {
				c.rule(g, dir, n, r)
			}
		}
	}
	return c.pol
}

func (c *compiler) rule(g *Group, dir string, n int, r Rule) {
	id := g.ID + ":" + dir + ":" + strconv.Itoa(n)
	var peerSets, cidrs []string
	selfEnv := false
	for _, ps := range r.Peers {
		p, _, err := ParsePeer(ps, g.Project)
		if err != nil {
			continue // validated on save; a stale rule matches nothing
		}
		switch p.Kind {
		case KindAny:
			cidrs = append(cidrs, "0.0.0.0/0")
		case KindCluster:
			for _, ip := range c.m.ClusterIPs {
				cidrs = append(cidrs, ip+"/32")
			}
		case KindCIDR:
			cidrs = append(cidrs, p.CIDR.String())
		case KindSelfEnv:
			selfEnv = true
		case KindSelf:
			peerSets = append(peerSets, c.members(g, ""))
		case KindSelfProject:
			peerSets = append(peerSets, c.projectSet(g.Project))
		case KindProject:
			peerSets = append(peerSets, c.projectSet(p.Project))
		case KindEnvironment:
			peerSets = append(peerSets, c.envSet(p.Project, p.Env))
		case KindService:
			peerSets = append(peerSets, c.serviceSet(p.Project, p.Env, p.Name))
		case KindGroup:
			if pg := c.m.byName[p.Project+"/"+p.Name]; pg != nil {
				peerSets = append(peerSets, c.members(pg, ""))
			}
		}
	}
	newRule := func(local string, sets, cidrs []string) *agentv1.SecurityRule {
		return &agentv1.SecurityRule{Id: id, Direction: dir, Protocol: r.Protocol, Ports: r.Ports, LocalSet: local, PeerSets: sets, PeerCidrs: cidrs}
	}
	if len(peerSets) > 0 || len(cidrs) > 0 {
		c.pol.Rules = append(c.pol.Rules, newRule(c.members(g, ""), peerSets, cidrs))
	}
	if selfEnv {
		for _, env := range c.envsOf(g) {
			c.pol.Rules = append(c.pol.Rules, newRule(c.members(g, env), []string{c.envSet(g.Project, env)}, nil))
		}
	}
}

// ── validation ──────────────────────────────────────────────────────────────

// Validate checks a group's rules and canonicalizes their peers. exists
// reports whether a referenced project, environment, service or group
// exists (kind, project, env, name).
func Validate(project string, rules []Rule, exists func(p Peer) bool) ([]Rule, error) {
	if len(rules) > 100 {
		return nil, errors.New("at most 100 rules per direction")
	}
	out := make([]Rule, 0, len(rules))
	for i, r := range rules {
		r.Protocol = strings.ToLower(strings.TrimSpace(r.Protocol))
		if r.Protocol == "" {
			r.Protocol = "any"
		}
		r.Ports = strings.TrimSpace(r.Ports)
		if err := firewall.ValidateSecurityRule(r.Protocol, r.Ports); err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
		if len(r.Peers) == 0 {
			return nil, fmt.Errorf("rule %d: needs at least one peer", i+1)
		}
		if len(r.Description) > 200 {
			return nil, fmt.Errorf("rule %d: description is too long", i+1)
		}
		peers := make([]string, 0, len(r.Peers))
		for _, ps := range r.Peers {
			p, canon, err := ParsePeer(ps, project)
			if err != nil {
				return nil, fmt.Errorf("rule %d: %w", i+1, err)
			}
			if exists != nil && !exists(p) {
				return nil, fmt.Errorf("rule %d: %s does not exist", i+1, canon)
			}
			peers = append(peers, canon)
		}
		r.Peers = peers
		out = append(out, r)
	}
	return out, nil
}

// ── evaluation ──────────────────────────────────────────────────────────────

// Endpoint is one side of a connection: a service's task, a job run without
// a service, an address, or the platform (Traefik and the controller).
type Endpoint struct {
	ServiceID    string
	Project, Env string // a standalone job run
	IP           string
	Platform     bool
}

// Match names the rule that allowed a connection.
type Match struct {
	GroupID   string `json:"groupId"`
	Group     string `json:"group"` // project/name
	Direction string `json:"direction"`
	Index     int    `json:"index"`
	Rule      Rule   `json:"rule"`
}

// Verdict is the answer to "can A reach B on proto/port?".
type Verdict struct {
	Allowed bool   `json:"allowed"`
	Egress  *Match `json:"egress,omitempty"`
	Ingress *Match `json:"ingress,omitempty"`
	Reason  string `json:"reason"`
}

func protoMatches(r Rule, proto string, port int) bool {
	switch r.Protocol {
	case "any":
		return true
	case "icmp":
		return proto == "icmp"
	}
	if r.Protocol != proto {
		return false
	}
	if r.Ports == "" {
		return true
	}
	lo, hi, _ := strings.Cut(r.Ports, "-")
	l, _ := strconv.Atoi(lo)
	h := l
	if hi != "" {
		h, _ = strconv.Atoi(hi)
	}
	return port >= l && port <= h
}

// ident is what an endpoint is, for peer matching.
type ident struct {
	serviceID, project, env string
	ips                     []string
}

func (m *Model) identOf(e Endpoint) ident {
	if e.ServiceID != "" {
		if sv := m.byID[e.ServiceID]; sv != nil {
			return ident{serviceID: sv.ID, project: sv.Project, env: sv.Env, ips: sv.IPs}
		}
	}
	id := ident{project: e.Project, env: e.Env}
	if e.IP != "" {
		id.ips = []string{e.IP}
	}
	return id
}

// groupsOfIdent covers standalone runs, which use the default group.
func (m *Model) groupsOfIdent(x ident) []*Group {
	if x.serviceID != "" {
		return m.GroupsOf(x.serviceID)
	}
	if x.project != "" && m.defaults[x.project] != nil {
		return []*Group{m.defaults[x.project]}
	}
	return nil
}

// peerMatches reports whether the endpoint x is covered by peer p of a rule
// in group g whose member is self.
func (m *Model) peerMatches(p Peer, x, self ident) bool {
	inCIDR := func(pfx netip.Prefix) bool {
		for _, ip := range x.ips {
			if a, err := netip.ParseAddr(ip); err == nil && pfx.Contains(a) {
				return true
			}
		}
		return false
	}
	switch p.Kind {
	case KindAny:
		return true
	case KindCluster:
		for _, c := range m.ClusterIPs {
			for _, ip := range x.ips {
				if ip == c {
					return true
				}
			}
		}
	case KindCIDR:
		return inCIDR(p.CIDR)
	case KindSelfEnv:
		return x.project != "" && x.project == self.project && x.env == self.env
	case KindSelfProject:
		return x.project != "" && x.project == self.project
	case KindProject:
		return x.project == p.Project
	case KindEnvironment:
		return x.project == p.Project && x.env == p.Env
	case KindService:
		sv := m.byID[x.serviceID]
		return sv != nil && sv.Project == p.Project && sv.Env == p.Env && sv.Name == p.Name
	case KindGroup:
		pg := m.byName[p.Project+"/"+p.Name]
		for _, g := range m.groupsOfIdent(x) {
			if g == pg {
				return true
			}
		}
	}
	return false
}

func (m *Model) firstMatch(groups []*Group, dir string, peer, self ident, proto string, port int) *Match {
	for _, g := range groups {
		rules := g.Inbound
		if dir == "out" {
			rules = g.Outbound
		}
		for i, r := range rules {
			if !protoMatches(r, proto, port) {
				continue
			}
			for _, ps := range r.Peers {
				p, _, err := ParsePeer(ps, g.Project)
				if err == nil && p.Kind == KindSelf && slices.Contains(m.groupsOfIdent(peer), g) {
					return &Match{GroupID: g.ID, Group: g.Project + "/" + g.Name, Direction: dir, Index: i, Rule: r}
				}
				if err == nil && m.peerMatches(p, peer, self) {
					return &Match{GroupID: g.ID, Group: g.Project + "/" + g.Name, Direction: dir, Index: i, Rule: r}
				}
			}
		}
	}
	return nil
}

func groupNames(gs []*Group) string {
	var n []string
	for _, g := range gs {
		n = append(n, g.Project+"/"+g.Name)
	}
	if len(n) == 0 {
		return "no group"
	}
	return strings.Join(n, ", ")
}

// Evaluate answers whether src can open a connection to dst (a service's
// tasks) on proto/port, and which rules decide it.
func (m *Model) Evaluate(src, dst Endpoint, proto string, port int) Verdict {
	m.index()
	d := m.identOf(dst)
	if d.serviceID == "" {
		return Verdict{Reason: "the destination must be a service"}
	}
	if src.Platform || (src.IP != "" && slices.Contains(m.PlatformIPs, src.IP)) {
		return Verdict{Allowed: true, Reason: "cluster nodes (Traefik, health checks) can always reach tasks"}
	}
	s := m.identOf(src)
	var v Verdict
	if s.project != "" { // a container: its outbound rules apply
		sg := m.groupsOfIdent(s)
		v.Egress = m.firstMatch(sg, "out", d, s, proto, port)
		if v.Egress == nil {
			v.Reason = "no outbound rule of " + groupNames(sg) + " allows it"
			return v
		}
	}
	dg := m.GroupsOf(d.serviceID)
	v.Ingress = m.firstMatch(dg, "in", s, d, proto, port)
	if v.Ingress == nil {
		v.Reason = "no inbound rule of " + groupNames(dg) + " allows it"
		return v
	}
	v.Allowed, v.Reason = true, "allowed by "+v.Ingress.Group+" inbound rule "+strconv.Itoa(v.Ingress.Index+1)
	if v.Egress != nil {
		v.Reason += " and " + v.Egress.Group + " outbound rule " + strconv.Itoa(v.Egress.Index+1)
	}
	return v
}

// ── effective rules ─────────────────────────────────────────────────────────

// Describe is a rule as one line, e.g. "tcp 5432 from service shop/production/web".
func Describe(r Rule, dir string) string {
	what := r.Protocol
	if r.Ports != "" {
		what += " " + r.Ports
	}
	if what == "any" {
		what = "all traffic"
	}
	word := "from"
	if dir == "out" {
		word = "to"
	}
	return what + " " + word + " " + strings.Join(r.Peers, ", ")
}

// EffectiveLines lists what applies to a service, one line per rule; the
// preview diffs these.
func (m *Model) EffectiveLines(serviceID string) []string {
	m.index()
	gs := m.GroupsOf(serviceID)
	out := []string{"groups: " + groupNames(gs)}
	for _, g := range gs {
		for _, r := range g.Inbound {
			out = append(out, "in  "+Describe(r, "in")+"  ("+g.Name+")")
		}
	}
	for _, g := range gs {
		for _, r := range g.Outbound {
			out = append(out, "out "+Describe(r, "out")+"  ("+g.Name+")")
		}
	}
	return out
}
