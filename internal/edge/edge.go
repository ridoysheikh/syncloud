// Package edge runs Traefik replicas on edge nodes (§8.5): nodes of edge
// pools serve public traffic with the routes and certificates the controller
// serves them over the private network, and keep serving with the last
// configuration when the controller is down.
package edge

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"syncloud/internal/agentgw"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/mesh"
	"syncloud/internal/metrics"
	"syncloud/internal/nodes"
	"syncloud/internal/store"
	"syncloud/internal/traefik"
)

// TaskID is the edge Traefik's task ID on every edge node.
const TaskID = "sys-edge-traefik"

// AdminPort is Traefik's ping and metrics port on the node's mesh address.
const AdminPort = "8082"

// Config of the edge replicas.
type Config struct {
	Image        string
	TraefikToken string
	TokenHeader  string
	// ControllerURL is the controller's API on the private network.
	ControllerURL func() string
	// Edges lists the nodes of edge pools.
	Edges func() []nodes.View
	// Settings returns the global Traefik settings (§5.7).
	Settings func() traefik.Settings
	// DatabaseEntrypoints are the public database entrypoints (name -> address).
	DatabaseEntrypoints map[string]string
	// ServiceEntrypoints returns the public service ports' entrypoints
	// (Phase 15c; may be nil).
	ServiceEntrypoints func() map[string]string
}

// Health is the controller's view of one edge.
type Health struct {
	NodeID    string    `json:"nodeId"`
	Node      string    `json:"node"`
	MeshIP    string    `json:"meshIp"`
	Address   string    `json:"address"` // public (point DNS here)
	State     string    `json:"state"`   // healthy | unhealthy | starting
	Error     string    `json:"error,omitempty"`
	Container string    `json:"container"` // the Traefik task state on the node
	CheckedAt time.Time `json:"checkedAt"`
}

type Manager struct {
	st      *store.Store
	gw      *agentgw.Gateway
	metrics *metrics.Store
	names   metrics.ServiceNames
	cfg     Config
	log     *slog.Logger
	http    *http.Client

	mu     sync.Mutex
	health map[string]*Health
	state  map[string]string // node ID -> container state
}

func New(st *store.Store, gw *agentgw.Gateway, ms *metrics.Store, names metrics.ServiceNames, cfg Config, log *slog.Logger) *Manager {
	return &Manager{st: st, gw: gw, metrics: ms, names: names, cfg: cfg, log: log, http: &http.Client{Timeout: 3 * time.Second},
		health: map[string]*Health{}, state: map[string]string{}}
}

func (m *Manager) isEdge(nodeID string) bool {
	for _, v := range m.cfg.Edges() {
		if v.ID == nodeID {
			return true
		}
	}
	return false
}

func (m *Manager) meshIP(ctx context.Context, nodeID string) string {
	nn, err := m.st.NodeNetwork(ctx, nodeID)
	if err != nil {
		return ""
	}
	return mesh.MeshAddr(nn.MeshIndex).String()
}

// Spec is the edge Traefik for one node.
func (m *Manager) Spec(nodeName, meshIP string) *agentv1.TaskSpec {
	return &agentv1.TaskSpec{
		TaskId: TaskID, Name: "syncloud-edge-traefik", Image: m.cfg.Image,
		Command: traefik.WithStatic(append([]string{
			"--global.checkNewVersion=false",
			"--global.sendAnonymousUsage=false",
			"--entrypoints.web.address=:80",
			"--entrypoints.websecure.address=:443",
			"--entrypoints.websecure.http.tls=true",
			"--entrypoints.admin.address=" + net.JoinHostPort(meshIP, AdminPort),
			"--ping=true",
			"--ping.entrypoint=admin",
			"--metrics.prometheus=true",
			"--metrics.prometheus.entrypoint=admin",
			"--metrics.prometheus.addRoutersLabels=true",
			"--metrics.prometheus.addServicesLabels=true",
			"--metrics.prometheus.buckets=0.002,0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10",
			"--providers.http.endpoint=" + m.cfg.ControllerURL() + "/internal/traefik/config?edge=" + nodeName,
			"--providers.http.pollInterval=2s",
			fmt.Sprintf("--providers.http.headers.%s=%s", m.cfg.TokenHeader, m.cfg.TraefikToken),
			"--accesslog=true",
			"--accesslog.format=json",
		}, traefik.Entrypoints(m.entrypoints())...), m.settings()),
		NetworkMode: "host",
		System:      true,
	}
}

