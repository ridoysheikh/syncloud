package workload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"syncloud/internal/agentgw"
	"syncloud/internal/auth"
	"syncloud/internal/events"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/nodes"
	"syncloud/internal/store"
)

// Event topics.
const (
	TopicService = "service.updated"
	TopicTask    = "task.updated"
	// TaskIDPrefix marks service tasks (system tasks use "sys-").
	TaskIDPrefix = "task_"
	// Network is the Docker network every service task joins (§8).
	Network = "syncloud"
)

const (
	resyncEvery     = 30 * time.Second
	pendingResend   = 30 * time.Second // re-send RunTask for tasks that never reported
	keepStopped     = 20               // stopped tasks kept per service for history
	stopTimeout     = 10               // seconds
	failureWindow   = 5 * time.Minute
	failuresBackoff = 3 // failures within the window before backing off
)

// NetworkReady reports whether tasks can be placed on a node.
type NetworkReady func(nodeID string) bool

// Manager owns the reconciler (§5.2) and scheduler (§5.3).
type Manager struct {
	st       *store.Store
	gw       *agentgw.Gateway
	nodes    *nodes.Registry
	netReady NetworkReady
	bus      *events.Bus
	log      *slog.Logger
	now      func() time.Time
	// OnChange runs after a reconcile that changed tasks (routes, DNS, VIPs).
	OnChange func()

	queue    chan string
	mu       sync.Mutex
	queued   map[string]bool
	specs    map[string]Spec        // "<service>:<revision>" -> spec (immutable)
	failures map[string][]time.Time // service -> recent task failures
	routes   routeCache
}

func NewManager(st *store.Store, gw *agentgw.Gateway, reg *nodes.Registry, netReady NetworkReady, bus *events.Bus, log *slog.Logger) *Manager {
	if netReady == nil {
		netReady = func(string) bool { return true }
	}
	return &Manager{
		st: st, gw: gw, nodes: reg, netReady: netReady, bus: bus, log: log, now: time.Now,
		queue: make(chan string, 1024), queued: map[string]bool{}, specs: map[string]Spec{}, failures: map[string][]time.Time{},
	}
}

func (m *Manager) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnConnect: m.onConnect, OnTaskStatus: m.onTaskStatus}
}

// Enqueue schedules a reconcile of one service.
func (m *Manager) Enqueue(serviceID string) {
	m.mu.Lock()
	if m.queued[serviceID] {
		m.mu.Unlock()
		return
	}
	m.queued[serviceID] = true
	m.mu.Unlock()
	select {
	case m.queue <- serviceID:
	default: // full: the periodic resync catches up
		m.mu.Lock()
		delete(m.queued, serviceID)
		m.mu.Unlock()
	}
}

// EnqueueAll schedules every service.
func (m *Manager) EnqueueAll() {
	svcs, err := m.st.ListServices(context.Background())
	if err != nil {
		m.log.Error("list services", "err", err)
		return
	}
	for _, sv := range svcs {
		m.Enqueue(sv.ID)
	}
}

func (m *Manager) enqueueAfter(serviceID string, d time.Duration) {
	time.AfterFunc(d, func() { m.Enqueue(serviceID) })
}

// Run processes the queue until ctx ends. Services are reconciled one at a
// time per service; different services run in parallel.
func (m *Manager) Run(ctx context.Context) {
	const workers = 4
	for range workers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-m.queue:
					m.mu.Lock()
					delete(m.queued, id)
					m.mu.Unlock()
					m.serialize(id, func() { m.reconcile(ctx, id) })
				}
			}
		}()
	}
	sub := m.bus.Subscribe(64, nodes.TopicNode, "node.removed")
	defer sub.Close()
	t := time.NewTicker(resyncEvery)
	defer t.Stop()
	m.EnqueueAll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.EnqueueAll()
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			// A node changed status: reconcile services with tasks there.
			if v, isView := e.Data.(nodes.View); isView && v.Status == store.NodeReady {
				continue
			}
			m.EnqueueAll()
		}
	}
}

var serviceLocks sync.Map

func (m *Manager) serialize(id string, f func()) {
	mu, _ := serviceLocks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	f()
}

