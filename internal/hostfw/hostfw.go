// Package hostfw holds the controller's side of the locked-down host
// firewall (Phase 24): the built-in SSH access and its guard, the block list
// with auto-ban, and each node's listening ports with the firewall's verdict.
// The mesh manager adds what it returns to every node's firewall.
package hostfw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/firewall"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// SettingSSH holds the SSH settings (JSON).
const SettingSSH = "firewall.ssh"

// RuleSSH is the built-in SSH rule's and guard's ID.
const RuleSSH = "builtin:ssh"

// SSH is how the nodes' SSH is reached: always open on port 22 and on every
// port sshd listens on, from Sources (empty = anywhere).
type SSH struct {
	// Ports are opened besides 22 and the detected sshd ports.
	Ports   []int    `json:"ports"`
	Sources []string `json:"sources"`
	// Rate limits new connections per source and minute (0 = no limit),
	// with Burst; Exempt sources are never limited.
	Rate   int      `json:"rate"`
	Burst  int      `json:"burst"`
	Exempt []string `json:"exempt"`
	// AutoBan blocks a source over the rate on that node for BanMinutes.
	AutoBan    bool `json:"autoBan"`
	BanMinutes int  `json:"banMinutes"`
}

// DefaultSSH is open to anyone, with a rate limit and auto-ban.
func DefaultSSH() SSH {
	return SSH{Ports: []int{}, Sources: []string{}, Exempt: []string{}, Rate: 10, Burst: 5, AutoBan: true, BanMinutes: 10}
}

// Validate normalizes and checks the settings.
func (s *SSH) Validate() error {
	if s.Ports == nil {
		s.Ports = []int{}
	}
	if s.Sources == nil {
		s.Sources = []string{}
	}
	if s.Exempt == nil {
		s.Exempt = []string{}
	}
	for _, p := range s.Ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("port %d is out of range", p)
		}
	}
	if len(s.Ports) > 8 {
		return errors.New("at most 8 extra SSH ports")
	}
	if err := firewall.ValidateRule("tcp", "22", s.Sources); err != nil {
		return err
	}
	for _, src := range s.Sources {
		if src == firewall.SourceCluster {
			return errors.New(`"cluster" cannot be the only way in: name addresses or leave the list empty`)
		}
	}
	for _, e := range s.Exempt {
		if _, err := netip.ParsePrefix(e); err != nil {
			if _, err := netip.ParseAddr(e); err != nil {
				return fmt.Errorf("exempt %q: use an IP or a CIDR", e)
			}
		}
	}
	if s.Rate < 0 || s.Rate > 10000 || s.Burst < 0 || s.Burst > 10000 {
		return errors.New("rate and burst must be 0–10000")
	}
	if s.AutoBan && s.Rate == 0 {
		return errors.New("auto-ban needs a rate limit")
	}
	if s.BanMinutes == 0 {
		s.BanMinutes = 10
	}
	if s.BanMinutes < 1 || s.BanMinutes > 7*24*60 {
		return errors.New("ban minutes must be 1–10080")
	}
	return nil
}

// Listener is a listening socket on a node with the firewall's verdict.
type Listener struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     uint32 `json:"port"`
	Process  string `json:"process"`
	// Verdict: open (to anyone), restricted (some sources), blocked (the
	// firewall drops it), local (loopback), private (the mesh only).
	Verdict string `json:"verdict"`
	// Rule is the rule that opens it ("" when blocked or not public).
	Rule string `json:"rule,omitempty"`
}

type nodeListeners struct {
	at  time.Time
	all []*agentv1.Listener
}

// Manager keeps the settings and block list in memory: every node's
// firewall config asks for them.
type Manager struct {
	st  *store.Store
	log *slog.Logger
	// OnChange runs after anything that changes nodes' firewalls.
	OnChange func()
	// ProtectedIn reports whether a source range covers an address that may
	// never be blocked (cluster nodes, the controller).
	ProtectedIn func(p netip.Prefix) bool
	// AutoBan returns the ban for a source over a guard's limit on a port
	// other than SSH (nil: SSH only; Phase 25 adds load balancers).
	AutoBan func(node string, port uint32) (time.Duration, string, bool)

	mu        sync.RWMutex
	ssh       SSH
	blocks    []store.FirewallBlock
	listeners map[string]nodeListeners // by node name
	now       func() time.Time
}

