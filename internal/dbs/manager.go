package dbs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"syncloud/internal/agentgw"
	"syncloud/internal/auth"
	"syncloud/internal/events"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/nodes"
	"syncloud/internal/secgroup"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

const (
	// TopicDatabase carries a View when a database changes ({id, deleted} on delete).
	TopicDatabase = "database.updated"
	// lostAfter is how long a member's node may be not ready before a
	// replica or sentinel is replaced elsewhere.
	lostAfter = 5 * time.Minute
	// pendingResend re-sends a member's spec while it has not started.
	pendingResend = 30 * time.Second
	probeEvery    = 5 * time.Second
)

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

// Manager is the database operator.
type Manager struct {
	st    *store.Store
	gw    *agentgw.Gateway
	wl    *workload.Manager
	nodes *nodes.Registry
	box   *secrets.Box
	bus   *events.Bus
	log   *slog.Logger
	now   func() time.Time

	// DNS returns a node's discovery resolver and search domains for a
	// member of the environment (nil without the private network).
	DNS func(nodeID, project, env string) (servers, search []string)
	// OnChange runs when endpoints or members change (service directory).
	OnChange func()
	// OnNetworkChange runs when the set of public endpoints or access lists
	// changes (certificates, host firewall, security policy).
	OnNetworkChange func()
	// PostgresImage runs PostgreSQL members, PgBouncer and the platform etcd.
	PostgresImage string
	// BaseDomain returns the platform's base domain ("" = none, so no
	// public endpoints).
	BaseDomain func() string
	// Metrics stores member samples (nil = none).
	Metrics Recorder

	queue  chan string
	qmu    sync.Mutex
	queued map[string]bool
	cl     clients

	mu        sync.Mutex
	live      map[string]*Live // member ID -> last probe
	auto      map[string]*autoState
	etcdUsers map[string]bool          // PostgreSQL clusters whose etcd user exists
	pgSlots   map[string]chan struct{} // explorer connection slots per PostgreSQL cluster
}

// Recorder imports Prometheus text samples.
type Recorder interface {
	Import(ctx context.Context, text string) error
}

// Live is what the last probe saw of a member.
type Live struct {
	At       time.Time `json:"at"`
	Role     string    `json:"role"` // master | slave | sentinel
	Info     Info      `json:"-"`
	Error    string    `json:"error,omitempty"`
	LinkUp   bool      `json:"linkUp"`
	LagBytes int64     `json:"lagBytes"`
	// Sentinels: the replicas and other sentinels it knows (failover needs
	// every replica known and a majority of sentinels).
	KnownReplicas, KnownPeers int
}

func New(st *store.Store, gw *agentgw.Gateway, wl *workload.Manager, reg *nodes.Registry, box *secrets.Box, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{st: st, gw: gw, wl: wl, nodes: reg, box: box, bus: bus, log: log, now: time.Now,
		queue: make(chan string, 256), queued: map[string]bool{}, live: map[string]*Live{}, auto: map[string]*autoState{}, etcdUsers: map[string]bool{}}
}

// Hooks follow member containers on the agent stream.
func (m *Manager) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnTaskStatus: m.onTaskStatus, OnConnect: m.onConnect}
}

func isMember(taskID string) bool {
	return strings.HasPrefix(taskID, MemberPrefix) || strings.HasPrefix(taskID, SentinelPrefix)
}

// ── lifecycle ───────────────────────────────────────────────────────────────

func aad(id string) []byte { return []byte("database:" + id) }

func randomPassword() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (m *Manager) secrets(d store.Database) (Secrets, error) {
	b, err := m.box.Open(d.Secrets, aad(d.ID))
	if err != nil {
		return Secrets{}, err
	}
	var s Secrets
	return s, json.Unmarshal(b, &s)
}

// CreateRequest is a new database.
type CreateRequest struct {
	Name, Engine, Version string
	// Env is the project environment; nil for a standalone database.
	Env     *store.Environment
	Spec    Spec
	Network Network
	// Exists checks access-list references (nil: not checked).
	Exists func(p secgroup.Peer) bool
	Actor  string
}

