// Package netcfg applies the controller's mesh configuration on a node (§8):
// the WireGuard interface and peers, routes for container subnets, the Docker
// network "syncloud" with the node's subnet, and the nftables rules that keep
// container traffic un-NATed inside the mesh and masqueraded to the internet.
package netcfg

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/protobuf/proto"

	"syncloud/internal/agent/dnssrv"
	"syncloud/internal/agent/docker"
	"syncloud/internal/firewall"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

const (
	// Interface is the WireGuard interface (not "wg0", which users often have).
	Interface = "wg-syncloud"
	// Bridge is the Linux bridge of the Docker network Network.
	Bridge = "syncloud0"
	// Network is the Docker network every task joins.
	Network = "syncloud"
	// ProbePort is a TCP port on the node's mesh address used to measure RTT.
	ProbePort = "7444"
	keyFile   = "wg.key"
	mtu       = 1420
)

// Manager applies the latest NetworkConfig and reports status.
type Manager struct {
	dataDir string
	docker  *docker.Client
	log     *slog.Logger
	key     wgtypes.Key
	enabled bool

	mu      sync.Mutex
	latest  *agentv1.NetworkConfig
	applied uint64
	lastErr string
	mode    string
	rtt     map[string]float64 // peer public key -> ms
	kick    chan struct{}
	drift   uint64

	// Commit-confirm (§8.3): a firewall change must be confirmed by the
	// controller, or it is rolled back to the last confirmed firewall.
	confirmedFW *agentv1.Firewall
	pendingFW   *agentv1.Firewall
	pendingGen  uint64
	deadline    time.Time
	rejectedGen uint64

	sys platform // OS-specific state (userspace WireGuard device, probe listener)

	discovery *agentv1.Discovery // service directory (VIPs and DNS records)
	dns       *dnssrv.Server

	// Firewall counters (§8.3). The table is replaced on every change, which
	// resets nftables counters, so totals are carried over here.
	countMu  sync.Mutex
	ruleBase map[string]firewall.Counts
	dropSeen map[firewall.DropKey]uint64 // live set counters at the last read
	dropNew  map[firewall.DropKey]uint64 // not reported yet
	secErr   string
}

// New loads (or creates) the node's WireGuard key. With enabled false the
// manager does nothing and the node stays out of the mesh.
func New(dataDir string, d *docker.Client, log *slog.Logger, enabled bool) (*Manager, error) {
	m := &Manager{dataDir: dataDir, docker: d, log: log, enabled: enabled, rtt: map[string]float64{}, kick: make(chan struct{}, 1),
		dns: dnssrv.New(DNSZone, log), ruleBase: map[string]firewall.Counts{}, dropSeen: map[firewall.DropKey]uint64{}, dropNew: map[firewall.DropKey]uint64{}}
	if !enabled {
		return m, nil
	}
	path := filepath.Join(dataDir, keyFile)
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		m.key, err = wgtypes.ParseKey(strings.TrimSpace(string(b)))
		if err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
		if m.key, err = wgtypes.GeneratePrivateKey(); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(m.key.String()+"\n"), 0o600); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return m, nil
}

// PublicKey is sent in Hello ("" when networking is disabled).
func (m *Manager) PublicKey() string {
	if !m.enabled {
		return ""
	}
	return m.key.PublicKey().String()
}

// DNSZone is the internal zone served to tasks (§8.1).
const DNSZone = "syncloud.internal"

// SubmitDiscovery installs a new service directory: DNS answers change at
// once, VIP rules on the next apply (immediately).
func (m *Manager) SubmitDiscovery(d *agentv1.Discovery) {
	if !m.enabled {
		return
	}
	recs := map[string][]string{}
	for _, r := range d.GetRecords() {
		recs[r.GetName()] = r.GetIps()
	}
	m.dns.SetRecords(recs)
	m.mu.Lock()
	m.discovery = d
	m.mu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) services() []*agentv1.VirtualService {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.discovery.GetServices()
}