// SpecFor returns a revision's spec (cached; revisions never change).
func (m *Manager) SpecFor(ctx context.Context, serviceID string, rev int) (Spec, error) {
	key := fmt.Sprintf("%s:%d", serviceID, rev)
	m.mu.Lock()
	s, ok := m.specs[key]
	m.mu.Unlock()
	if ok {
		return s, nil
	}
	td, err := m.st.TaskDefinition(ctx, serviceID, rev)
	if err != nil {
		return Spec{}, err
	}
	s, err = ParseSpec(td.Spec)
	if err != nil {
		return Spec{}, err
	}
	m.mu.Lock()
	m.specs[key] = s
	m.mu.Unlock()
	return s, nil
}

func live(t store.Task) bool {
	switch t.State {
	case store.TaskPending, store.TaskPulling, store.TaskStarting, store.TaskRunning:
		return true
	}
	return false
}

// reconcile drives one service towards its desired state (§5.2).
func (m *Manager) reconcile(ctx context.Context, serviceID string) {
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if errors.Is(err, store.ErrNotFound) {
		return
	} else if err != nil {
		m.log.Error("load service", "service", serviceID, "err", err)
		return
	}
	spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		m.log.Error("load task definition", "service", serviceID, "err", err)
		return
	}
	tasks, err := m.st.ServiceTasks(ctx, sv.ID, 0)
	if err != nil {
		m.log.Error("load tasks", "service", serviceID, "err", err)
		return
	}
	now := m.now()
	changed := false
	status := ""
	var active []store.Task
	for _, t := range tasks {
		if t.Desired != "running" {
			continue
		}
		node, ok := m.nodes.Get(t.NodeID)
		switch {
		case t.NodeID == "" || !ok || node.Status == store.NodeNotReady:
			// The node is gone or unreachable: replace the task elsewhere.
			m.setDesired(ctx, t, store.TaskLost, now)
			m.sendStop(t)
			changed = true
		case t.State == store.TaskExited || t.State == store.TaskFailed:
			m.setDesired(ctx, t, "", now)
			m.sendStop(t) // remove the dead container
			m.recordFailure(sv.ID, now)
			changed = true
		default:
			if t.State == store.TaskPending && now.Sub(t.UpdatedAt) > pendingResend {
				m.sendRun(ctx, sv, spec, t)
			}
			active = append(active, t)
		}
	}

	var current, old []store.Task
	for _, t := range active {
		if t.Revision == sv.Revision {
			current = append(current, t)
		} else {
			old = append(old, t)
		}
	}
	desired := sv.DesiredCount

	// Too many of the current revision: stop the extras (not running first, then newest).
	for len(current) > desired {
		i := pickVictim(current)
		m.stop(ctx, current[i], now)
		current = append(current[:i], current[i+1:]...)
		changed = true
	}

	// Start missing tasks, up to 200% of desired while old tasks drain (§5.4).
	missing := desired - len(current)
	if missing > 0 {
		if wait := m.backoff(sv.ID, now); wait > 0 {
			status = fmt.Sprintf("tasks keep failing; next attempt in %s", wait.Round(time.Second))
			m.enqueueAfter(sv.ID, wait)
		} else {
			room := max(2*desired, desired+1) - len(current) - len(old)
			if len(old) == 0 {
				room = missing
			}
			for range min(missing, room) {
				nodeID, why := m.place(ctx, spec, sv.ID)
				if nodeID == "" {
					status = "cannot place task: " + why
					m.enqueueAfter(sv.ID, 15*time.Second)
					break
				}
				t := store.Task{ID: auth.NewID(TaskIDPrefix), ServiceID: sv.ID, Revision: sv.Revision, NodeID: nodeID,
					Desired: "running", State: store.TaskPending, CreatedAt: now.Truncate(time.Second)}
				if err := m.st.CreateTask(ctx, t); err != nil {
					m.log.Error("create task", "service", sv.ID, "err", err)
					break
				}
				m.publishTask(ctx, t)
				m.sendRun(ctx, sv, spec, t)
				current = append(current, t)
				changed = true
			}
		}
	}

	// Retire old revisions as new tasks become running, keeping the running
	// total at the desired count.
	runningNew := 0
	for _, t := range current {
		if t.State == store.TaskRunning {
			runningNew++
		}
	}
	for keepOld := max(desired-runningNew, 0); len(old) > keepOld; {
		i := pickVictim(old)
		m.stop(ctx, old[i], now)
		old = append(old[:i], old[i+1:]...)
		changed = true
	}

	if sv.Deleting && len(current)+len(old) == 0 {
		remaining, _ := m.st.ServiceTasks(ctx, sv.ID, keepStopped)
		stillStopping := false
		for _, t := range remaining {
			if t.State != store.TaskStopped && t.State != store.TaskLost && t.State != store.TaskExited && t.State != store.TaskFailed {
				stillStopping = true
			}
		}
		if !stillStopping {
			if err := m.st.DeleteService(ctx, sv.ID); err != nil {
				m.log.Error("delete service", "service", sv.ID, "err", err)
				return
			}
			m.log.Info("service deleted", "service", sv.Project+"/"+sv.Environment+"/"+sv.Name)
			m.routesDirty()
			m.bus.Publish(TopicService, map[string]any{"id": sv.ID, "deleted": true})
			if m.OnChange != nil {
				m.OnChange()
			}
			return
		}
		m.enqueueAfter(sv.ID, 3*time.Second)
	}
	if err := m.st.SetServiceStatus(ctx, sv.ID, status); err != nil {
		m.log.Warn("service status", "err", err)
	}
	if changed {
		_ = m.st.PruneTasks(ctx, sv.ID, keepStopped)
		m.routesDirty()
		if m.OnChange != nil {
			m.OnChange()
		}
	}
	if changed || status != sv.Status {
		if v, err := m.ServiceView(ctx, sv.ID); err == nil {
			m.bus.Publish(TopicService, v)
		}
	}
}