// Create stores a database and starts its members.
func (m *Manager) Create(ctx context.Context, req CreateRequest) (View, error) {
	name := req.Name
	if err := workload.ValidName(name); err != nil {
		return View{}, ErrInvalid{fmt.Errorf("database name %w", err)}
	}
	if len(name) > 29 { // room for "-ro" in a 32-character label
		return View{}, ErrInvalid{errors.New("database name must be at most 29 characters")}
	}
	if err := reservedName(name); err != nil {
		return View{}, ErrInvalid{err}
	}
	if req.Engine == "" {
		req.Engine = EngineValkey
	}
	eng, ok := EngineByName(req.Engine)
	if !ok {
		return View{}, ErrInvalid{fmt.Errorf("unknown engine %q", req.Engine)}
	}
	if !eng.Available {
		return View{}, ErrInvalid{fmt.Errorf("%s is not available yet", eng.Title)}
	}
	version := req.Version
	if version == "" {
		version = eng.DefaultVersion
	}
	if !slices.Contains(eng.Versions, version) {
		return View{}, ErrInvalid{fmt.Errorf("version must be one of %s", strings.Join(eng.Versions, ", "))}
	}
	spec := req.Spec
	spec.ForEngine(eng.Name)
	if err := spec.Normalize(); err != nil {
		return View{}, ErrInvalid{err}
	}
	now := m.now().UTC().Truncate(time.Second)
	d := store.Database{ID: auth.NewID("db_"), Name: name, Engine: eng.Name, Version: version, CreatedAt: now}
	if req.Env != nil {
		if err := m.checkNodes(ctx, req.Env.ProjectID, spec.Nodes); err != nil {
			return View{}, err
		}
		d.EnvironmentID = req.Env.ID
		if p, err := m.st.ProjectByID(ctx, req.Env.ProjectID); err == nil {
			d.Project, d.Environment = p.Name, req.Env.Name
		}
	}
	n := req.Network
	if n.Access == nil {
		n.Access = d.ParseNetwork().Access // the default for its kind
	}
	if err := NormalizeNetwork(&n, req.Exists); err != nil {
		return View{}, ErrInvalid{err}
	}
	nb, _ := json.Marshal(n)
	d.Network = string(nb)
	secrets := Secrets{Password: randomPassword(), AdminPassword: randomPassword()}
	st := State{MemoryMiB: spec.Memory.Min, Replicas: spec.Replicas.Min, LimitMiB: spec.Memory.Max}
	if eng.Name == EnginePostgres {
		secrets.ReplicationPassword, secrets.RestPassword, secrets.EtcdPassword = randomPassword(), randomPassword(), randomPassword()
		st.LimitMiB = spec.Memory.Min // the container's size (vertical scaling restarts members)
	}
	sec, _ := json.Marshal(secrets)
	d.Secrets = m.box.Seal(sec, aad(d.ID))
	d.Spec = encode(spec)
	d.State = encode(st)
	if err := m.st.CreateDatabase(ctx, d); errors.Is(err, store.ErrNameTaken) {
		return View{}, ErrInvalid{fmt.Errorf("the name %s is taken (database names are unique in the cluster, and a project database cannot share its name with a service)", name)}
	} else if err != nil {
		return View{}, err
	}
	m.event(ctx, d.ID, "created", "", fmt.Sprintf("%d MiB, %d replicas", spec.Memory.Min, spec.Replicas.Min), "", req.Actor)
	m.Enqueue(d.ID)
	m.networkChanged()
	full, err := m.st.DatabaseByID(ctx, d.ID)
	if err != nil {
		return View{}, err
	}
	return m.View(ctx, full), nil
}