func New(st *store.Store, log *slog.Logger) *Manager {
	return &Manager{st: st, log: log, ssh: DefaultSSH(), listeners: map[string]nodeListeners{}, now: time.Now}
}

// Load reads the SSH settings and the block list. The first time, SSH
// rules of the original "default" policy move into the settings and that
// policy goes (it only held SSH).
func (m *Manager) Load(ctx context.Context) error {
	ssh := DefaultSSH()
	raw, ok, err := m.st.GetSetting(ctx, SettingSSH)
	if err != nil {
		return err
	}
	if ok {
		if err := json.Unmarshal([]byte(raw), &ssh); err != nil {
			return fmt.Errorf("ssh settings: %w", err)
		}
	} else if ssh, err = m.migrateDefaultPolicy(ctx, ssh); err != nil {
		return err
	}
	blocks, err := m.st.ListFirewallBlocks(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.ssh, m.blocks = ssh, blocks
	m.mu.Unlock()
	return nil
}

func (m *Manager) migrateDefaultPolicy(ctx context.Context, ssh SSH) (SSH, error) {
	ps, err := m.st.ListFirewallPolicies(ctx)
	if err != nil {
		return ssh, err
	}
	for _, p := range ps {
		if p.ID != "fwp_default" || len(p.Rules) != 1 || len(p.Targets) != 1 || p.Targets[0] != "*" {
			continue
		}
		r := p.Rules[0]
		if r.Protocol != "tcp" || r.Ports != "22" || slices.Contains(r.Sources, firewall.SourceCluster) {
			continue
		}
		ssh.Sources = append([]string{}, r.Sources...)
		if err := m.st.DeleteFirewallPolicy(ctx, p.ID); err != nil {
			return ssh, err
		}
		m.log.Info("host firewall: SSH is built in now; the default policy's SSH rule moved to the SSH settings", "sources", ssh.Sources)
	}
	b, _ := json.Marshal(ssh)
	return ssh, m.st.SetSetting(ctx, SettingSSH, string(b))
}

// SSH returns the SSH settings.
func (m *Manager) SSH() SSH {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ssh
}

// SetSSH saves the SSH settings.
func (m *Manager) SetSSH(ctx context.Context, s SSH) (SSH, error) {
	if err := s.Validate(); err != nil {
		return s, err
	}
	b, _ := json.Marshal(s)
	if err := m.st.SetSetting(ctx, SettingSSH, string(b)); err != nil {
		return s, err
	}
	m.mu.Lock()
	m.ssh = s
	m.mu.Unlock()
	m.changed()
	return s, nil
}

func (m *Manager) changed() {
	if m.OnChange != nil {
		m.OnChange()
	}
}

// SSHPorts are the ports SSH is open on for a node: 22, the extra ones and
// those sshd listens on there.
func (m *Manager) SSHPorts(node string) []int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ports := append([]int{22}, m.ssh.Ports...)
	for _, l := range m.listeners[node].all {
		if l.GetProtocol() == "tcp" && l.GetProcess() == "sshd" && !isLocal(l.GetAddress()) {
			ports = append(ports, int(l.GetPort()))
		}
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}

func portList(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

// For returns what the host firewall adds to a node's firewall: the SSH
// rule, its guard and the node's blocks. clusterSources are the nodes'
// public addresses (never limited).
func (m *Manager) For(node string, clusterSources []string) (rules []*agentv1.FirewallRule, guards []*agentv1.FirewallGuard, blocks []*agentv1.FirewallBlock) {
	ports := m.SSHPorts(node)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range ports {
		rules = append(rules, &agentv1.FirewallRule{Id: RuleSSH, Protocol: "tcp", Ports: strconv.Itoa(p), Sources: slices.Clone(m.ssh.Sources), Description: "SSH (built-in)"})
	}
	if m.ssh.Rate > 0 {
		exempt := slices.Clone(m.ssh.Exempt)
		for _, c := range clusterSources {
			if _, err := netip.ParseAddr(c); err == nil {
				exempt = append(exempt, c)
			}
		}
		guards = append(guards, &agentv1.FirewallGuard{Id: RuleSSH, Protocol: "tcp", Ports: portList(ports), Rate: uint32(m.ssh.Rate), Unit: "minute",
			Burst: uint32(max(m.ssh.Burst, 1)), Exempt: exempt})
	}
	now := m.now()
	for _, b := range m.blocks {
		if b.Node != "" && b.Node != node {
			continue
		}
		var left uint32
		if b.ExpiresAt != nil {
			d := b.ExpiresAt.Sub(now)
			if d <= 0 {
				continue
			}
			left = uint32(d/time.Second) + 1
		}
		blocks = append(blocks, &agentv1.FirewallBlock{Source: b.Source, TimeoutSeconds: left})
	}
	return rules, guards, blocks
}

// Blocks lists the blocks (expired ones are removed every 30 seconds).
func (m *Manager) Blocks() []store.FirewallBlock {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.blocks)
}

// ErrProtected means the source covers an address that must stay reachable.
var ErrProtected = errors.New("protected address")

// BlockInput is a block to add.
type BlockInput struct {
	Source string
	Node   string        // "" = every node
	For    time.Duration // 0 = until removed
	Reason string
	By     string
	Auto   bool
}

// Block adds (or replaces) a block.
func (m *Manager) Block(ctx context.Context, in BlockInput) (store.FirewallBlock, error) {
	p, err := firewall.ValidateBlock(in.Source)
	if err != nil {
		return store.FirewallBlock{}, err
	}
	if m.ProtectedIn != nil && m.ProtectedIn(p) {
		return store.FirewallBlock{}, fmt.Errorf("%w: %s covers a cluster node", ErrProtected, in.Source)
	}
	src := p.String()
	if p.IsSingleIP() {
		src = p.Addr().String()
	}
	now := m.now().UTC()
	b := store.FirewallBlock{ID: "blk_" + randHex(6), Source: src, Node: in.Node, Reason: in.Reason, CreatedBy: in.By, Auto: in.Auto, CreatedAt: now}
	if in.For > 0 {
		t := now.Add(in.For).Truncate(time.Second)
		b.ExpiresAt = &t
	}
	if err := m.st.PutFirewallBlock(ctx, b); err != nil {
		return b, err
	}
	if err := m.reload(ctx); err != nil {
		return b, err
	}
	for _, x := range m.Blocks() {
		if x.Source == b.Source && x.Node == b.Node {
			b = x
		}
	}
	m.changed()
	return b, nil
}

// Unblock removes a block.
func (m *Manager) Unblock(ctx context.Context, id string) error {
	if err := m.st.DeleteFirewallBlock(ctx, id); err != nil {
		return err
	}
	if err := m.reload(ctx); err != nil {
		return err
	}
	m.changed()
	return nil
}

func (m *Manager) reload(ctx context.Context) error {
	blocks, err := m.st.ListFirewallBlocks(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.blocks = blocks
	m.mu.Unlock()
	return nil
}

// Run removes expired blocks every 30 seconds.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n, err := m.st.DeleteExpiredFirewallBlocks(ctx, m.now())
		if err != nil {
			m.log.Error("host firewall: expire blocks", "err", err)
			continue
		}
		if n > 0 {
			if err := m.reload(ctx); err == nil {
				m.changed()
			}
		}
	}
}

