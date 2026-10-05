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

	"syncloud/internal/agent/docker"
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

	sys platform // OS-specific state (userspace WireGuard device, probe listener)
}

// New loads (or creates) the node's WireGuard key. With enabled false the
// manager does nothing and the node stays out of the mesh.
func New(dataDir string, d *docker.Client, log *slog.Logger, enabled bool) (*Manager, error) {
	m := &Manager{dataDir: dataDir, docker: d, log: log, enabled: enabled, rtt: map[string]float64{}, kick: make(chan struct{}, 1)}
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
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	probe := time.NewTicker(10 * time.Second)
	defer probe.Stop()
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
	if m.applied != cfg.GetGeneration() || m.lastErr != "" {
		m.log.Info("mesh config applied", "generation", cfg.GetGeneration(), "address", cfg.GetNodeAddress(), "peers", len(cfg.GetPeers()), "mode", m.mode)
	}
	m.applied, m.lastErr = cfg.GetGeneration(), ""
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

// Status is reported in every heartbeat (nil when disabled).
func (m *Manager) Status() *agentv1.NetworkStatus {
	if !m.enabled {
		return nil
	}
	m.mu.Lock()
	st := &agentv1.NetworkStatus{Generation: m.applied, Error: m.lastErr, Mode: m.mode}
	rtt := m.rtt
	m.mu.Unlock()
	for _, p := range m.peerStatus() {
		p.RttMs = rtt[p.GetPublicKey()]
		st.Peers = append(st.Peers, p)
	}
	return st
}