// Update changes a database's spec. Memory and replicas apply online;
// persistence, eviction policy and a higher memory maximum restart the
// members one at a time (replicas first).
func (m *Manager) Update(ctx context.Context, id string, spec Spec, actor string) (View, error) {
	d, err := m.st.DatabaseByID(ctx, id)
	if err != nil {
		return View{}, err
	}
	spec.ForEngine(d.Engine)
	if err := spec.Normalize(); err != nil {
		return View{}, ErrInvalid{err}
	}
	if !d.Standalone() {
		e, err := m.st.EnvironmentByID(ctx, d.EnvironmentID)
		if err != nil {
			return View{}, err
		}
		if err := m.checkNodes(ctx, e.ProjectID, spec.Nodes); err != nil {
			return View{}, err
		}
	}
	old, _ := parseSpec(d.Spec)
	st := parseState(d.State)
	st.MemoryMiB = min(max(st.MemoryMiB, spec.Memory.Min), spec.Memory.Max)
	st.Replicas = min(max(st.Replicas, spec.Replicas.Min), spec.Replicas.Max)
	if spec.Memory.Max > st.LimitMiB || spec.Memory.Max < st.LimitMiB/2 {
		st.LimitMiB = spec.Memory.Max // containers are recreated, one at a time
	}
	now := m.now().UTC()
	if err := m.st.SetDatabaseSpec(ctx, id, encode(spec), now); err != nil {
		return View{}, err
	}
	if err := m.st.SetDatabaseState(ctx, id, encode(st)); err != nil {
		return View{}, err
	}
	if old.Memory != spec.Memory || old.Replicas != spec.Replicas {
		m.event(ctx, id, "settings", fmt.Sprintf("memory %d–%d MiB, replicas %d–%d", old.Memory.Min, old.Memory.Max, old.Replicas.Min, old.Replicas.Max),
			fmt.Sprintf("memory %d–%d MiB, replicas %d–%d", spec.Memory.Min, spec.Memory.Max, spec.Replicas.Min, spec.Replicas.Max), "", actor)
	}
	m.Enqueue(id)
	d, _ = m.st.DatabaseByID(ctx, id)
	return m.View(ctx, d), nil
}

// checkNodes keeps a database within its project's allowed nodes.
func (m *Manager) checkNodes(ctx context.Context, projectID string, nodes []string) error {
	p, err := m.st.ProjectByID(ctx, projectID)
	if err != nil || len(p.Nodes) == 0 {
		return err
	}
	for _, n := range nodes {
		if !slices.Contains(p.Nodes, n) {
			return ErrInvalid{fmt.Errorf("node %s is not allowed in project %s (allowed: %s)", n, p.Name, strings.Join(p.Nodes, ", "))}
		}
	}
	return nil
}

// Delete removes a database: its members stop and their volumes are deleted.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if err := m.st.MarkDatabaseDeleting(ctx, id, m.now().UTC()); err != nil {
		return err
	}
	m.Enqueue(id)
	return nil
}

// Failover promotes a replica (Sentinel picks the most up-to-date one).
func (m *Manager) Failover(ctx context.Context, id string) error {
	d, err := m.st.DatabaseByID(ctx, id)
	if err != nil {
		return err
	}
	st := parseState(d.State)
	if st.Replicas == 0 {
		return ErrInvalid{errors.New("the database has no replica to fail over to")}
	}
	sec, err := m.secrets(d)
	if err != nil {
		return err
	}
	if d.Engine == EnginePostgres {
		if err := m.pgSwitchover(ctx, d, sec); err != nil {
			return err
		}
		m.event(ctx, id, "failover", "m"+strconv.Itoa(st.Primary), "", "switchover requested", "user")
		return nil
	}
	members, err := m.st.DatabaseMembers(ctx, id)
	if err != nil {
		return err
	}
	var last error = errors.New("no sentinel is reachable")
	for _, s := range members {
		if s.Kind != KindSentinel || s.IP == "" || s.State != store.TaskRunning {
			continue
		}
		cl := m.cl.get(addr(s.IP, SentinelPort), "", sec.AdminPassword)
		if last = cl.Do(ctx, "SENTINEL", "FAILOVER", d.Name).Err(); last == nil {
			m.event(ctx, id, "failover", "m"+strconv.Itoa(st.Primary), "", "requested", "user")
			return nil
		}
	}
	return last
}

func (m *Manager) event(ctx context.Context, id, kind, from, to, reason, actor string) {
	if err := m.st.AddDatabaseEvent(ctx, store.DatabaseEvent{DatabaseID: id, At: m.now().UTC(), Kind: kind, From: from, To: to, Reason: reason, Actor: actor}); err != nil {
		m.log.Warn("database event", "err", err)
	}
}

// ── reconciling ─────────────────────────────────────────────────────────────

// Enqueue asks for a reconcile of one database.
func (m *Manager) Enqueue(id string) {
	m.qmu.Lock()
	defer m.qmu.Unlock()
	if m.queued[id] {
		return
	}
	m.queued[id] = true
	select {
	case m.queue <- id:
	default:
		delete(m.queued, id)
	}
}