func (m *Manager) security() *agentv1.SecurityPolicy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.discovery.GetSecurity()
}

// Kick re-applies soon, e.g. when a container starts and must join its
// security sets.
func (m *Manager) Kick() {
	if !m.enabled {
		return
	}
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Submit queues cfg; the newest config wins.
func (m *Manager) Submit(cfg *agentv1.NetworkConfig) {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	m.latest = cfg
	m.mu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Run applies configs as they arrive, re-applies every 30s to repair drift,
// and probes peers. It returns when ctx ends.
func (m *Manager) Run(ctx context.Context) {
	if !m.enabled {
		return
	}
	defer m.sys.close()
	defer m.dns.Close()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	probe := time.NewTicker(10 * time.Second)
	defer probe.Stop()
	confirm := time.NewTicker(2 * time.Second)
	defer confirm.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
			m.applyLatest(ctx)
		case <-t.C:
			m.applyLatest(ctx)
		case <-probe.C:
			m.probePeers(ctx)
		case <-confirm.C:
			if m.confirmExpired() {
				m.applyLatest(ctx)
			}
		}
	}
}

func (m *Manager) applyLatest(ctx context.Context) {
	m.mu.Lock()
	cfg := m.latest
	m.mu.Unlock()
	if cfg == nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	mode, err := m.apply(actx, cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	if mode != "" {
		m.mode = mode
	}
	if err != nil {
		if m.lastErr != err.Error() {
			m.log.Error("apply mesh config", "generation", cfg.GetGeneration(), "err", err)
		}
		m.lastErr = err.Error()
		return
	}
	if fw := m.firewallForLocked(cfg); !proto.Equal(fw, m.confirmedFW) && cfg.GetFirewall().GetConfirmTimeoutSeconds() > 0 && cfg.GetGeneration() != m.rejectedGen {
		if m.pendingGen != cfg.GetGeneration() {
			m.pendingFW, m.pendingGen = fw, cfg.GetGeneration()
			m.deadline = time.Now().Add(time.Duration(cfg.GetFirewall().GetConfirmTimeoutSeconds()) * time.Second)
		}
	}
	if m.applied != cfg.GetGeneration() {
		m.log.Info("mesh config applied", "generation", cfg.GetGeneration(), "address", cfg.GetNodeAddress(), "peers", len(cfg.GetPeers()), "mode", m.mode)
	}
	m.applied = cfg.GetGeneration()
	if cfg.GetGeneration() != m.rejectedGen {
		m.lastErr = ""
	}
}

func (m *Manager) probePeers(ctx context.Context) {
	m.mu.Lock()
	cfg := m.latest
	m.mu.Unlock()
	if cfg == nil {
		return
	}
	res := map[string]float64{}
	for _, p := range cfg.GetPeers() {
		if len(p.GetAllowedIps()) == 0 {
			continue
		}
		ip, _, _ := strings.Cut(p.GetAllowedIps()[0], "/")
		start := time.Now()
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, ProbePort))
		if err != nil {
			continue
		}
		res[p.GetPublicKey()] = float64(time.Since(start).Microseconds()) / 1000
		c.Close()
	}
	m.mu.Lock()
	m.rtt = res
	m.mu.Unlock()
}

// firewallFor picks the firewall to render: the latest one, unless that
// generation was rolled back.
func (m *Manager) firewallFor(cfg *agentv1.NetworkConfig) *agentv1.Firewall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.firewallForLocked(cfg)
}

func (m *Manager) firewallForLocked(cfg *agentv1.NetworkConfig) *agentv1.Firewall {
	if m.rejectedGen != 0 && cfg.GetGeneration() == m.rejectedGen {
		return m.confirmedFW
	}
	return cfg.GetFirewall()
}