// pickVictim prefers tasks that are not running yet, then the newest.
func pickVictim(ts []store.Task) int {
	best := 0
	for i, t := range ts {
		b := ts[best]
		if (t.State != store.TaskRunning) != (b.State != store.TaskRunning) {
			if t.State != store.TaskRunning {
				best = i
			}
			continue
		}
		if t.CreatedAt.After(b.CreatedAt) {
			best = i
		}
	}
	return best
}

func (m *Manager) recordFailure(serviceID string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keep []time.Time
	for _, t := range m.failures[serviceID] {
		if now.Sub(t) < failureWindow {
			keep = append(keep, t)
		}
	}
	m.failures[serviceID] = append(keep, now)
}

// backoff returns how long to wait before starting tasks of a crash-looping service.
func (m *Manager) backoff(serviceID string, now time.Time) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	var recent []time.Time
	for _, t := range m.failures[serviceID] {
		if now.Sub(t) < failureWindow {
			recent = append(recent, t)
		}
	}
	m.failures[serviceID] = recent
	if len(recent) < failuresBackoff {
		return 0
	}
	delay := min(time.Duration(1<<min(len(recent)-failuresBackoff, 5))*5*time.Second, 2*time.Minute)
	return max(recent[len(recent)-1].Add(delay).Sub(now), 0)
}

// place builds candidates from live node state and current reservations.
func (m *Manager) place(ctx context.Context, spec Spec, serviceID string) (string, string) {
	all, err := m.st.ActiveTasks(ctx)
	if err != nil {
		return "", err.Error()
	}
	type usage struct {
		cpu      float64
		mem      int
		svc, all int
	}
	used := map[string]*usage{}
	for _, t := range all {
		u := used[t.NodeID]
		if u == nil {
			u = &usage{}
			used[t.NodeID] = u
		}
		if s, err := m.SpecFor(ctx, t.ServiceID, t.Revision); err == nil {
			u.cpu += s.Resources.CPU
			u.mem += s.Resources.Memory
		}
		u.all++
		if t.ServiceID == serviceID {
			u.svc++
		}
	}
	var cands []Candidate
	for _, n := range m.nodes.List() {
		cpu, mem := Allocatable(n.Info.CPUCores, n.Info.MemoryBytes)
		c := Candidate{ID: n.ID, Name: n.Name, CPU: cpu, MemoryMiB: mem, Eligible: true}
		if u := used[n.ID]; u != nil {
			c.UsedCPU, c.UsedMemory, c.ServiceRuns, c.TotalRuns = u.cpu, u.mem, u.svc, u.all
		}
		switch {
		case n.Status != store.NodeReady || !n.Connected:
			c.Eligible, c.Why = false, "not ready"
		case !n.Schedulable:
			c.Eligible, c.Why = false, "not schedulable"
		case n.Info.DockerVersion == "":
			c.Eligible, c.Why = false, "without Docker"
		case !m.netReady(n.ID):
			c.Eligible, c.Why = false, "network not ready"
		}
		cands = append(cands, c)
	}
	return Place(cands, spec.Resources, spec.Placement.Strategy)
}