// Run reconciles on demand, and probes and reconciles everything every 5s.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(probeEvery)
	defer t.Stop()
	m.probeAll(ctx) // know every database's state right after a restart
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.queue:
			m.qmu.Lock()
			delete(m.queued, id)
			m.qmu.Unlock()
			m.reconcile(ctx, id)
		case <-t.C:
			m.probeAll(ctx)
		}
	}
}

func (m *Manager) probeAll(ctx context.Context) {
	dbs, err := m.st.ListDatabases(ctx)
	if err != nil {
		m.log.Error("list databases", "err", err)
		return
	}
	m.reconcileEtcd(ctx)
	for _, d := range dbs {
		m.probe(ctx, d)
		m.reconcile(ctx, d.ID)
		if d.Engine == EngineValkey {
			m.autoscale(ctx, d.ID)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context, id string) {
	d, err := m.st.DatabaseByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return
	} else if err != nil {
		m.log.Error("load database", "database", id, "err", err)
		return
	}
	spec, err := parseSpec(d.Spec)
	if err != nil {
		m.log.Error("database spec", "database", id, "err", err)
		return
	}
	st := parseState(d.State)
	sec, err := m.secrets(d)
	if err != nil {
		m.log.Error("database secrets", "database", id, "err", err)
		return
	}
	members, err := m.st.DatabaseMembers(ctx, id)
	if err != nil {
		m.log.Error("database members", "database", id, "err", err)
		return
	}
	now := m.now().UTC()
	changed := false

	if d.Deleting {
		if d.Engine == EnginePostgres {
			m.dropEtcdUser(ctx, d)
		}
		for _, mb := range members {
			// A node that cannot be told now removes the container and
			// volume when it reports in again (onConnect).
			m.sendStop(mb, []string{Volume(d, mb.Kind, mb.Ordinal)})
			_ = m.st.DeleteDatabaseMember(ctx, mb.ID)
		}
		if err := m.st.DeleteDatabase(ctx, id, now); err != nil && !errors.Is(err, store.ErrNotFound) {
			m.log.Error("delete database", "database", id, "err", err)
			return
		}
		m.log.Info("database deleted", "database", d.Name)
		m.mu.Lock()
		for _, mb := range members {
			delete(m.live, mb.ID)
		}
		delete(m.auto, id)
		m.mu.Unlock()
		m.bus.Publish(TopicDatabase, map[string]any{"id": id, "deleted": true})
		m.changed()
		m.networkChanged()
		return
	}

	if st.LimitMiB == 0 {
		st.LimitMiB = spec.Memory.Max
	}
	if d.Engine == EnginePostgres && !m.etcdUserReady(id) {
		// Patroni needs its etcd user before any member starts.
		if err := m.ensureEtcdUser(ctx, d, sec); err != nil {
			if why := "waiting for the platform etcd: " + err.Error(); why != d.Status {
				_ = m.st.SetDatabaseStatus(ctx, id, why)
				m.publish(ctx, id)
			}
			return
		}
		m.mu.Lock()
		m.etcdUsers[id] = true
		m.mu.Unlock()
	}
	var data, sents []store.DatabaseMember
	for _, mb := range members {
		// A replica or sentinel whose node is gone for good is replaced.
		n, ok := m.nodes.Get(mb.NodeID)
		gone := !ok || (n.Status == store.NodeNotReady && now.Sub(n.StatusAt) > lostAfter)
		if gone && !(mb.Kind == KindData && (mb.Ordinal == st.Primary || len(members) == 1)) {
			m.log.Warn("replacing database member on a lost node", "database", d.Name, "member", mb.ID)
			_ = m.st.DeleteDatabaseMember(ctx, mb.ID)
			m.event(ctx, id, "member", memberName(mb), "", "its node is lost; replaced on another node", "operator")
			changed = true
			continue
		}
		if mb.Kind == KindData {
			data = append(data, mb)
		} else {
			sents = append(sents, mb)
		}
	}

	status := ""
	place := func(kind string, ordinal int, avoid []string) bool {
		res := spec.CPU
		mem := memberReservation(spec, st)
		if kind == KindSentinel {
			res, mem = 0.02, 16
		}
		ws := workload.Spec{Resources: workload.Resources{CPU: res, Memory: mem}, Placement: workload.Placement{Strategy: "spread", Nodes: spec.Nodes}}
		ws = ws.WithProjectNodes(m.projectNodes(ctx, d.EnvironmentID)).Avoiding(avoid)
		nodeID, why := m.wl.PlaceSpec(ctx, ws)
		if nodeID == "" && kind == KindSentinel && len(avoid) > 0 {
			// Fewer nodes than sentinels: share a node rather than run none.
			nodeID, why = m.wl.PlaceSpec(ctx, ws.Avoiding(nil))
		}
		if nodeID == "" {
			status = fmt.Sprintf("cannot place %s %d: %s", kind, ordinal, why)
			return false
		}
		mb := store.DatabaseMember{ID: auth.NewID(MemberPrefix), DatabaseID: id, Kind: kind, Ordinal: ordinal, NodeID: nodeID,
			Desired: "running", State: store.TaskPending, CreatedAt: now.Truncate(time.Second)}
		if kind == KindSentinel {
			mb.ID = auth.NewID(SentinelPrefix)
		}
		if err := m.st.CreateDatabaseMember(ctx, mb); err != nil {
			m.log.Error("create database member", "database", id, "err", err)
			return false
		}
		m.send(ctx, d, spec, st, sec, &mb)
		changed = true
		if kind == KindData {
			data = append(data, mb)
		} else {
			sents = append(sents, mb)
		}
		return true
	}
	nodesOf := func(ms []store.DatabaseMember) []string {
		var out []string
		for _, x := range ms {
			if n, ok := m.nodes.Get(x.NodeID); ok {
				out = append(out, n.Name)
			}
		}
		return out
	}

	// Sentinels first, so new data members find the primary through them.
	wantSentinels := 0
	if spec.HasSentinels() {
		wantSentinels = Sentinels
	}
	for ord := 0; ord < wantSentinels; ord++ {
		if !slices.ContainsFunc(sents, func(x store.DatabaseMember) bool { return x.Ordinal == ord }) {
			place(KindSentinel, ord, nodesOf(sents))
		}
	}
	for _, s := range sents {
		if s.Ordinal >= wantSentinels {
			m.remove(ctx, d, s)
			changed = true
		}
	}

	// Data members: the primary plus the current replica count.
	want := 1 + st.Replicas
	sort.Slice(data, func(i, j int) bool { return data[i].Ordinal < data[j].Ordinal })
	for len(data) < want {
		ord := 0
		for slices.ContainsFunc(data, func(x store.DatabaseMember) bool { return x.Ordinal == ord }) {
			ord++
		}
		if !place(KindData, ord, nodesOf(data)) {
			break
		}
		sort.Slice(data, func(i, j int) bool { return data[i].Ordinal < data[j].Ordinal })
	}
	for len(data) > want {
		// Remove the newest replica; never the primary.
		i := len(data) - 1
		if data[i].Ordinal == st.Primary {
			i--
		}
		if i < 0 {
			break
		}
		m.remove(ctx, d, data[i])
		data = slices.Delete(data, i, i+1)
		changed = true
	}

	// Send specs: new or not yet started members, and changed specs one at a
	// time (replicas and sentinels first; the primary after a failover).
	rolling := false
	for _, mb := range append(slices.Clone(sents), data...) {
		ts := m.taskSpec(d, spec, st, sec, mb)
		h := specHash(ts)
		switch {
		case mb.SpecHash == "", mb.State == store.TaskPending && now.Sub(mb.UpdatedAt) > pendingResend:
			m.send(ctx, d, spec, st, sec, &mb)
		case mb.SpecHash != h && !m.connected(mb.NodeID):
			// Part of the spec (the node's resolver) is only known while the
			// node is connected, e.g. not yet after a controller restart;
			// a restart could not be sent anyway.
			continue
		case mb.SpecHash != h && !rolling:
			if !m.allRunning(data, sents, mb.ID) {
				rolling = true // wait for the cluster to be whole again
				continue
			}
			if mb.Kind == KindData && mb.Ordinal == st.Primary && len(data) > 1 && (len(sents) > 0 || d.Engine == EnginePostgres) {
				if err := m.Failover(ctx, id); err != nil {
					m.log.Warn("failover before restarting the primary", "database", d.Name, "err", err)
				}
				rolling = true
				continue
			}
			m.log.Info("restarting database member with a new spec", "database", d.Name, "member", memberName(mb))
			m.send(ctx, d, spec, st, sec, &mb)
			rolling = true
		}
	}

	if status != d.Status {
		_ = m.st.SetDatabaseStatus(ctx, id, status)
		changed = true
	}
	if encode(st) != d.State {
		_ = m.st.SetDatabaseState(ctx, id, encode(st))
		changed = true
	}
	if changed {
		m.publish(ctx, id)
		m.changed()
	}
}

func (m *Manager) allRunning(data, sents []store.DatabaseMember, except string) bool {
	for _, x := range append(slices.Clone(data), sents...) {
		if x.ID != except && x.State != store.TaskRunning {
			return false
		}
	}
	return true
}

func (m *Manager) projectNodes(ctx context.Context, envID string) []string {
	if envID == "" {
		return nil // standalone: any node
	}
	e, err := m.st.EnvironmentByID(ctx, envID)
	if err != nil {
		return nil
	}
	p, err := m.st.ProjectByID(ctx, e.ProjectID)
	if err != nil {
		return nil
	}
	return p.Nodes
}

func (m *Manager) connected(nodeID string) bool {
	n, ok := m.nodes.Get(nodeID)
	return ok && n.Connected
}

func (m *Manager) taskSpec(d store.Database, spec Spec, st State, sec Secrets, mb store.DatabaseMember) *agentv1.TaskSpec {
	var dns, search []string
	if m.DNS != nil {
		dns, search = m.DNS(mb.NodeID, d.Project, d.Environment)
	}
	if d.Engine == EnginePostgres {
		return pgTaskSpec(m.PostgresImage, d, spec, st, sec, mb, m.EtcdHosts(context.Background()), dns, search)
	}
	return taskSpec(d, spec, st, sec, mb, dns, search)
}

func (m *Manager) send(ctx context.Context, d store.Database, spec Spec, st State, sec Secrets, mb *store.DatabaseMember) {
	ts := m.taskSpec(d, spec, st, sec, *mb)
	mb.SpecHash = specHash(ts)
	if err := m.st.UpdateDatabaseMember(ctx, *mb, m.now().UTC()); err != nil {
		m.log.Warn("update database member", "member", mb.ID, "err", err)
	}
	err := m.gw.Send(mb.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: ts}}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("send database member", "member", mb.ID, "err", err)
	}
}