// Confirm marks the pending firewall as good (the controller still hears us).
func (m *Manager) Confirm(gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingFW != nil && gen >= m.pendingGen {
		m.confirmedFW, m.pendingFW, m.pendingGen = m.pendingFW, nil, 0
	}
}

// confirmExpired rolls back an unconfirmed firewall change.
func (m *Manager) confirmExpired() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingFW == nil || time.Now().Before(m.deadline) {
		return false
	}
	m.log.Error("firewall change not confirmed by the controller; rolling back", "generation", m.pendingGen)
	m.rejectedGen = m.pendingGen
	m.pendingFW, m.pendingGen = nil, 0
	m.lastErr = "firewall change rolled back: the controller did not confirm it in time"
	return true
}

// Ready reports whether the "syncloud" Docker network exists with this
// node's subnet, so tasks can be started. Always nil when disabled.
func (m *Manager) Ready() error {
	if !m.enabled {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.applied == 0 {
		if m.lastErr != "" {
			return errors.New("private network not ready: " + m.lastErr)
		}
		return errors.New("private network not ready yet")
	}
	return nil
}

// Status is reported in every heartbeat (nil when disabled).
func (m *Manager) Status() *agentv1.NetworkStatus {
	if !m.enabled {
		return nil
	}
	m.mu.Lock()
	st := &agentv1.NetworkStatus{Generation: m.applied, Error: m.lastErr, Mode: m.mode, FirewallPending: m.pendingFW != nil, DriftCorrections: m.drift}
	rtt := m.rtt
	m.mu.Unlock()
	st.Counters, st.Drops, st.SecurityError = m.takeCounters()
	for _, p := range m.peerStatus() {
		p.RttMs = rtt[p.GetPublicKey()]
		st.Peers = append(st.Peers, p)
	}
	return st
}

// collect records drop-set growth since the last read. Callers hold countMu.
func (m *Manager) collect() {
	_, drops, ok := readCounters()
	if !ok {
		return
	}
	for k, n := range drops {
		prev := m.dropSeen[k]
		if n < prev { // the element expired and came back
			prev = 0
		}
		if n > prev {
			m.dropNew[k] += n - prev
		}
	}
	m.dropSeen = drops
}

// replaced is called after the table was replaced: the counters read just
// before (rules) become part of the totals. Callers hold countMu.
func (m *Manager) replaced(rules map[string]firewall.Counts) {
	for k, c := range rules {
		b := m.ruleBase[k]
		b.Packets += c.Packets
		b.Bytes += c.Bytes
		m.ruleBase[k] = b
	}
	m.dropSeen = map[firewall.DropKey]uint64{}
}

// takeCounters returns cumulative rule counters and the drops since the
// last call.
func (m *Manager) takeCounters() ([]*agentv1.RuleCounter, []*agentv1.DropFlow, string) {
	m.countMu.Lock()
	defer m.countMu.Unlock()
	rules, _, ok := readCounters()
	if !ok {
		return nil, nil, m.secErr
	}
	m.collect()
	total := map[string]firewall.Counts{}
	for k, c := range m.ruleBase {
		total[k] = c
	}
	for k, c := range rules {
		t := total[k]
		t.Packets += c.Packets
		t.Bytes += c.Bytes
		total[k] = t
	}
	out := make([]*agentv1.RuleCounter, 0, len(total))
	for k, c := range total {
		out = append(out, &agentv1.RuleCounter{Id: k, Packets: c.Packets, Bytes: c.Bytes})
	}
	var drops []*agentv1.DropFlow
	for k, n := range m.dropNew {
		drops = append(drops, &agentv1.DropFlow{Direction: k.Direction, Src: k.Src, Dst: k.Dst, Protocol: k.Protocol, Port: k.Port, Packets: n})
		if len(drops) >= 500 {
			break
		}
	}
	m.dropNew = map[firewall.DropKey]uint64{}
	return out, drops, m.secErr
}