// OnHeartbeat records a node's listening sockets and bans sources that went
// over a guard's limit.
func (m *Manager) OnHeartbeat(node store.Node, hb *agentv1.Heartbeat) {
	ns := hb.GetNetwork()
	if ns == nil {
		return
	}
	if ns.GetListenersSent() {
		before := m.SSHPorts(node.Name)
		m.mu.Lock()
		m.listeners[node.Name] = nodeListeners{at: m.now().UTC(), all: ns.GetListeners()}
		m.mu.Unlock()
		if !slices.Equal(before, m.SSHPorts(node.Name)) {
			m.log.Info("host firewall: sshd ports changed", "node", node.Name, "ports", m.SSHPorts(node.Name))
			m.changed() // open the new port
		}
	}
	for _, d := range ns.GetDrops() {
		if d.GetDirection() != "limit" {
			continue
		}
		dur, reason, ok := m.banFor(node.Name, d.GetPort())
		if !ok || m.blocked(d.GetSrc(), node.Name) {
			continue
		}
		go func(src string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := m.Block(ctx, BlockInput{Source: src, Node: node.Name, For: dur, Reason: reason, By: "auto-ban", Auto: true}); err != nil && !errors.Is(err, ErrProtected) {
				m.log.Error("host firewall: auto-ban", "node", node.Name, "source", src, "err", err)
			} else if err == nil {
				m.log.Warn("host firewall: banned a source over its limit", "node", node.Name, "source", src, "port", d.GetPort(), "for", dur)
			}
		}(d.GetSrc())
	}
}