// remove stops a member for good and deletes its volume.
func (m *Manager) remove(ctx context.Context, d store.Database, mb store.DatabaseMember) {
	m.log.Info("removing database member", "database", d.Name, "member", memberName(mb))
	m.sendStop(mb, []string{Volume(d, mb.Kind, mb.Ordinal)})
	_ = m.st.DeleteDatabaseMember(ctx, mb.ID)
	m.mu.Lock()
	delete(m.live, mb.ID)
	m.mu.Unlock()
	if mb.IP != "" {
		m.cl.forget(addr(mb.IP, Port))
		m.cl.forget(addr(mb.IP, SentinelPort))
	}
}

func (m *Manager) sendStop(mb store.DatabaseMember, volumes []string) {
	// The volume is recorded until the node reports the task removed, so a
	// stop that never arrives is repeated when the node reconnects.
	for _, v := range volumes {
		o := store.DatabaseOrphan{TaskID: mb.ID, NodeID: mb.NodeID, Volume: v, CreatedAt: m.now().UTC()}
		if err := m.st.AddDatabaseOrphan(context.Background(), o); err != nil {
			m.log.Warn("record member volume", "member", mb.ID, "err", err)
		}
	}
	err := m.gw.Send(mb.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
		TaskId: mb.ID, TimeoutSeconds: 20, Remove: true, RemoveVolumes: volumes}}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("stop database member", "member", mb.ID, "err", err)
	}
}

