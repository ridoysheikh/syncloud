package dbs

import (
	"context"
	"time"

	"syncloud/internal/store"
)

// View is a database as the API shows it.
type View struct {
	ID          string          `json:"id"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Name        string          `json:"name"`
	Engine      string          `json:"engine"`
	Version     string          `json:"version"`
	Spec        Spec            `json:"spec"`
	State       State           `json:"state"`
	Status      string          `json:"status"` // why the operator is stuck ("" = fine)
	Health      string          `json:"health"` // healthy | degraded | starting | down | deleting
	Deleting    bool            `json:"deleting"`
	Host        string          `json:"host"`     // read-write
	ReadHost    string          `json:"readHost"` // read-only
	Port        int             `json:"port"`
	Members     []Member        `json:"members"`
	Usage       Usage           `json:"usage"`
	Autoscale   AutoscaleStatus `json:"autoscale"`
	// FailoverReady: every sentinel knows every replica and its peers, so a
	// failed primary is replaced (false without replicas).
	FailoverReady bool      `json:"failoverReady"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Member is one container of a database.
type Member struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"` // m0, s1…
	Kind     string    `json:"kind"`
	Node     string    `json:"node"`
	NodeID   string    `json:"nodeId"`
	State    string    `json:"state"`
	IP       string    `json:"ip"`
	Role     string    `json:"role"` // primary | replica | sentinel | ""
	LinkUp   bool      `json:"linkUp"`
	LagBytes int64     `json:"lagBytes"`
	Memory   int64     `json:"usedMemoryBytes"`
	Ops      float64   `json:"opsPerSec"`
	Clients  int64     `json:"clients"`
	CPU      float64   `json:"cpuPercent"`
	Error    string    `json:"error,omitempty"`
	Since    time.Time `json:"createdAt"`
}

// Usage is the primary's latest figures.
type Usage struct {
	UsedMemoryBytes int64   `json:"usedMemoryBytes"`
	MaxMemoryBytes  int64   `json:"maxMemoryBytes"`
	Keys            int64   `json:"keys"`
	OpsPerSec       float64 `json:"opsPerSec"`
	Clients         int64   `json:"clients"`
	HitRate         float64 `json:"hitRate"` // %; -1 before any lookup
	UptimeSeconds   int64   `json:"uptimeSeconds"`
}

// View builds a database's API view.
func (m *Manager) View(ctx context.Context, d store.Database) View {
	spec, _ := parseSpec(d.Spec)
	st := parseState(d.State)
	v := View{ID: d.ID, Project: d.Project, Environment: d.Environment, Name: d.Name, Engine: d.Engine, Version: d.Version,
		Spec: spec, State: st, Status: d.Status, Deleting: d.Deleting, Host: Host(d), ReadHost: ReadHost(d), Port: Port,
		Members: []Member{}, Autoscale: m.Autoscale(d.ID), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
	members, _ := m.st.DatabaseMembers(ctx, d.ID)
	dataRunning, data, sentRunning, sents := 0, 0, 0, 0
	primaryUp, primaryProbed := false, false
	sentinelsReady := 0
	for _, mb := range members {
		x := Member{ID: mb.ID, Name: memberName(mb), Kind: mb.Kind, NodeID: mb.NodeID, State: mb.State, IP: mb.IP, Error: mb.Error, Since: mb.CreatedAt}
		if n, ok := m.nodes.Get(mb.NodeID); ok {
			x.Node = n.Name
		}
		l := m.liveOf(mb.ID)
		if mb.Kind == KindSentinel {
			sents++
			if mb.State == store.TaskRunning {
				sentRunning++
				x.Role = "sentinel"
				if l != nil && l.Error == "" && l.KnownReplicas >= st.Replicas && l.KnownPeers >= Sentinels-1 {
					sentinelsReady++
				}
			}
		} else {
			data++
			if mb.State == store.TaskRunning {
				dataRunning++
			}
			if mb.Ordinal == st.Primary && l != nil {
				primaryProbed = true
			}
			if l != nil && l.Info != nil {
				x.Memory, x.Ops, x.Clients = l.Info.Int("used_memory"), l.Info.Float("instantaneous_ops_per_sec"), l.Info.Int("connected_clients")
				x.CPU, x.LinkUp, x.LagBytes = l.Info.Float("_cpu_percent"), l.LinkUp, l.LagBytes
				x.Role = "replica"
				if l.Role == "master" {
					x.Role = "primary"
				}
				if mb.Ordinal == st.Primary && mb.State == store.TaskRunning && l.Role == "master" {
					primaryUp = true
					hits, misses := l.Info.Float("keyspace_hits"), l.Info.Float("keyspace_misses")
					v.Usage = Usage{UsedMemoryBytes: x.Memory, MaxMemoryBytes: l.Info.Int("maxmemory"), Keys: l.Info.Keys(),
						OpsPerSec: x.Ops, Clients: x.Clients, HitRate: -1, UptimeSeconds: l.Info.Int("uptime_in_seconds")}
					if hits+misses > 0 {
						v.Usage.HitRate = hits / (hits + misses) * 100
					}
				}
			}
			if l != nil && l.Error != "" && x.Error == "" {
				x.Error = l.Error
			}
		}
		v.Members = append(v.Members, x)
	}
	wantSent := 0
	if spec.HasSentinels() {
		wantSent = Sentinels
	}
	switch {
	case d.Deleting:
		v.Health = "deleting"
	case data == 0 || (dataRunning == 0 && time.Since(d.CreatedAt) < 3*time.Minute):
		v.Health = "starting"
	case !primaryUp && !primaryProbed && dataRunning > 0:
		v.Health = "checking" // no probe since the controller started
	case !primaryUp:
		v.Health = "down"
	case dataRunning < 1+st.Replicas || sentRunning < wantSent || sents < wantSent:
		v.Health = "degraded"
	case st.Replicas > 0 && sentinelsReady < wantSent:
		// Up, but Sentinel does not know the whole topology yet (it learns
		// replicas within ~10s): a failover could not happen now.
		v.Health = "degraded"
	default:
		v.Health = "healthy"
	}
	v.FailoverReady = st.Replicas > 0 && wantSent > 0 && sentinelsReady >= wantSent
	return v
}

// List returns every database's view.
func (m *Manager) List(ctx context.Context) ([]View, error) {
	dbs, err := m.st.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(dbs))
	for _, d := range dbs {
		out = append(out, m.View(ctx, d))
	}
	return out, nil
}

// Credentials are what clients need to connect.
type Credentials struct {
	Host     string `json:"host"`
	ReadHost string `json:"readHost"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	URL      string `json:"url"`     // redis://default:…@host:6379
	ReadURL  string `json:"readUrl"` // read-only endpoint
}

// Credentials returns the app user's connection details.
func (m *Manager) Credentials(ctx context.Context, d store.Database) (Credentials, error) {
	sec, err := m.secrets(d)
	if err != nil {
		return Credentials{}, err
	}
	c := Credentials{Host: Host(d), ReadHost: ReadHost(d), Port: Port, Username: "default", Password: sec.Password}
	c.URL = "redis://default:" + sec.Password + "@" + c.Host + ":6379"
	c.ReadURL = "redis://default:" + sec.Password + "@" + c.ReadHost + ":6379"
	return c, nil
}

// Events lists a database's scaling and failover events, newest first.
func (m *Manager) Events(ctx context.Context, id string) ([]store.DatabaseEvent, error) {
	ev, err := m.st.DatabaseEvents(ctx, id, 200)
	if ev == nil {
		ev = []store.DatabaseEvent{}
	}
	return ev, err
}
