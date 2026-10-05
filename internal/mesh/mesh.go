// Package mesh runs the controller side of private networking (§8): it
// allocates each node a WireGuard address and a container subnet (IPAM, §8.2)
// and sends every agent its view of the full mesh.
package mesh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"syncloud/internal/agentgw"
	"syncloud/internal/events"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/store"
)

// Address plan (§8).
var (
	MeshCIDR      = netip.MustParsePrefix("10.90.0.0/16")
	ContainerCIDR = netip.MustParsePrefix("10.91.0.0/16")
	ServiceCIDR   = netip.MustParsePrefix("10.92.0.0/16")
)

const (
	// ListenPort is the WireGuard UDP port on every node.
	ListenPort = 51820
	// ControllerNode always gets mesh index 1 (10.90.0.1).
	ControllerNode = "ctl-0"
	// Cooldown keeps released indexes out of the pool.
	Cooldown = time.Hour
	// TopicUpdated carries a NodeView when a node's mesh state changes.
	TopicUpdated = "mesh.updated"
)

// MeshAddr maps a mesh index to its address (10.90.hi.lo).
func MeshAddr(i int) netip.Addr {
	b := MeshCIDR.Addr().As4()
	b[2], b[3] = byte(i>>8), byte(i)
	return netip.AddrFrom4(b)
}

// Subnet maps a subnet index to its /24 (10.91.i.0/24).
func Subnet(i int) netip.Prefix {
	b := ContainerCIDR.Addr().As4()
	b[2] = byte(i)
	return netip.PrefixFrom(netip.AddrFrom4(b), 24)
}

func meshPool(nodeName string) store.IndexPool {
	p := store.IndexPool{Kind: "mesh", Min: 2, Max: 0xfffe, Skip: func(i int) bool { return i&0xff == 0 || i&0xff == 0xff }}
	if nodeName == ControllerNode {
		p.Min, p.Prefer = 1, 1
	}
	return p
}

var subnetPool = store.IndexPool{Kind: "subnet", Min: 1, Max: 254}

// PeerView is one peer link as reported by an agent.
type PeerView struct {
	NodeID        string     `json:"nodeId"`
	Name          string     `json:"name"`
	Endpoint      string     `json:"endpoint"`
	LastHandshake *time.Time `json:"lastHandshake"`
	RxBytes       uint64     `json:"rxBytes"`
	TxBytes       uint64     `json:"txBytes"`
	RTTMillis     float64    `json:"rttMs"`
}

// NodeView is a node's mesh state for the API.
type NodeView struct {
	NodeID          string     `json:"nodeId"`
	Name            string     `json:"name"`
	Address         string     `json:"address"`
	Subnet          string     `json:"subnet"`
	Endpoint        string     `json:"endpoint"`
	PublicKey       string     `json:"publicKey"`
	Mode            string     `json:"mode"`
	Generation      uint64     `json:"generation"`
	AppliedGen      uint64     `json:"appliedGeneration"`
	Error           string     `json:"error"`
	Peers           []PeerView `json:"peers"`
	StatusUpdatedAt *time.Time `json:"statusUpdatedAt"`
}

// PublicIP returns the controller's public address (for ctl-0's endpoint).
type PublicIP func(ctx context.Context) (string, error)

type Manager struct {
	st       *store.Store
	gw       *agentgw.Gateway
	bus      *events.Bus
	log      *slog.Logger
	publicIP PublicIP
	gen      atomic.Uint64

	mu     sync.Mutex
	status map[string]*agentv1.NetworkStatus // by node ID
	seen   map[string]time.Time
}

func NewManager(st *store.Store, gw *agentgw.Gateway, bus *events.Bus, log *slog.Logger, publicIP PublicIP) *Manager {
	m := &Manager{st: st, gw: gw, bus: bus, log: log, publicIP: publicIP, status: map[string]*agentv1.NetworkStatus{}, seen: map[string]time.Time{}}
	m.gen.Store(uint64(time.Now().UnixNano())) // increases across restarts
	return m
}

func (m *Manager) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnConnect: m.onConnect, OnHeartbeat: m.onHeartbeat}
}

// Run rebroadcasts when nodes are removed.
func (m *Manager) Run(ctx context.Context) {
	sub := m.bus.Subscribe(16, "node.removed")
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sub.C:
			if !ok {
				return
			}
			m.broadcast(ctx, "")
		}
	}
}