func (m *Manager) setDesired(ctx context.Context, t store.Task, state string, now time.Time) {
	if err := m.st.SetTaskDesired(ctx, t.ID, "stopped", state, now); err != nil {
		m.log.Error("update task", "task", t.ID, "err", err)
	}
	m.routesDirty() // stop sending traffic before the container stops
	if state != "" {
		t.State = state
	}
	t.Desired = "stopped"
	m.publishTask(ctx, t)
}

func (m *Manager) stop(ctx context.Context, t store.Task, now time.Time) {
	m.setDesired(ctx, t, "", now)
	m.sendStop(t)
}

func (m *Manager) sendStop(t store.Task) {
	if t.NodeID == "" {
		return
	}
	err := m.gw.Send(t.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
		TaskId: t.ID, TimeoutSeconds: stopTimeout, Remove: true,
	}}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("send stop", "task", t.ID, "err", err)
	}
}

func (m *Manager) sendRun(ctx context.Context, sv store.Service, spec Spec, t store.Task) {
	if t.Revision != sv.Revision {
		s, err := m.SpecFor(ctx, sv.ID, t.Revision)
		if err != nil {
			return
		}
		spec = s
	}
	err := m.gw.Send(t.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: TaskSpec(sv, spec, t)}}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("send run", "task", t.ID, "err", err)
	}
}

// TaskSpec is what the agent runs for a task.
func TaskSpec(sv store.Service, spec Spec, t store.Task) *agentv1.TaskSpec {
	env := map[string]string{}
	for _, k := range sortedKeys(spec.Env) {
		env[k] = spec.Env[k]
	}
	env["SYNCLOUD_PROJECT"] = sv.Project
	env["SYNCLOUD_ENVIRONMENT"] = sv.Environment
	env["SYNCLOUD_SERVICE"] = sv.Name
	env["SYNCLOUD_TASK_ID"] = t.ID
	env["SYNCLOUD_REVISION"] = fmt.Sprint(t.Revision)
	ts := &agentv1.TaskSpec{
		TaskId:  t.ID,
		Name:    fmt.Sprintf("%s-%s-%s-%s", sv.Project, sv.Environment, sv.Name, strings.TrimPrefix(t.ID, TaskIDPrefix)[:8]),
		Image:   spec.Image,
		Command: spec.Command,
		Env:     env,
		Labels: map[string]string{
			"syncloud.project": sv.Project, "syncloud.environment": sv.Environment, "syncloud.service": sv.Name,
			"syncloud.service_id": sv.ID, "syncloud.revision": fmt.Sprint(t.Revision),
		},
		NetworkMode:      Network,
		Restart:          agentv1.RestartPolicy_RESTART_POLICY_NO, // the reconciler replaces failed tasks
		MemoryLimitBytes: int64(spec.Resources.MemoryLimit) << 20,
	}
	if spec.Resources.CPULimit > 0 {
		ts.NanoCpus = int64(spec.Resources.CPULimit * 1e9)
	}
	return ts
}

// ── agent reports ───────────────────────────────────────────────────────────

