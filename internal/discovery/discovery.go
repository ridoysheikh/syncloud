// Package discovery publishes the service directory to every node (§8.1,
// §8.6): stable service VIPs with their backends, and internal DNS records.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"syncloud/internal/agentgw"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/mesh"
	"syncloud/internal/secgroup"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

// Zone is the internal DNS zone.
const Zone = "syncloud.internal"

// VIPAddr maps a VIP index into 10.92.0.0/16.
func VIPAddr(i int) netip.Addr {
	b := mesh.ServiceCIDR.Addr().As4()
	b[2], b[3] = byte(i>>8), byte(i)
	return netip.AddrFrom4(b)
}

var vipPool = store.IndexPool{Kind: "vip", Min: 1, Max: 0xfffe, Skip: func(i int) bool { return i&0xff == 0 || i&0xff == 0xff }}

// ServiceName is a service's DNS name: <service>.<env>.<project>.syncloud.internal.
func ServiceName(sv store.Service) string {
	return sv.Name + "." + sv.Environment + "." + sv.Project + "." + Zone
}

// SearchDomains let tasks use short names within their environment.
func SearchDomains(sv store.Service) []string {
	return []string{sv.Environment + "." + sv.Project + "." + Zone, sv.Project + "." + Zone, Zone}
}

type Manager struct {
	st  *store.Store
	gw  *agentgw.Gateway
	wl  *workload.Manager
	log *slog.Logger
	gen atomic.Uint64

	// Security compiles security groups into the directory (§8.3).
	Security bool
	// Databases adds managed databases' VIPs and names (Phase 12); may be nil.
	Databases Directory

	mu   sync.Mutex
	last *agentv1.Discovery
	vips map[string]string // service ID -> VIP
	kick chan struct{}
}

// Directory contributes VIPs and DNS records from outside services.
type Directory interface {
	Directory(ctx context.Context, vip func(index int) string, pool store.IndexPool, cooldown time.Duration) ([]*agentv1.VirtualService, []*agentv1.DNSRecord)
}

func NewManager(st *store.Store, gw *agentgw.Gateway, wl *workload.Manager, log *slog.Logger) *Manager {
	m := &Manager{st: st, gw: gw, wl: wl, log: log, vips: map[string]string{}, kick: make(chan struct{}, 1)}
	m.gen.Store(uint64(time.Now().UnixNano()))
	return m
}

// Kick asks for a rebuild (debounced).
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnConnect: func(c agentgw.Conn) {
		m.mu.Lock()
		d := m.last
		m.mu.Unlock()
		if d != nil {
			m.send(c.Node.ID, d)
		}
	}}
}

// Records returns the DNS records last published (§8.1).
func (m *Manager) Records() []*agentv1.DNSRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last.GetRecords()
}

// Backends returns how many running backends each VIP has, by service ID.
func (m *Manager) Backends() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, vs := range m.last.GetServices() {
		n := 0
		for _, p := range vs.GetPorts() {
			n = max(n, len(p.GetBackends()))
		}
		out["svc_"+vs.GetId()] = n
	}
	return out
}

// VIP returns a service's virtual IP ("" before the first build).
func (m *Manager) VIP(serviceID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vips[serviceID]
}

// Run rebuilds on Kick (at most every 300ms) and every 30s.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	m.publish(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
			time.Sleep(300 * time.Millisecond) // coalesce bursts of task changes
		case <-t.C:
		}
		m.publish(ctx)
	}
}

func (m *Manager) publish(ctx context.Context) {
	d, vips, err := m.Build(ctx)
	if err != nil {
		m.log.Error("build service directory", "err", err)
		return
	}
	m.mu.Lock()
	same := m.last != nil && proto.Equal(withoutGen(m.last), withoutGen(d))
	if !same {
		d.Generation = m.gen.Add(1)
		m.last = d
	}
	m.vips = vips
	m.mu.Unlock()
	if same {
		return
	}
	nodes, err := m.st.ListNodes(ctx)
	if err != nil {
		return
	}
	for _, n := range nodes {
		m.send(n.ID, d)
	}
}