func (m *Manager) onConnect(c agentgw.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := m.st.AllocateNodeNetwork(ctx, c.Node.ID, meshPool(c.Node.Name), subnetPool, Cooldown, time.Now())
	if err != nil {
		m.log.Error("allocate mesh addresses", "node", c.Node.Name, "err", err)
		return
	}
	info := c.Hello.GetInfo()
	key := info.GetWireguardPublicKey()
	if key == "" {
		m.log.Warn("agent has no WireGuard key; it is not part of the mesh", "node", c.Node.Name)
		return
	}
	host := info.GetAdvertiseAddress()
	if host == "" {
		host = c.Remote
	}
	if ip, err := netip.ParseAddr(host); err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		host = "" // the local agent: peers need the controller's public address
		if m.publicIP != nil {
			host, _ = m.publicIP(ctx)
		}
	}
	endpoint := ""
	if host != "" {
		endpoint = net.JoinHostPort(host, fmt.Sprint(ListenPort))
	}
	changed, err := m.st.SetNodeWireGuard(ctx, c.Node.ID, key, endpoint, time.Now())
	if err != nil {
		m.log.Error("store WireGuard key", "node", c.Node.Name, "err", err)
		return
	}
	if changed {
		m.log.Info("mesh member updated", "node", c.Node.Name, "endpoint", endpoint)
		m.broadcast(ctx, "")
	} else {
		m.broadcast(ctx, c.Node.ID)
	}
}

// broadcast sends every connected node (or only onlyID) its mesh view.
func (m *Manager) broadcast(ctx context.Context, onlyID string) {
	all, err := m.st.ListNodeNetworks(ctx)
	if err != nil {
		m.log.Error("list mesh members", "err", err)
		return
	}
	gen := m.gen.Add(1)
	for _, self := range all {
		if onlyID != "" && self.NodeID != onlyID {
			continue
		}
		cfg := ConfigFor(self, all, gen)
		err := m.gw.Send(self.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Network{Network: cfg}})
		if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
			m.log.Warn("send mesh config", "node", self.NodeName, "err", err)
		}
	}
}

// ConfigFor builds self's view: every other member with a key is a peer.
func ConfigFor(self store.NodeNetwork, all []store.NodeNetwork, gen uint64) *agentv1.NetworkConfig {
	cfg := &agentv1.NetworkConfig{
		Generation:      gen,
		NodeAddress:     netip.PrefixFrom(MeshAddr(self.MeshIndex), MeshCIDR.Bits()).String(),
		ContainerSubnet: Subnet(self.SubnetIndex).String(),
		ListenPort:      ListenPort,
		MeshCidr:        MeshCIDR.String(),
		ContainerCidr:   ContainerCIDR.String(),
		ServiceCidr:     ServiceCIDR.String(),
	}
	for _, p := range all {
		if p.NodeID == self.NodeID || p.PublicKey == "" {
			continue
		}
		cfg.Peers = append(cfg.Peers, &agentv1.Peer{
			NodeId: p.NodeID, Name: p.NodeName, PublicKey: p.PublicKey, Endpoint: p.Endpoint,
			AllowedIps: []string{netip.PrefixFrom(MeshAddr(p.MeshIndex), 32).String(), Subnet(p.SubnetIndex).String()},
		})
	}
	return cfg
}

func (m *Manager) onHeartbeat(node store.Node, hb *agentv1.Heartbeat) {
	ns := hb.GetNetwork()
	if ns == nil {
		return
	}
	m.mu.Lock()
	m.status[node.ID] = ns
	m.seen[node.ID] = time.Now().UTC()
	m.mu.Unlock()
}

// List returns every member's mesh state.
func (m *Manager) List(ctx context.Context) ([]NodeView, error) {
	all, err := m.st.ListNodeNetworks(ctx)
	if err != nil {
		return nil, err
	}
	byKey := map[string]store.NodeNetwork{}
	for _, n := range all {
		byKey[n.PublicKey] = n
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]NodeView, 0, len(all))
	for _, n := range all {
		v := NodeView{
			NodeID: n.NodeID, Name: n.NodeName, Address: MeshAddr(n.MeshIndex).String(), Subnet: Subnet(n.SubnetIndex).String(),
			Endpoint: n.Endpoint, PublicKey: n.PublicKey, Generation: m.gen.Load(), Peers: []PeerView{},
		}
		if st := m.status[n.NodeID]; st != nil {
			seen := m.seen[n.NodeID]
			v.Mode, v.AppliedGen, v.Error, v.StatusUpdatedAt = st.GetMode(), st.GetGeneration(), st.GetError(), &seen
			for _, p := range st.GetPeers() {
				pv := PeerView{Endpoint: p.GetEndpoint(), RxBytes: p.GetRxBytes(), TxBytes: p.GetTxBytes(), RTTMillis: p.GetRttMs()}
				if peer, ok := byKey[p.GetPublicKey()]; ok {
					pv.NodeID, pv.Name = peer.NodeID, peer.NodeName
				}
				if t := p.GetLastHandshakeUnix(); t > 0 {
					at := time.Unix(t, 0).UTC()
					pv.LastHandshake = &at
				}
				v.Peers = append(v.Peers, pv)
			}
		}
		out = append(out, v)
	}
	return out, nil
}