func (m *Manager) entrypoints() map[string]string {
	out := map[string]string{}
	for k, v := range m.cfg.DatabaseEntrypoints {
		out[k] = v
	}
	if m.cfg.ServiceEntrypoints != nil {
		for k, v := range m.cfg.ServiceEntrypoints() {
			out[k] = v
		}
	}
	return out
}

func (m *Manager) settings() traefik.Settings {
	if m.cfg.Settings == nil {
		return traefik.DefaultSettings()
	}
	return m.cfg.Settings()
}

// Refresh re-sends the replica to every edge node (after a settings change).
func (m *Manager) Refresh(ctx context.Context) {
	for _, v := range m.cfg.Edges() {
		m.apply(ctx, v.ID, v.Name)
	}
}

// Hooks start the replica when an edge node connects and remove it from
// nodes that left their edge pool.
func (m *Manager) Hooks() agentgw.Hooks {
	return agentgw.Hooks{
		OnConnect: func(c agentgw.Conn) {
			has := false
			for _, t := range c.Hello.GetTasks() {
				has = has || t.GetTaskId() == TaskID
			}
			switch {
			case m.isEdge(c.Node.ID):
				m.apply(context.Background(), c.Node.ID, c.Node.Name)
			case has:
				m.stop(c.Node.ID)
			}
		},
		OnTaskStatus: func(node store.Node, s *agentv1.TaskStatus) {
			if s.GetTaskId() != TaskID {
				return
			}
			m.mu.Lock()
			m.state[node.ID] = map[agentv1.TaskState]string{
				agentv1.TaskState_TASK_STATE_RUNNING: "running", agentv1.TaskState_TASK_STATE_STARTING: "starting",
				agentv1.TaskState_TASK_STATE_PULLING: "pulling", agentv1.TaskState_TASK_STATE_EXITED: "exited",
				agentv1.TaskState_TASK_STATE_FAILED: "failed", agentv1.TaskState_TASK_STATE_REMOVED: "removed",
			}[s.GetState()]
			m.mu.Unlock()
		},
	}
}

func (m *Manager) apply(ctx context.Context, nodeID, name string) {
	ip := m.meshIP(ctx, nodeID)
	if ip == "" {
		return // not on the private network yet
	}
	_ = m.gw.Send(nodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: m.Spec(name, ip)}}})
}

func (m *Manager) stop(nodeID string) {
	_ = m.gw.Send(nodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{TaskId: TaskID, TimeoutSeconds: 10, Remove: true}}})
	m.mu.Lock()
	delete(m.health, nodeID)
	delete(m.state, nodeID)
	m.mu.Unlock()
}

// Run keeps replicas on every edge node, removes them from former edges,
// checks their health and imports their metrics, every 10 seconds.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	known := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		edges := m.cfg.Edges()
		now := map[string]bool{}
		addrs := map[string]string{}
		if nets, err := m.st.ListNodeNetworks(ctx); err == nil {
			for _, n := range nets {
				if h, _, err := net.SplitHostPort(n.Endpoint); err == nil {
					addrs[n.NodeID] = h
				}
			}
		}
		for _, v := range edges {
			now[v.ID] = true
			m.apply(ctx, v.ID, v.Name)
			ip := m.meshIP(ctx, v.ID)
			h := &Health{NodeID: v.ID, Node: v.Name, MeshIP: ip, Address: addrs[v.ID], CheckedAt: time.Now().UTC(), State: "healthy"}
			m.mu.Lock()
			h.Container = m.state[v.ID]
			m.mu.Unlock()
			if err := m.ping(ctx, ip); err != nil {
				h.State, h.Error = "unhealthy", err.Error()
				if h.Container != "running" {
					h.State = "starting"
				}
			} else if m.metrics != nil {
				_ = m.metrics.ScrapeTraefikOnce(ctx, "http://"+net.JoinHostPort(ip, AdminPort)+"/metrics", v.Name, m.names)
			}
			m.mu.Lock()
			m.health[v.ID] = h
			m.mu.Unlock()
		}
		for id := range known {
			if !now[id] {
				m.stop(id)
			}
		}
		known = now
	}
}

func (m *Manager) ping(ctx context.Context, ip string) error {
	if ip == "" {
		return fmt.Errorf("no private address yet")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(ip, AdminPort)+"/ping", nil)
	if err != nil {
		return err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping: HTTP %d", resp.StatusCode)
	}
	return nil
}

// List returns every edge's health.
func (m *Manager) List() []Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Health, 0, len(m.health))
	for _, h := range m.health {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}