func withoutGen(d *agentv1.Discovery) *agentv1.Discovery {
	c := proto.Clone(d).(*agentv1.Discovery)
	c.Generation = 0
	return c
}

func (m *Manager) send(nodeID string, d *agentv1.Discovery) {
	err := m.gw.Send(nodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Discovery{Discovery: d}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("send service directory", "node", nodeID, "err", err)
	}
}

// Build computes the directory from the database.
func (m *Manager) Build(ctx context.Context) (*agentv1.Discovery, map[string]string, error) {
	d := &agentv1.Discovery{}
	vips := map[string]string{}
	svcs, err := m.st.ListServices(ctx)
	if err != nil {
		return nil, nil, err
	}
	tasks, err := m.st.ActiveTasks(ctx)
	if err != nil {
		return nil, nil, err
	}
	running := map[string][]store.Task{}
	for _, t := range tasks {
		if t.IP != "" && m.wl.Serving(ctx, t) {
			running[t.ServiceID] = append(running[t.ServiceID], t)
		}
	}
	now := time.Now()
	for _, sv := range svcs {
		if sv.Deleting {
			continue
		}
		spec, err := m.wl.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			continue
		}
		var ips []string
		for _, t := range running[sv.ID] {
			ips = append(ips, t.IP)
		}
		sort.Strings(ips)
		d.Records = append(d.Records, &agentv1.DNSRecord{Name: "tasks." + ServiceName(sv), Ips: ips})
		if len(spec.Ports) == 0 {
			continue // no ports: no VIP, only the tasks record
		}
		idx, err := m.st.EnsureServiceVIP(ctx, sv.ID, vipPool, mesh.Cooldown, now)
		if err != nil {
			m.log.Error("allocate VIP", "service", sv.ID, "err", err)
			continue
		}
		vip := VIPAddr(idx).String()
		vips[sv.ID] = vip
		d.Records = append(d.Records, &agentv1.DNSRecord{Name: ServiceName(sv), Ips: []string{vip}})
		vs := &agentv1.VirtualService{Id: strings.TrimPrefix(sv.ID, "svc_"), Vip: vip}
		for _, p := range spec.Ports {
			proto := "tcp"
			if p.Protocol == "udp" {
				proto = "udp"
			}
			vp := &agentv1.VirtualPort{Protocol: proto, Port: uint32(p.Container)}
			for _, t := range running[sv.ID] {
				port := p.Container
				if t.Revision != sv.Revision {
					if old, err := m.wl.SpecFor(ctx, sv.ID, t.Revision); err == nil {
						for _, op := range old.Ports {
							if op.Name == p.Name {
								port = op.Container
							}
						}
					}
				}
				vp.Backends = append(vp.Backends, fmt.Sprintf("%s:%d", t.IP, port))
			}
			sort.Strings(vp.Backends)
			vs.Ports = append(vs.Ports, vp)
		}
		d.Services = append(d.Services, vs)
	}
	if m.Databases != nil {
		svcs, recs := m.Databases.Directory(ctx, func(i int) string { return VIPAddr(i).String() }, vipPool, mesh.Cooldown)
		d.Services = append(d.Services, svcs...)
		d.Records = append(d.Records, recs...)
	}
	nets, err := m.st.ListNodeNetworks(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range nets {
		d.Records = append(d.Records, &agentv1.DNSRecord{Name: n.NodeName + ".node." + Zone, Ips: []string{mesh.MeshAddr(n.MeshIndex).String()}})
	}
	sort.Slice(d.Records, func(i, j int) bool { return d.Records[i].Name < d.Records[j].Name })
	if m.Security {
		model, err := secgroup.Load(ctx, m.st)
		if err != nil {
			return nil, nil, fmt.Errorf("security groups: %w", err)
		}
		d.Security = secgroup.Compile(model)
	}
	return d, vips, nil
}
