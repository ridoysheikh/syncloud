package workload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/agentgw"
	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/events"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/mesh"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/store"
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
	// OnChange runs after a reconcile that changed tasks or services (routes, certificates).
	OnChange func()
	// OnTaskChange runs whenever a task's state changes (service directory).
	OnTaskChange func()
	// AdmitCount checks a count quota (domains) before adding one.
	AdmitCount func(ctx context.Context, envID, what string) error
	unplacedMu sync.Mutex
	unplaced   map[string]Unplaced
	// NodePool names a node's pool and its role ("" and "worker" for nodes
	// outside any pool); edge nodes only run tasks that ask for their pool.
	NodePool func(nodeID string) (pool, role string)
	// Admit checks a change against quotas (§7.2); nil admits everything.
	Admit func(ctx context.Context, req AdmitRequest) error
	// Reachable reports whether the controller can reach a task over the
	// private network (§5.6); routes leave out tasks it cannot.
	Reachable func(taskID string) bool
	// DNS returns the resolver and search domains for a task on a node
	// (nil before the node's private network exists).
	DNS func(nodeID string, sv store.Service) (servers, search []string)
	// Hooks runs pre- and post-deploy jobs (§5.11); may be nil.
	DeployHooks DeployHooks
	// ResolveImage turns "@registry/…" into the private registry's reference
	// and returns pull credentials for the node (§5.9); may be nil.
	ResolveImage func(image string) (ref, registryAuth string)
	// RegistryCA returns a certificate nodes must trust to pull a resolved
	// image ("" = none): the platform registry's, while self-signed.
	RegistryCA func(ref string) string
	// S3Bindings lists a service's S3 bindings for a new revision; S3Env
	// turns one into environment variables with credentials (§16).
	S3Bindings func(ctx context.Context, serviceID string) ([]S3Ref, error)
	S3Env      func(ctx context.Context, ref S3Ref) (map[string]string, error)
	// ExtraUsage is per-node reservations outside service tasks (database
	// members), counted when placing.
	ExtraUsage func(ctx context.Context) map[string]Usage
	// ImageAvailable reports whether an image can still be pulled (false
	// once registry cleanup removed it); may be nil.
	ImageAvailable func(ctx context.Context, image string) (bool, error)
	// PublicPortRange is where public TCP/UDP ports are assigned from (zero:
	// DefaultPublicPorts); OnPublicPorts runs when the set of public ports
	// changes (Traefik entrypoints and firewall rules follow it).
	PublicPortRange [2]int
	OnPublicPorts   func()

	queue    chan string
	mu       sync.Mutex
	queued   map[string]bool
	specs    map[string]Spec        // "<service>:<revision>" -> spec (immutable)
	failures map[string][]time.Time // service -> recent task failures
	routes   routeCache
	progress sync.Map // deployment ID -> tasks of the new revision last seen serving
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
	spec = spec.WithProjectNodes(m.projectNodes(ctx, sv.Project))
	tasks, err := m.st.ServiceTasks(ctx, sv.ID, 0)
	if err != nil {
		m.log.Error("load tasks", "service", serviceID, "err", err)
		return
	}
	now := m.now()
	changed := false
	status := ""
	dep, depErr := m.st.ActiveDeployment(ctx, sv.ID)
	hasDep := depErr == nil && dep.ToRev == sv.Revision
	failed := func(t store.Task) {
		m.recordFailure(sv.ID, now)
		if hasDep && t.Revision == dep.ToRev {
			if n, err := m.st.IncDeploymentFailures(ctx, dep.ID); err == nil {
				dep.Failed = n
			}
			m.DeploymentEvent(ctx, dep.ID, "task-failed", taskFailure(t))
		}
	}
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
			failed(t)
			changed = true
		case t.State == store.TaskRunning && t.Health == "unhealthy":
			// Failed its health check (§5.6): replace it.
			m.log.Info("replacing unhealthy task", "task", t.ID, "service", sv.Name)
			m.setDesired(ctx, t, "", now)
			m.sendStop(t)
			failed(t)
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
		// Tasks on a draining node are retired like an old revision, so
		// replacements start elsewhere before they stop.
		if n, ok := m.nodes.Get(t.NodeID); ok && (n.Draining || !spec.Placement.Allows(n.Name)) {
			// Also tasks on a node the project or service no longer allows.
			old = append(old, t)
			continue
		}
		if t.Revision == sv.Revision {
			current = append(current, t)
		} else {
			old = append(old, t)
		}
	}
	desired := sv.DesiredCount
	if spec.Image == AwaitingBuild {
		desired, status = 0, "waiting for the first build"
	}
	if sv.Held {
		// The first deployment waits on its pre-deploy jobs (§5.11).
		desired, status = 0, "waiting for the first pre-deploy jobs to pass"
		if ds, err := m.st.ListDeployments(ctx, sv.ID, 1, ""); err == nil && len(ds) > 0 && ds[0].Status != store.DeployWaitingHook {
			status = "the first pre-deploy jobs did not pass: fix them, then redeploy"
		}
	}

	// Too many of the current revision: stop the extras (not running first, then newest).
	for len(current) > desired {
		i := pickVictim(current)
		m.stop(ctx, current[i], now)
		current = append(current[:i], current[i+1:]...)
		changed = true
	}

	// Start missing tasks, up to 200% of desired while old tasks drain (§5.4).
	missing := desired - len(current)
	if missing <= 0 {
		m.trackUnplaced(sv.ID, spec, 0, "", now)
	}
	if missing > 0 {
		if wait := m.backoff(sv.ID, now); wait > 0 {
			status = fmt.Sprintf("tasks keep failing; next attempt in %s", wait.Round(time.Second))
			m.enqueueAfter(sv.ID, wait)
		} else {
			room := max(2*desired, desired+1) - len(current) - len(old)
			if len(old) == 0 {
				room = missing
			}
			placed := 0
			defer func() { m.trackUnplaced(sv.ID, spec, missing-placed, status, now) }()
			for range min(missing, room) {
				nodeID, why := m.place(ctx, spec, sv.ID, sv.Revision, false)
				if nodeID == "" {
					status = "cannot place task: " + why
					m.enqueueAfter(sv.ID, 15*time.Second)
					break
				}
				placed++
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
			if hasDep && placed > 0 {
				m.DeploymentEvent(ctx, dep.ID, "tasks-started", fmt.Sprintf("started %d %s of revision %d", placed, plural(placed, "task"), sv.Revision))
			}
		}
	}

	// Retire old revisions as new tasks become serving (running, and healthy
	// when the revision has a health check), keeping the serving total at the
	// desired count.
	servingNew := 0
	for _, t := range current {
		if m.serving(ctx, t) {
			servingNew++
		}
	}
	if hasDep {
		if last, _ := m.progress.Load(dep.ID); last == nil || last.(int) < servingNew {
			m.progress.Store(dep.ID, servingNew)
			if servingNew > 0 {
				m.DeploymentEvent(ctx, dep.ID, "serving", fmt.Sprintf("%d of %d tasks of revision %d serve traffic", servingNew, desired, dep.ToRev))
			}
		}
	}
	retired := 0
	for keepOld := max(desired-servingNew, 0); len(old) > keepOld; {
		i := pickVictim(old)
		m.stop(ctx, old[i], now)
		old = append(old[:i], old[i+1:]...)
		changed = true
		retired++
	}
	if hasDep && retired > 0 {
		m.DeploymentEvent(ctx, dep.ID, "drained", fmt.Sprintf("stopped %d old %s", retired, plural(retired, "task")))
	}

	// Deployment outcome (§5.4): done when every desired task of the new
	// revision serves and the old ones are gone; the circuit breaker trips
	// when too many new tasks fail.
	if hasDep {
		threshold := min(max((desired+1)/2, 3), 200)
		switch {
		case *spec.Deployment.CircuitBreaker && dep.Failed >= threshold:
			msg := fmt.Sprintf("%d tasks of revision %d failed or turned unhealthy", dep.Failed, dep.ToRev)
			if *spec.Deployment.Rollback && dep.FromRev > 0 {
				_ = m.st.FinishDeployment(ctx, dep.ID, store.DeployRolledBack, msg+"; rolled back to revision "+fmt.Sprint(dep.FromRev), now)
				m.log.Warn("deployment failed; rolling back", "service", sv.Name, "revision", dep.ToRev, "to", dep.FromRev)
				m.DeploymentEvent(ctx, dep.ID, store.DeployRolledBack, fmt.Sprintf("circuit breaker tripped (%s); rolling back to revision %d", msg, dep.FromRev))
				if _, err := m.rollback(WithCause(ctx, Cause{Trigger: store.TriggerAutoRollback}), sv, dep.FromRev, actorCircuitBreaker, "automatic rollback: "+msg, false); err != nil {
					m.log.Error("automatic rollback", "service", sv.ID, "err", err)
				}
				return
			}
			_ = m.st.FinishDeployment(ctx, dep.ID, store.DeployFailed, msg, now)
			status = "deployment failed: " + msg
			m.DeploymentEvent(ctx, dep.ID, store.DeployFailed, "circuit breaker tripped: "+msg)
			changed = true
		case servingNew >= desired && len(old) == 0:
			_ = m.st.FinishDeployment(ctx, dep.ID, store.DeploySucceeded, "", now)
			m.DeploymentEvent(ctx, dep.ID, store.DeploySucceeded, fmt.Sprintf("all %d tasks of revision %d serve traffic", desired, dep.ToRev))
			m.progress.Delete(dep.ID)
			if m.DeployHooks != nil {
				m.DeployHooks.RunPostDeploy(context.WithoutCancel(ctx), sv, dep)
			}
			changed = true
		}
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
			hadPublic := false
			if rows, err := m.st.ListRouting(ctx, sv.ID); err == nil {
				for _, r := range rows {
					hadPublic = hadPublic || r.PublicPort > 0
				}
			}
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
			if hadPublic && m.OnPublicPorts != nil {
				m.OnPublicPorts() // its entrypoints and firewall rules go
			}
			m.finishDeletions(ctx, sv.EnvironmentID)
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

// serving reports whether a task should receive traffic: running, and
// healthy when its revision defines a health check.
func (m *Manager) serving(ctx context.Context, t store.Task) bool {
	if t.State != store.TaskRunning || t.Desired != "running" {
		return false
	}
	spec, err := m.SpecFor(ctx, t.ServiceID, t.Revision)
	if err != nil {
		return false
	}
	return spec.Health == nil || t.Health == "healthy"
}

// poolAllows applies placement pools: tasks run in the listed pools, or in
// any worker pool when none is listed (edge pools only when named).
func (m *Manager) poolAllows(nodeID string, pools []string) bool {
	pool, role := "default", "worker"
	if m.NodePool != nil {
		if p, r := m.NodePool(nodeID); p != "" {
			pool, role = p, r
		}
	}
	if len(pools) == 0 {
		return role != "edge"
	}
	for _, p := range pools {
		if p == pool {
			return true
		}
	}
	return false
}

// Unplaced is demand the cluster cannot place: tasks a service is missing
// because no node has room (the cluster autoscaler's signal, §6.5).
type Unplaced struct {
	ServiceID string
	Count     int
	CPU       float64 // per task
	MemoryMiB int
	Pools     []string
	Since     time.Time
}

func (m *Manager) trackUnplaced(serviceID string, spec Spec, count int, status string, now time.Time) {
	m.unplacedMu.Lock()
	defer m.unplacedMu.Unlock()
	if m.unplaced == nil {
		m.unplaced = map[string]Unplaced{}
	}
	if count <= 0 || !strings.HasPrefix(status, "cannot place task") {
		delete(m.unplaced, serviceID)
		return
	}
	u, ok := m.unplaced[serviceID]
	if !ok {
		u.Since = now
	}
	// Only reserved CPU can keep a task waiting; memory always can.
	u.ServiceID, u.Count, u.CPU, u.MemoryMiB, u.Pools = serviceID, count, spec.Resources.ReservedCPU(), spec.Resources.Memory, spec.Placement.Pools
	m.unplaced[serviceID] = u
}

// UnplacedDemand lists services waiting for capacity.
func (m *Manager) UnplacedDemand() []Unplaced {
	m.unplacedMu.Lock()
	defer m.unplacedMu.Unlock()
	out := make([]Unplaced, 0, len(m.unplaced))
	for _, u := range m.unplaced {
		out = append(out, u)
	}
	return out
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

// place picks a node for a new task of serviceID's revision rev, from live
// node state, reservations and real usage. Old revisions of the service do
// not count against it: a rollout retires them, and waiting for room they
// are about to free would stall the deploy. Spreading likewise counts only
// tasks of that revision.
func (m *Manager) place(ctx context.Context, spec Spec, serviceID string, rev int, platform bool) (string, string) {
	all, err := m.st.ActiveTasks(ctx)
	if err != nil {
		return "", err.Error()
	}
	type usage struct {
		cpu      float64 // reserved
		mem      int     // reserved
		expected int     // memory every task is expected to use
		starting int     // memory of tasks not running yet
		svc, all int
	}
	used := map[string]*usage{}
	at := func(nodeID string) *usage {
		u := used[nodeID]
		if u == nil {
			u = &usage{}
			used[nodeID] = u
		}
		return u
	}
	for _, t := range all {
		u := at(t.NodeID)
		u.all++
		if t.ServiceID == serviceID && t.Revision == rev {
			u.svc++
		}
		s, err := m.SpecFor(ctx, t.ServiceID, t.Revision)
		if err != nil {
			continue
		}
		u.expected += s.Resources.Memory
		if t.State != store.TaskRunning {
			u.starting += s.Resources.Memory
		}
		if t.ServiceID == serviceID && t.Revision != rev {
			continue // being replaced
		}
		u.cpu += s.Resources.ReservedCPU()
		u.mem += s.Resources.ReservedMemory()
	}
	if m.ExtraUsage != nil { // database members (Phase 12): their memory is reserved, their CPU shared
		for nodeID, x := range m.ExtraUsage(ctx) {
			u := at(nodeID)
			u.mem += x.MemoryMiB
			u.expected += x.MemoryMiB
			u.all += x.Count
		}
	}
	var cands []Candidate
	for _, n := range m.nodes.List() {
		cpu, mem := Allocatable(n.Info.CPUCores, n.Info.MemoryBytes)
		c := Candidate{ID: n.ID, Name: n.Name, CPU: cpu, MemoryMiB: mem, Eligible: true}
		u := used[n.ID]
		if u == nil {
			u = &usage{}
		}
		c.ReservedCPU, c.ReservedMemory, c.ServiceRuns, c.TotalRuns = u.cpu, u.mem, u.svc, u.all
		if mt := n.Metrics; mt != nil && mt.MemoryTotalBytes > 0 {
			c.UsedMemory = int(mt.MemoryUsedBytes>>20) + u.starting
			c.CPULoad = mt.CPUPercent / 100 * float64(n.Info.CPUCores)
		} else {
			c.UsedMemory = u.expected // no report yet: assume tasks use what they expect
		}
		switch {
		case n.Status != store.NodeReady || !n.Connected:
			c.Eligible, c.Why = false, "not ready"
		case !takesTasks(n.Name, n.Schedulable, n.Draining, spec.Placement, platform):
			c.Eligible, c.Why = false, "not schedulable"
		case n.Info.DockerVersion == "":
			c.Eligible, c.Why = false, "without Docker"
		case !m.netReady(n.ID):
			c.Eligible, c.Why = false, "network not ready"
		case !spec.Placement.Allows(n.Name):
			c.Eligible, c.Why = false, "not an allowed node"
		case !m.poolAllows(n.ID, spec.Placement.Pools):
			c.Eligible, c.Why = false, "in another node pool"
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
	if m.OnTaskChange != nil {
		m.OnTaskChange()
	}
	if state != "" {
		t.State = state
	}
	t.Desired = "stopped"
	m.publishTask(ctx, t)
}

// stop retires a task. One that was serving leaves the routes and VIPs at
// once and keeps running for its drain time (§5.4): otherwise Traefik, which
// polls its configuration every 2s, can still send a request to a container
// that is gone, and it hangs instead of failing over.
func (m *Manager) stop(ctx context.Context, t store.Task, now time.Time) {
	serving := t.State == store.TaskRunning && t.Desired == "running"
	m.setDesired(ctx, t, "", now)
	if d := m.drainTime(ctx, t); serving && d > 0 {
		time.AfterFunc(d, func() { m.sendStop(t) })
		return
	}
	m.sendStop(t)
}

func (m *Manager) drainTime(ctx context.Context, t store.Task) time.Duration {
	spec, err := m.SpecFor(ctx, t.ServiceID, t.Revision)
	if err != nil || len(spec.Ports) == 0 {
		return 0 // nothing routes to it
	}
	if d := spec.Deployment.DrainSeconds; d != nil {
		return time.Duration(*d) * time.Second
	}
	return DefaultDrain
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
	ts := m.RunSpec(sv, spec, t)
	err := m.gw.Send(t.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: ts}}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("send run", "task", t.ID, "err", err)
	}
}

// DeployHooks lets the jobs subsystem gate and follow deployments.
type DeployHooks interface {
	// HasPreDeploy reports whether the service has pre-deploy jobs.
	HasPreDeploy(ctx context.Context, sv store.Service) bool
	// RunPreDeploy starts them for revision toRev of deployment depID; the
	// outcome comes back through Manager.PreDeployDone.
	RunPreDeploy(ctx context.Context, sv store.Service, toRev int, depID string)
	// RunPostDeploy starts post-deploy jobs after a deployment succeeded.
	RunPostDeploy(ctx context.Context, sv store.Service, dep store.Deployment)
	// CancelHooks stops the pre-deploy runs of a cancelled deployment.
	CancelHooks(ctx context.Context, depID string)
}

// takesTasks reports whether a node accepts a new task: schedulable nodes
// do; the controller node also takes platform tasks, and tasks that name it
// explicitly even when it runs no general workloads (unless draining).
func takesTasks(name string, schedulable, draining bool, p Placement, platform bool) bool {
	if schedulable {
		return true
	}
	return name == mesh.ControllerNode && (platform || p.Names(name) && !draining)
}

// projectNodes is the project's allowed nodes (none = any).
func (m *Manager) projectNodes(ctx context.Context, project string) []string {
	p, err := m.st.ProjectByName(ctx, project)
	if err != nil {
		return nil
	}
	return p.Nodes
}

// RestrictToEnvironment limits spec to the allowed nodes of the
// environment's project (for job runs).
func (m *Manager) RestrictToEnvironment(ctx context.Context, envID string, spec Spec) Spec {
	e, err := m.st.EnvironmentByID(ctx, envID)
	if err != nil {
		return spec
	}
	p, err := m.st.ProjectByID(ctx, e.ProjectID)
	if err != nil {
		return spec
	}
	return spec.WithProjectNodes(p.Nodes)
}

// Reserved is every node's reservations: service tasks plus ExtraUsage.
func (m *Manager) Reserved(ctx context.Context) map[string]Usage {
	out := map[string]Usage{}
	if all, err := m.st.ActiveTasks(ctx); err == nil {
		for _, t := range all {
			u := out[t.NodeID]
			if s, err := m.SpecFor(ctx, t.ServiceID, t.Revision); err == nil {
				u.CPU += s.Resources.CPU
				u.MemoryMiB += s.Resources.Memory
			}
			u.Count++
			out[t.NodeID] = u
		}
	}
	if m.ExtraUsage != nil {
		for id, x := range m.ExtraUsage(ctx) {
			u := out[id]
			u.CPU, u.MemoryMiB, u.Count = u.CPU+x.CPU, u.MemoryMiB+x.MemoryMiB, u.Count+x.Count
			out[id] = u
		}
	}
	return out
}

// Usage is resources reserved on a node outside service tasks.
type Usage struct {
	CPU       float64
	MemoryMiB int
	Count     int
}

// PlaceSpec picks a node for a one-off task with spec's resources.
func (m *Manager) PlaceSpec(ctx context.Context, spec Spec) (string, string) {
	return m.place(ctx, spec, "", 0, false)
}

// PlaceBuild picks a node for a platform build: like PlaceSpec, but the
// controller node is a candidate even when it takes no user tasks, as
// BuildKit runs there by default (§5.0) and small clusters may have no
// worker with room for a build.
func (m *Manager) PlaceBuild(ctx context.Context, spec Spec) (string, string) {
	return m.place(ctx, spec, "", 0, true)
}

// RunSpec builds the agent spec for a job run on a node, with the node's DNS.
func (m *Manager) RunSpec(sv store.Service, spec Spec, t store.Task) *agentv1.TaskSpec {
	ts := TaskSpec(sv, spec, t)
	if m.DNS != nil {
		ts.DnsServers, ts.DnsSearch = m.DNS(t.NodeID, sv)
	}
	if m.ResolveImage != nil {
		ts.Image, ts.RegistryAuth = m.ResolveImage(ts.Image)
	}
	if m.RegistryCA != nil {
		ts.RegistryCa = m.RegistryCA(ts.Image)
	}
	if m.S3Env != nil {
		for _, ref := range spec.S3 {
			env, err := m.S3Env(context.Background(), ref)
			if err != nil {
				m.log.Warn("S3 binding", "service", sv.Name, "endpoint", ref.Endpoint, "err", err)
				continue
			}
			for k, v := range env {
				if _, set := spec.Env[k]; !set { // the service's own env wins
					ts.Env[k] = v
				}
			}
		}
	}
	return ts
}

// reservedWeight is how much more CPU weight a reserved core has than a
// shared one. Under contention reserved tasks keep their cores while the
// shared tasks' cpu adds up to less than reservedWeight × the unreserved
// cores.
const reservedWeight = 8

// CPUShares is the Docker CPU weight of a task: its cpu in units of 1024 per
// core, reservedWeight times that when reserved, within Docker's 2–262144.
func CPUShares(r Resources) int64 {
	w := r.CPU * 1024
	if r.CPUMode == ResourceReserved {
		w *= reservedWeight
	}
	return min(max(int64(w), 2), 262144)
}

// TaskSpec is what the agent runs for a task.
func TaskSpec(sv store.Service, spec Spec, t store.Task) *agentv1.TaskSpec {
	env := map[string]string{}
	for k, v := range spec.SharedEnv {
		env[k] = v
	}
	for _, k := range sortedKeys(spec.Env) {
		env[k] = spec.Env[k]
	}
	env["SYNCLOUD_PROJECT"] = sv.Project
	env["SYNCLOUD_ENVIRONMENT"] = sv.Environment
	env["SYNCLOUD_SERVICE"] = sv.Name
	env["SYNCLOUD_TASK_ID"] = t.ID
	env["SYNCLOUD_REVISION"] = fmt.Sprint(t.Revision)
	short := strings.TrimPrefix(strings.TrimPrefix(t.ID, TaskIDPrefix), "run_")
	ts := &agentv1.TaskSpec{
		TaskId:     t.ID,
		Name:       fmt.Sprintf("%s-%s-%s-%s", sv.Project, sv.Environment, sv.Name, short[:min(8, len(short))]),
		Image:      spec.Image,
		Entrypoint: spec.Entrypoint,
		Command:    spec.Command,
		Env:        env,
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
	ts.CpuShares = CPUShares(spec.Resources)
	ts.MemoryReservationBytes = int64(spec.Resources.ReservedMemory()) << 20
	if h := spec.Health; h != nil {
		port, _ := spec.PortNumber(h.Port)
		ts.Health = &agentv1.HealthCheck{
			Type: h.Type, Path: h.Path, Port: uint32(port), Command: h.Command,
			IntervalSeconds: uint32(h.Interval), TimeoutSeconds: uint32(h.Timeout), Retries: uint32(h.Retries), StartPeriodSeconds: uint32(h.StartPeriod),
		}
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

// recordAddress keeps the IP history (§8.2).
func (m *Manager) recordAddress(ctx context.Context, t store.Task, prevIP, nodeID string, now time.Time) {
	switch {
	case t.IP != "" && t.IP != prevIP:
		owner := t.ServiceID
		if sv, err := m.st.ServiceByID(ctx, t.ServiceID); err == nil {
			owner = sv.Project + "/" + sv.Environment + "/" + sv.Name
		}
		if err := m.st.AssignAddress(ctx, t.IP, t.ID, owner, nodeID, now); err != nil {
			m.log.Warn("record address", "task", t.ID, "err", err)
		}
	case t.IP == "" && prevIP != "":
		_ = m.st.ReleaseAddress(ctx, t.ID, now)
	}
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
	if t.Desired == "stopped" && state == store.TaskStopped && (t.State == store.TaskExited || t.State == store.TaskFailed) {
		return // removing a dead container: keep why the task failed
	}
	if t.Desired == "stopped" && t.State == store.TaskLost && state != store.TaskStopped {
		// The node was given up and came back with the container still
		// there; a replacement already runs elsewhere. Remove it, keep "lost".
		t.NodeID = node.ID
		m.sendStop(t)
		return
	}
	prevIP := t.IP
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
	m.recordAddress(ctx, t, prevIP, node.ID, now)
	m.routesDirty()
	if m.OnTaskChange != nil {
		m.OnTaskChange()
	}
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
			} else if sv, err := m.st.ServiceByID(ctx, t.ServiceID); err == nil {
				// Idempotent: lets a restarted agent resume health probes.
				if spec, err := m.SpecFor(ctx, sv.ID, t.Revision); err == nil {
					m.sendRun(ctx, sv, spec, t)
				}
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