func (m *Manager) banFor(node string, port uint32) (time.Duration, string, bool) {
	if slices.Contains(m.SSHPorts(node), int(port)) {
		s := m.SSH()
		if !s.AutoBan {
			return 0, "", false
		}
		return time.Duration(s.BanMinutes) * time.Minute, fmt.Sprintf("SSH: over %d new connections a minute", s.Rate), true
	}
	if m.AutoBan != nil {
		return m.AutoBan(node, port)
	}
	return 0, "", false
}

// blocked reports whether a source already has a block on node (or all).
func (m *Manager) blocked(src, node string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.blocks {
		if b.Source == src && (b.Node == "" || b.Node == node) {
			return true
		}
	}
	return false
}

// Listeners returns a node's listening sockets with the firewall's verdict
// for each, given the node's firewall (nil = no host firewall).
func (m *Manager) Listeners(node string, fw *agentv1.Firewall, wireguardPort uint32) ([]Listener, time.Time) {
	m.mu.RLock()
	nl := m.listeners[node]
	m.mu.RUnlock()
	out := make([]Listener, 0, len(nl.all))
	for _, l := range nl.all {
		v := Listener{Protocol: l.GetProtocol(), Address: l.GetAddress(), Port: l.GetPort(), Process: l.GetProcess()}
		v.Verdict, v.Rule = Verdict(l, fw, wireguardPort)
		out = append(out, v)
	}
	return out, nl.at
}

// private are the mesh, container and service networks (§8).
var private = []netip.Prefix{netip.MustParsePrefix("10.90.0.0/16"), netip.MustParsePrefix("10.91.0.0/16"), netip.MustParsePrefix("10.92.0.0/16")}

func isLocal(addr string) bool {
	a, err := netip.ParseAddr(addr)
	return err == nil && (a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast())
}

// Verdict says how a listening socket is reached through the firewall.
func Verdict(l *agentv1.Listener, fw *agentv1.Firewall, wireguardPort uint32) (verdict, rule string) {
	a, err := netip.ParseAddr(l.GetAddress())
	switch {
	case err != nil:
		return "blocked", ""
	case a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast():
		return "local", ""
	}
	for _, p := range private {
		if p.Contains(a) {
			return "private", ""
		}
	}
	if fw == nil {
		return "open", "no host firewall"
	}
	if l.GetProtocol() == "udp" && l.GetPort() == wireguardPort {
		return "restricted", "builtin:wireguard"
	}
	verdict = "blocked"
	for _, r := range fw.GetRules() {
		if r.GetProtocol() != l.GetProtocol() && r.GetProtocol() != "any" || !portMatches(r.GetPorts(), l.GetPort()) {
			continue
		}
		if len(r.GetSources()) == 0 {
			return "open", r.GetId()
		}
		verdict, rule = "restricted", r.GetId()
	}
	return verdict, rule
}

func portMatches(ports string, port uint32) bool {
	if ports == "" {
		return true
	}
	for _, part := range strings.Split(ports, ",") {
		lo, hi, ok := strings.Cut(strings.TrimSpace(part), "-")
		l, err := strconv.ParseUint(lo, 10, 16)
		if err != nil {
			continue
		}
		h := l
		if ok {
			if h, err = strconv.ParseUint(hi, 10, 16); err != nil {
				continue
			}
		}
		if uint64(port) >= l && uint64(port) <= h {
			return true
		}
	}
	return false
}

// SortBlocks orders blocks newest first.
func SortBlocks(bs []store.FirewallBlock) {
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].CreatedAt.After(bs[j].CreatedAt) })
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
