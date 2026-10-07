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
	// Metrics stores member samples (nil = none).
	Metrics Recorder

	queue  chan string
	qmu    sync.Mutex
	queued map[string]bool
	cl     clients

	mu   sync.Mutex
	live map[string]*Live // member ID -> last probe
	auto map[string]*autoState
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
		queue: make(chan string, 256), queued: map[string]bool{}, live: map[string]*Live{}, auto: map[string]*autoState{}}
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

// Create stores a database and starts its members.
func (m *Manager) Create(ctx context.Context, env store.Environment, name, version string, spec Spec, actor string) (View, error) {
	if err := workload.ValidName(name); err != nil {
		return View{}, ErrInvalid{fmt.Errorf("database name %w", err)}
	}
	if len(name) > 29 { // room for "-ro" in a 32-character label
		return View{}, ErrInvalid{errors.New("database name must be at most 29 characters")}
	}
	if version == "" {
		version = DefaultVersion
	}
	if Images[version] == "" {
		return View{}, ErrInvalid{fmt.Errorf("version must be one of %s", strings.Join(versions(), ", "))}
	}
	if err := spec.Normalize(); err != nil {
		return View{}, ErrInvalid{err}
	}
	if err := m.checkNodes(ctx, env.ProjectID, spec.Nodes); err != nil {
		return View{}, err
	}
	now := m.now().UTC().Truncate(time.Second)
	d := store.Database{ID: auth.NewID("db_"), EnvironmentID: env.ID, Name: name, Engine: Engine, Version: version, CreatedAt: now}
	sec, _ := json.Marshal(Secrets{Password: randomPassword(), AdminPassword: randomPassword()})
	d.Secrets = m.box.Seal(sec, aad(d.ID))
	d.Spec = encode(spec)
	d.State = encode(State{MemoryMiB: spec.Memory.Min, Replicas: spec.Replicas.Min, LimitMiB: spec.Memory.Max})
	if err := m.st.CreateDatabase(ctx, d); errors.Is(err, store.ErrNameTaken) {
		return View{}, ErrInvalid{fmt.Errorf("the name %s is taken in this environment (by a service or a database)", name)}
	} else if err != nil {
		return View{}, err
	}
	m.event(ctx, d.ID, "created", "", fmt.Sprintf("%d MiB, %d replicas", spec.Memory.Min, spec.Replicas.Min), "", actor)
	m.Enqueue(d.ID)
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
	if err := spec.Normalize(); err != nil {
		return View{}, ErrInvalid{err}
	}
	e, err := m.st.EnvironmentByID(ctx, d.EnvironmentID)
	if err != nil {
		return View{}, err
	}
	if err := m.checkNodes(ctx, e.ProjectID, spec.Nodes); err != nil {
		return View{}, err
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

func versions() []string {
	var v []string
	for k := range Images {
		v = append(v, k)
	}
	sort.Strings(v)
	return v
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
	for _, d := range dbs {
		m.probe(ctx, d)
		m.reconcile(ctx, d.ID)
		m.autoscale(ctx, d.ID)
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
		for _, mb := range members {
			if n, ok := m.nodes.Get(mb.NodeID); ok && n.Connected {
				m.sendStop(mb, []string{Volume(d, mb.Kind, mb.Ordinal)})
			}
			// A node that is gone cleans the container up when it returns (onConnect).
			_ = m.st.DeleteDatabaseMember(ctx, mb.ID)
		}
		if err := m.st.DeleteDatabase(ctx, id, now); err != nil && !errors.Is(err, store.ErrNotFound) {
			m.log.Error("delete database", "database", id, "err", err)
			return
		}
		m.log.Info("database deleted", "database", d.Project+"/"+d.Environment+"/"+d.Name)
		m.mu.Lock()
		for _, mb := range members {
			delete(m.live, mb.ID)
		}
		delete(m.auto, id)
		m.mu.Unlock()
		m.bus.Publish(TopicDatabase, map[string]any{"id": id, "deleted": true})
		m.changed()
		return
	}

	if st.LimitMiB == 0 {
		st.LimitMiB = spec.Memory.Max
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
		mem := reservation(st.MemoryMiB)
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
		case mb.SpecHash != h && !rolling:
			if !m.allRunning(data, sents, mb.ID) {
				rolling = true // wait for the cluster to be whole again
				continue
			}
			if mb.Kind == KindData && mb.Ordinal == st.Primary && len(data) > 1 && len(sents) > 0 {
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

func (m *Manager) taskSpec(d store.Database, spec Spec, st State, sec Secrets, mb store.DatabaseMember) *agentv1.TaskSpec {
	var dns, search []string
	if m.DNS != nil {
		dns, search = m.DNS(mb.NodeID, d.Project, d.Environment)
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
	if !isMember(s.GetTaskId()) {
		return
	}
	ctx := context.Background()
	mb, err := m.st.DatabaseMemberByID(ctx, s.GetTaskId())
	if errors.Is(err, store.ErrNotFound) {
		if s.GetState() != agentv1.TaskState_TASK_STATE_REMOVED {
			// Left over from a removed member or database: remove it. Its
			// volume stays unless the operator removed it on purpose.
			_ = m.gw.Send(node.ID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{TaskId: s.GetTaskId(), TimeoutSeconds: 10, Remove: true}}})
		}
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
	for _, s := range c.Hello.GetTasks() {
		if !isMember(s.GetTaskId()) {
			continue
		}
		seen[s.GetTaskId()] = true
		m.onTaskStatus(c.Node, s)
	}
	for _, mb := range mine {
		if !seen[mb.ID] {
			// The container is missing (e.g. the node was reinstalled): start it again.
			mb.SpecHash = ""
			_ = m.st.UpdateDatabaseMember(ctx, mb, m.now().UTC())
			m.Enqueue(mb.DatabaseID)
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
			u.MemoryMiB += reservation(parseState(d.State).MemoryMiB)
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
	for _, d := range dbs {
		if d.Deleting {
			continue
		}
		members, err := m.st.DatabaseMembers(ctx, d.ID)
		if err != nil {
			continue
		}
		st := parseState(d.State)
		var primary string
		var replicas []string
		for _, mb := range members {
			if mb.IP == "" || mb.State != store.TaskRunning {
				continue
			}
			recs = append(recs, &agentv1.DNSRecord{Name: memberHost(d, mb.Kind, mb.Ordinal), Ips: []string{mb.IP}})
			if mb.Kind != KindData {
				continue
			}
			if mb.Ordinal == st.Primary {
				primary = mb.IP
			} else if l := m.liveOf(mb.ID); l == nil || l.Role != "master" {
				replicas = append(replicas, mb.IP) // a restarted old primary joins as a replica soon
			}
		}
		rw, ro, err := m.st.EnsureDatabaseVIPs(ctx, d.ID, pool, cooldown, now)
		if err != nil {
			m.log.Error("database VIPs", "database", d.ID, "err", err)
			continue
		}
		rwIP, roIP := vip(rw), vip(ro)
		recs = append(recs, &agentv1.DNSRecord{Name: Host(d), Ips: []string{rwIP}}, &agentv1.DNSRecord{Name: ReadHost(d), Ips: []string{roIP}})
		short := strings.TrimPrefix(d.ID, "db_")
		w := &agentv1.VirtualPort{Protocol: "tcp", Port: Port}
		if primary != "" {
			w.Backends = []string{addr(primary, Port)}
		}
		r := &agentv1.VirtualPort{Protocol: "tcp", Port: Port}
		for _, ip := range replicas {
			r.Backends = append(r.Backends, addr(ip, Port))
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

func (m *Manager) liveOf(id string) *Live {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live[id]
}
