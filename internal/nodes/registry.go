// Package nodes tracks live node state (§5.6). Heartbeats and metrics stay in
// memory; only status transitions are written to SQLite (D5).
package nodes

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	"syncloud/internal/events"
	"syncloud/internal/store"
)

// Thresholds from §5.6.
const (
	HeartbeatInterval = 5 * time.Second
	SuspectAfter      = 20 * time.Second
	NotReadyAfter     = 60 * time.Second
	checkEvery        = 2 * time.Second
)

// TopicNode carries a View on every status change and heartbeat.
const TopicNode = "node.updated"

type Info struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Kernel        string `json:"kernel"`
	Arch          string `json:"arch"`
	CPUCores      int    `json:"cpuCores"`
	MemoryBytes   uint64 `json:"memoryBytes"`
	DiskBytes     uint64 `json:"diskBytes"`
	DockerVersion string `json:"dockerVersion"`
	AgentVersion  string `json:"agentVersion"`
}

type Metrics struct {
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryUsedBytes  uint64  `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64  `json:"memoryTotalBytes"`
	DiskUsedBytes    uint64  `json:"diskUsedBytes"`
	DiskTotalBytes   uint64  `json:"diskTotalBytes"`
	Load1            float64 `json:"load1"`
	Load5            float64 `json:"load5"`
	Load15           float64 `json:"load15"`
	NetRxBytes       uint64  `json:"netRxBytes"`
	NetTxBytes       uint64  `json:"netTxBytes"`
	UptimeSeconds    int64   `json:"uptimeSeconds"`
}

// View is a node as the API and dashboard see it.
type View struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	StatusAt   time.Time  `json:"statusAt"`
	Connected  bool       `json:"connected"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	Info       Info       `json:"info"`
	Metrics    *Metrics   `json:"metrics"`
	// Schedulable allows new tasks (§6.4: off for the controller by default).
	Schedulable bool `json:"schedulable"`
	// Draining moves the node's tasks to other nodes.
	Draining bool `json:"draining"`
}

type live struct {
	view      View
	connected int // number of open streams (a reconnect can briefly overlap)
}

type Registry struct {
	st  *store.Store
	bus *events.Bus
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	nodes map[string]*live
}

func NewRegistry(st *store.Store, bus *events.Bus, log *slog.Logger) *Registry {
	return &Registry{st: st, bus: bus, log: log, now: time.Now, nodes: map[string]*live{}}
}

// Load fills the registry from SQLite at startup. Every node starts
// disconnected; heartbeat timeouts then apply from each node's last-seen time,
// so nodes that don't reconnect go Suspect and then Not ready.
func (r *Registry) Load(ctx context.Context) error {
	list, err := r.st.ListNodes(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range list {
		v := View{ID: n.ID, Name: n.Name, Status: n.Status, StatusAt: n.StatusAt.UTC(), LastSeenAt: n.LastSeenAt, CreatedAt: n.CreatedAt.UTC(), Schedulable: n.Schedulable, Draining: n.Draining}
		_ = json.Unmarshal([]byte(n.Info), &v.Info)
		r.nodes[n.ID] = &live{view: v}
	}
	return nil
}

// Added registers a freshly joined node.
func (r *Registry) Added(n store.Node) {
	r.mu.Lock()
	v := View{ID: n.ID, Name: n.Name, Status: n.Status, StatusAt: n.StatusAt.UTC(), CreatedAt: n.CreatedAt.UTC(), Schedulable: n.Schedulable, Draining: n.Draining}
	r.nodes[n.ID] = &live{view: v}
	r.mu.Unlock()
	r.bus.Publish(TopicNode, v)
}

func (r *Registry) Removed(id string) {
	r.mu.Lock()
	delete(r.nodes, id)
	r.mu.Unlock()
	r.bus.Publish("node.removed", map[string]string{"id": id})
}

// Connected records an agent stream opening with its Hello info.
func (r *Registry) Connected(ctx context.Context, id string, info Info) {
	if b, err := json.Marshal(info); err == nil {
		if err := r.st.UpdateNodeInfo(ctx, id, string(b)); err != nil {
			r.log.Warn("store node info", "node", id, "err", err)
		}
	}
	r.update(ctx, id, func(l *live) {
		l.connected++
		l.view.Info = info
		r.seen(l)
	})
}

func (r *Registry) Heartbeat(ctx context.Context, id string, m Metrics) {
	r.update(ctx, id, func(l *live) {
		l.view.Metrics = &m
		r.seen(l)
	})
}

func (r *Registry) Disconnected(ctx context.Context, id string) {
	r.update(ctx, id, func(l *live) {
		if l.connected > 0 {
			l.connected--
		}
	})
}

// seen marks a sign of life; a node that was not Ready becomes Ready.
func (r *Registry) seen(l *live) {
	now := r.now()
	l.view.LastSeenAt = &now
	if l.view.Status != store.NodeReady {
		l.view.Status, l.view.StatusAt = store.NodeReady, now
	}
}

// update applies f, persists a status change if one happened, and publishes the new view.
func (r *Registry) update(ctx context.Context, id string, f func(*live)) {
	r.mu.Lock()
	l, ok := r.nodes[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	before := l.view.Status
	f(l)
	l.view.Connected = l.connected > 0
	v := l.view
	r.mu.Unlock()

	if v.Status != before {
		r.persistStatus(ctx, v)
	}
	r.bus.Publish(TopicNode, v)
}

func (r *Registry) persistStatus(ctx context.Context, v View) {
	seen := v.StatusAt
	if v.LastSeenAt != nil {
		seen = *v.LastSeenAt
	}
	if err := r.st.SetNodeStatus(ctx, v.ID, v.Status, v.StatusAt, seen); err != nil {
		r.log.Error("store node status", "node", v.ID, "err", err)
	}
	r.log.Info("node status", "node", v.Name, "status", v.Status)
}

// List returns all nodes sorted by name.
// SetSchedulable updates the live view after the store changed.
func (r *Registry) SetSchedulable(ctx context.Context, id string, on, draining bool) {
	r.update(ctx, id, func(l *live) { l.view.Schedulable, l.view.Draining = on, draining })
}

// Get returns one node's view.
func (r *Registry) Get(id string) (View, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.nodes[id]
	if !ok {
		return View{}, false
	}
	return l.view, true
}

func (r *Registry) List() []View {
	r.mu.Lock()
	out := make([]View, 0, len(r.nodes))
	for _, l := range r.nodes {
		out = append(out, l.view)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run applies the heartbeat timeouts until ctx ends.
func (r *Registry) Run(ctx context.Context) {
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.checkTimeouts(ctx)
		}
	}
}

func (r *Registry) checkTimeouts(ctx context.Context) {
	now := r.now()
	var changed []View
	r.mu.Lock()
	for _, l := range r.nodes {
		if l.view.LastSeenAt == nil { // pending: never connected
			continue
		}
		since := now.Sub(*l.view.LastSeenAt)
		next := l.view.Status
		switch {
		case since >= NotReadyAfter:
			next = store.NodeNotReady
		case since >= SuspectAfter:
			next = store.NodeSuspect
		}
		if next != l.view.Status {
			l.view.Status, l.view.StatusAt = next, now
			changed = append(changed, l.view)
		}
	}
	r.mu.Unlock()
	for _, v := range changed {
		r.persistStatus(ctx, v)
		r.bus.Publish(TopicNode, v)
	}
}