func memberName(mb store.DatabaseMember) string {
	if mb.Kind == KindSentinel {
		return "s" + strconv.Itoa(mb.Ordinal)
	}
	return "m" + strconv.Itoa(mb.Ordinal)
}

func (m *Manager) changed() {
	if m.OnChange != nil {
		m.OnChange()
	}
}

func (m *Manager) publish(ctx context.Context, id string) {
	if d, err := m.st.DatabaseByID(ctx, id); err == nil {
		m.bus.Publish(TopicDatabase, m.View(ctx, d))
	}
}

// ── agent events ────────────────────────────────────────────────────────────

func stateOf(s agentv1.TaskState) string {
	switch s {
	case agentv1.TaskState_TASK_STATE_RUNNING:
		return store.TaskRunning
	case agentv1.TaskState_TASK_STATE_PULLING:
		return store.TaskPulling
	case agentv1.TaskState_TASK_STATE_STARTING:
		return store.TaskStarting
	case agentv1.TaskState_TASK_STATE_EXITED:
		return store.TaskExited
	case agentv1.TaskState_TASK_STATE_FAILED:
		return store.TaskFailed
	case agentv1.TaskState_TASK_STATE_REMOVED:
		return store.TaskStopped
	}
	return store.TaskPending
}

func (m *Manager) onTaskStatus(node store.Node, s *agentv1.TaskStatus) {
	if strings.HasPrefix(s.GetTaskId(), EtcdPrefix) {
		m.onEtcdStatus(node, s)
		return
	}
	if !isMember(s.GetTaskId()) {
		return
	}
	ctx := context.Background()
	mb, err := m.st.DatabaseMemberByID(ctx, s.GetTaskId())
	if errors.Is(err, store.ErrNotFound) {
		orphan, oerr := m.st.DatabaseOrphanByTask(ctx, s.GetTaskId())
		if s.GetState() == agentv1.TaskState_TASK_STATE_REMOVED {
			if oerr == nil {
				_ = m.st.DeleteDatabaseOrphan(ctx, orphan.TaskID)
			}
			return
		}
		// Left over from a removed member or database: remove it, with its
		// volume when the removal recorded one. Otherwise the volume stays
		// unless the operator removes it on purpose.
		var volumes []string
		if oerr == nil {
			volumes = []string{orphan.Volume}
		}
		_ = m.gw.Send(node.ID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
			TaskId: s.GetTaskId(), TimeoutSeconds: 10, Remove: true, RemoveVolumes: volumes}}})
		return
	} else if err != nil {
		return
	}
	state := stateOf(s.GetState())
	ip := s.GetIp()
	if state != store.TaskRunning {
		ip = ""
	}
	if mb.State == state && mb.IP == ip && mb.Error == s.GetError() {
		return
	}
	if mb.IP != "" && mb.IP != ip {
		m.cl.forget(addr(mb.IP, Port))
		m.cl.forget(addr(mb.IP, SentinelPort))
	}
	mb.State, mb.IP, mb.Error = state, ip, s.GetError()
	if err := m.st.UpdateDatabaseMember(ctx, mb, m.now().UTC()); err != nil {
		m.log.Warn("update database member", "member", mb.ID, "err", err)
		return
	}
	m.publish(ctx, mb.DatabaseID)
	m.changed()
	m.Enqueue(mb.DatabaseID)
}

