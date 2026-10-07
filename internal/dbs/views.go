package dbs

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
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
	Standalone  bool            `json:"standalone"`
	Network     Network         `json:"network"`
	Public      PublicEndpoint  `json:"public"`
	Members     []Member        `json:"members"`
	Usage       Usage           `json:"usage"`
	Autoscale   AutoscaleStatus `json:"autoscale"`
	// FailoverReady: every sentinel knows every replica and its peers, so a
	// failed primary is replaced (false without replicas).
	FailoverReady bool      `json:"failoverReady"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// PublicEndpoint is the database as reached from outside the cluster.
type PublicEndpoint struct {
	Enabled bool `json:"enabled"`
	// Available is false when the endpoint cannot work (Reason says why).
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Host      string `json:"host,omitempty"`
	ReadHost  string `json:"readHost,omitempty"`
	Port      int    `json:"port"`
	TLS       bool   `json:"tls"`
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
		Spec: spec, State: st, Status: d.Status, Deleting: d.Deleting, Host: Host(d), ReadHost: ReadHost(d), Port: portOf(d),
		Standalone: d.Standalone(), Network: d.ParseNetwork(), Public: m.public(d),
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
			if d.Engine == EnginePostgres && l != nil && l.Error == "" {
				x.Role, x.LinkUp, x.LagBytes = "replica", l.LinkUp, l.LagBytes
				if l.Role == "master" {
					x.Role, x.LinkUp = "primary", true
					primaryUp = mb.State == store.TaskRunning
				}
			}
			if l != nil && l.Error != "" && x.Error == "" {
				x.Error = l.Error
			}
		}
		v.Members = append(v.Members, x)
	}
	if d.Engine == EnginePostgres {
		var data []Member
		for _, x := range v.Members {
			if x.Kind == KindData {
				data = append(data, x)
			}
		}
		v.Health = pgHealth(d, st, data, d.CreatedAt)
		v.FailoverReady = st.Replicas > 0 && v.Health == "healthy"
		return v
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

func (m *Manager) baseDomain() string {
	if m.BaseDomain == nil {
		return ""
	}
	return m.BaseDomain()
}

// public describes the public endpoint.
func (m *Manager) public(d store.Database) PublicEndpoint {
	eng, _ := EngineByName(d.Engine)
	p := PublicEndpoint{Enabled: d.ParseNetwork().Public.Enabled, Port: eng.Port, TLS: true}
	base := m.baseDomain()
	if base == "" {
		p.Reason = "the platform has no base domain yet; public endpoints need one for their certificates"
		return p
	}
	p.Host, p.ReadHost = PublicHost(d, base), PublicReadHost(d, base)
	p.Available = true
	return p
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
	Database string `json:"database,omitempty"` // PostgreSQL
	URL      string `json:"url"`                // redis://default:…@host:6379
	ReadURL  string `json:"readUrl"`            // read-only endpoint
	// PublicURL and PublicReadURL reach the database from outside the
	// cluster over TLS (only while the public endpoint is on).
	PublicURL     string `json:"publicUrl,omitempty"`
	PublicReadURL string `json:"publicReadUrl,omitempty"`
	// HAURL (PostgreSQL) lists every member: libpq-compatible clients find
	// the primary themselves, so they follow a failover even while the
	// controller (which moves the endpoints) is down.
	HAURL string `json:"haUrl,omitempty"`
}

// Credentials returns the app user's connection details.
func (m *Manager) Credentials(ctx context.Context, d store.Database) (Credentials, error) {
	sec, err := m.secrets(d)
	if err != nil {
		return Credentials{}, err
	}
	c := Credentials{Host: Host(d), ReadHost: ReadHost(d), Port: portOf(d), Username: "default", Password: sec.Password}
	eng, _ := EngineByName(d.Engine)
	path, publicQuery := "", ""
	if d.Engine == EnginePostgres {
		c.Username, c.Database = pgAppUser, PgDatabase(d)
		path, publicQuery = "/"+c.Database, "?sslmode=require"
	}
	userinfo := url.UserPassword(c.Username, sec.Password).String()
	build := func(scheme, host string) string {
		return scheme + "://" + userinfo + "@" + net.JoinHostPort(host, fmt.Sprint(eng.Port)) + path
	}
	c.URL, c.ReadURL = build(eng.Scheme, c.Host), build(eng.Scheme, c.ReadHost)
	if d.Engine == EnginePostgres {
		members, _ := m.st.DatabaseMembers(ctx, d.ID)
		var hosts []string
		for _, mb := range members {
			if mb.Kind == KindData {
				hosts = append(hosts, net.JoinHostPort(memberHost(d, mb.Kind, mb.Ordinal), fmt.Sprint(PostgresPort)))
			}
		}
		if len(hosts) > 0 {
			c.HAURL = eng.Scheme + "://" + userinfo + "@" + strings.Join(hosts, ",") + path + "?target_session_attrs=read-write"
		}
	}
	if p := m.public(d); p.Enabled && p.Available {
		c.PublicURL, c.PublicReadURL = build(eng.TLSScheme, p.Host)+publicQuery, build(eng.TLSScheme, p.ReadHost)+publicQuery
	}
	return c, nil
}

// Events lists a database's scaling and failover events, newest first,
// older than the event before names when set.
func (m *Manager) Events(ctx context.Context, id string, limit int, before string) ([]store.DatabaseEvent, error) {
	ev, err := m.st.DatabaseEvents(ctx, id, limit, before)
	if ev == nil {
		ev = []store.DatabaseEvent{}
	}
	return ev, err
}