func stateOf(s agentv1.TaskState) string {
	switch s {
	case agentv1.TaskState_TASK_STATE_PULLING:
		return store.TaskPulling
	case agentv1.TaskState_TASK_STATE_STARTING:
		return store.TaskStarting
	case agentv1.TaskState_TASK_STATE_RUNNING:
		return store.TaskRunning
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
	if !strings.HasPrefix(s.GetTaskId(), TaskIDPrefix) {
		return
	}
	ctx := context.Background()
	t, err := m.st.TaskByID(ctx, s.GetTaskId())
	if errors.Is(err, store.ErrNotFound) {
		// A container for a task we no longer know: remove it.
		if s.GetState() != agentv1.TaskState_TASK_STATE_REMOVED {
			m.sendStop(store.Task{ID: s.GetTaskId(), NodeID: node.ID})
		}
		return
	} else if err != nil {
		m.log.Error("load task", "task", s.GetTaskId(), "err", err)
		return
	}
	now := m.now().UTC()
	state := stateOf(s.GetState())
	if state == store.TaskStopped && t.Desired == "running" {
		state = store.TaskExited // the container vanished
		if s.GetError() == "" {
			s.Error = "container removed"
		}
	}
	if t.Desired == "stopped" && (state == store.TaskExited || state == store.TaskFailed) && t.State == store.TaskStopped {
		return // late report for a task already finished
	}
	t.State, t.IP, t.Health, t.ExitCode, t.Error, t.UpdatedAt = state, s.GetIp(), s.GetHealth(), int(s.GetExitCode()), s.GetError(), now
	if s.GetContainerId() != "" {
		t.ContainerID = s.GetContainerId()
	}
	if state == store.TaskRunning && s.GetStartedAtUnix() > 0 {
		at := time.Unix(s.GetStartedAtUnix(), 0).UTC()
		t.StartedAt = &at
	}
	if state == store.TaskExited || state == store.TaskFailed || state == store.TaskStopped {
		t.FinishedAt = &now
		if state != store.TaskRunning {
			t.IP = ""
		}
	}
	if err := m.st.UpdateTaskStatus(ctx, t); err != nil {
		m.log.Error("update task", "task", t.ID, "err", err)
		return
	}
	m.routesDirty()
	m.publishTask(ctx, t)
	m.Enqueue(t.ServiceID)
}

// onConnect compares the node's containers with what it should run.
func (m *Manager) onConnect(c agentgw.Conn) {
	ctx := context.Background()
	have := map[string]*agentv1.TaskStatus{}
	for _, s := range c.Hello.GetTasks() {
		if strings.HasPrefix(s.GetTaskId(), TaskIDPrefix) {
			have[s.GetTaskId()] = s
		}
	}
	tasks, err := m.st.NodeTasks(ctx, c.Node.ID)
	if err != nil {
		m.log.Error("node tasks", "node", c.Node.Name, "err", err)
		return
	}
	known := map[string]bool{}
	for _, t := range tasks {
		known[t.ID] = true
		s, ok := have[t.ID]
		switch {
		case ok:
			m.onTaskStatus(c.Node, s)
			if t.Desired == "stopped" {
				m.sendStop(t)
			}
		case t.Desired == "running" && (t.State == store.TaskRunning || t.State == store.TaskExited):
			m.onTaskStatus(c.Node, &agentv1.TaskStatus{TaskId: t.ID, State: agentv1.TaskState_TASK_STATE_EXITED, Error: "container disappeared while the node was disconnected"})
		case t.Desired == "running":
			if sv, err := m.st.ServiceByID(ctx, t.ServiceID); err == nil {
				if spec, err := m.SpecFor(ctx, sv.ID, t.Revision); err == nil {
					m.sendRun(ctx, sv, spec, t)
				}
			}
		default: // desired stopped and no container: done
			_ = m.st.SetTaskDesired(ctx, t.ID, "stopped", store.TaskStopped, m.now())
		}
	}
	for id, s := range have {
		if !known[id] {
			m.onTaskStatus(c.Node, s) // unknown or finished tasks are removed there
		}
	}
}

func (m *Manager) publishTask(ctx context.Context, t store.Task) {
	if v, err := m.taskView(ctx, t); err == nil {
		m.bus.Publish(TopicTask, v)
	}
}