// onConnect reconciles a node's database containers with its members.
func (m *Manager) onConnect(c agentgw.Conn) {
	ctx := context.Background()
	mine, err := m.st.NodeDatabaseMembers(ctx, c.Node.ID)
	if err != nil {
		return
	}
	known := map[string]bool{}
	for _, mb := range mine {
		known[mb.ID] = true
	}
	seen := map[string]bool{}
	etcdSeen := map[string]bool{}
	for _, s := range c.Hello.GetTasks() {
		if strings.HasPrefix(s.GetTaskId(), EtcdPrefix) {
			etcdSeen[s.GetTaskId()] = true
			m.onEtcdStatus(c.Node, s)
			continue
		}
		if !isMember(s.GetTaskId()) {
			continue
		}
		seen[s.GetTaskId()] = true
		m.onTaskStatus(c.Node, s)
	}
	if orphans, err := m.st.NodeDatabaseOrphans(ctx, c.Node.ID); err == nil {
		for _, o := range orphans {
			if seen[o.TaskID] {
				continue // stopped with its volume above
			}
			// The container is already gone; remove the volume left behind.
			err := m.gw.Send(c.Node.ID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
				TaskId: o.TaskID, TimeoutSeconds: 10, Remove: true, RemoveVolumes: []string{o.Volume}}}})
			if err == nil {
				_ = m.st.DeleteDatabaseOrphan(ctx, o.TaskID)
			}
		}
	}
	for _, mb := range mine {
		if !seen[mb.ID] {
			// The container is missing (e.g. the node was reinstalled): start it again.
			mb.SpecHash = ""
			_ = m.st.UpdateDatabaseMember(ctx, mb, m.now().UTC())
			m.Enqueue(mb.DatabaseID)
		}
	}
	if ems, err := m.st.EtcdMembers(ctx); err == nil {
		for _, em := range ems {
			if em.NodeID == c.Node.ID && !etcdSeen[em.ID] {
				em.SpecHash = "" // resent by the next etcd reconcile
				_ = m.st.UpdateEtcdMember(ctx, em, m.now().UTC())
			}
		}
	}
}

// ── usage and directory ─────────────────────────────────────────────────────

// Usage is the per-node reservation of every database member, for the scheduler.
func (m *Manager) Usage(ctx context.Context) map[string]workload.Usage {
	out := map[string]workload.Usage{}
	dbs, err := m.st.ListDatabases(ctx)
	if err != nil {
		return out
	}
	byID := map[string]store.Database{}
	for _, d := range dbs {
		byID[d.ID] = d
	}
	members, err := m.st.AllDatabaseMembers(ctx)
	if err != nil {
		return out
	}
	for _, mb := range members {
		d, ok := byID[mb.DatabaseID]
		if !ok {
			continue
		}
		u := out[mb.NodeID]
		u.Count++
		if mb.Kind == KindSentinel {
			u.CPU += 0.02
			u.MemoryMiB += 16
		} else {
			spec, _ := parseSpec(d.Spec)
			u.CPU += spec.CPU
			u.MemoryMiB += memberReservation(spec, parseState(d.State))
		}
		out[mb.NodeID] = u
	}
	return out
}

// Directory is what discovery publishes for databases: the read-write and
// read-only VIPs, and DNS names for the endpoints and every member.
func (m *Manager) Directory(ctx context.Context, vip func(index int) string, pool store.IndexPool, cooldown time.Duration) ([]*agentv1.VirtualService, []*agentv1.DNSRecord) {
	var svcs []*agentv1.VirtualService
	var recs []*agentv1.DNSRecord
	dbs, err := m.st.ListDatabases(ctx)
	if err != nil {
		return nil, nil
	}
	now := m.now()
	if ems, err := m.st.EtcdMembers(ctx); err == nil {
		for _, em := range ems {
			if em.IP != "" && em.State == store.TaskRunning {
				recs = append(recs, &agentv1.DNSRecord{Name: EtcdHost(em.Ordinal), Ips: []string{em.IP}})
			}
		}
	}
	for _, d := range dbs {
		if d.Deleting {
			continue
		}
		members, err := m.st.DatabaseMembers(ctx, d.ID)
		if err != nil {
			continue
		}
		for _, mb := range members {
			if mb.IP != "" && mb.State == store.TaskRunning {
				recs = append(recs, &agentv1.DNSRecord{Name: memberHost(d, mb.Kind, mb.Ordinal), Ips: []string{mb.IP}})
			}
		}
		primary, replicas := m.endpoints(ctx, d)
		rw, ro, err := m.st.EnsureDatabaseVIPs(ctx, d.ID, pool, cooldown, now)
		if err != nil {
			m.log.Error("database VIPs", "database", d.ID, "err", err)
			continue
		}
		rwIP, roIP := vip(rw), vip(ro)
		recs = append(recs, &agentv1.DNSRecord{Name: Host(d), Ips: []string{rwIP}}, &agentv1.DNSRecord{Name: ReadHost(d), Ips: []string{roIP}})
		short := strings.TrimPrefix(d.ID, "db_")
		port := portOf(d)
		w := &agentv1.VirtualPort{Protocol: "tcp", Port: uint32(port)}
		if primary != "" {
			w.Backends = []string{addr(primary, port)}
		}
		r := &agentv1.VirtualPort{Protocol: "tcp", Port: uint32(port)}
		for _, ip := range replicas {
			r.Backends = append(r.Backends, addr(ip, port))
		}
		if len(r.Backends) == 0 {
			r.Backends = w.Backends // no replica: reads go to the primary
		}
		sort.Strings(r.Backends)
		svcs = append(svcs, &agentv1.VirtualService{Id: "dbw" + short, Vip: rwIP, Ports: []*agentv1.VirtualPort{w}},
			&agentv1.VirtualService{Id: "dbr" + short, Vip: roIP, Ports: []*agentv1.VirtualPort{r}})
	}
	return svcs, recs
}

func (m *Manager) etcdUserReady(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.etcdUsers[id]
}

func (m *Manager) liveOf(id string) *Live {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live[id]
}
