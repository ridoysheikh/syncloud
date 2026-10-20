package workload

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Rebalancing (§5.3): placement only decides where new tasks go, so a
// cluster drifts out of balance as nodes join, services scale and load
// changes. The rebalancer watches what nodes really use and moves one
// service task at a time:
//
//   - off a node that stays busy (busyCPU or busyMemory) to one with room;
//   - from the node with the most tasks to the one with the fewest, while
//     they differ by two or more.
//
// A move starts the replacement first and retires the old task once the
// replacement serves, like a rollout, so it costs no downtime. A move never
// puts more of a service's tasks on one node than before, and never makes
// the target busy. Database members count as load but are not moved: their
// data lives on their node.
const (
	rebalanceEvery   = 20 * time.Second
	rebalanceSustain = 3                // checks in a row a node must stay busy
	moveCooldown     = time.Minute      // between two moves
	serviceCooldown  = 10 * time.Minute // between two moves of one service
	minTaskAge       = 2 * time.Minute  // tasks running for less stay put
	moveTimeout      = 10 * time.Minute // a replacement that never serves is given up
	// moveHeadroom keeps a target clear of busy after a move, so the task
	// does not bounce back.
	moveHeadroom = 0.1
)

type rebalancer struct {
	moving sync.Map // task ID -> time.Time: its replacement is starting
	usage  sync.Map // task ID -> taskUsage, from heartbeats
	// Used by the rebalancer's goroutine only.
	hotTicks map[string]int
	lastMove time.Time
	movedSvc map[string]time.Time
}

type taskUsage struct {
	CPU    float64 // cores
	Memory int     // MiB
}

// onHeartbeat records what each task really uses, smoothed over heartbeats.
func (m *Manager) onHeartbeat(_ store.Node, hb *agentv1.Heartbeat) {
	for _, s := range hb.GetTasks() {
		u := taskUsage{CPU: s.GetCpuPercent() / 100, Memory: int(s.GetMemoryBytes() >> 20)}
		if v, ok := m.usage.Load(s.GetTaskId()); ok {
			p := v.(taskUsage)
			u = taskUsage{CPU: (p.CPU + u.CPU) / 2, Memory: (p.Memory + u.Memory) / 2}
		}
		m.usage.Store(s.GetTaskId(), u)
	}
}

func (m *Manager) runRebalancer(ctx context.Context) {
	m.hotTicks, m.movedSvc = map[string]int{}, map[string]time.Time{}
	t := time.NewTicker(rebalanceEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.rebalanceOnce(ctx)
		}
	}
}

func (m *Manager) rebalanceOnce(ctx context.Context) {
	now := m.now()
	inflight := false
	m.moving.Range(func(k, v any) bool {
		t, err := m.st.TaskByID(ctx, k.(string))
		switch {
		case err != nil || t.Desired != "running":
			m.moving.Delete(k) // retired: the move is done
		case now.Sub(v.(time.Time)) > moveTimeout:
			// The replacement never served: keep the old task, and let the
			// reconciler stop the extra one.
			m.log.Warn("rebalance: replacement did not serve; keeping the task where it is", "task", t.ID)
			m.moving.Delete(k)
			m.Enqueue(t.ServiceID)
		default:
			inflight = true
		}
		return true
	})
	nodes, err := m.candidates(ctx, Spec{}, "", 0, false)
	if err != nil {
		return
	}
	hot := map[string]bool{}
	for _, c := range nodes {
		if !c.busy() {
			delete(m.hotTicks, c.ID)
			continue
		}
		if m.hotTicks[c.ID]++; m.hotTicks[c.ID] >= rebalanceSustain {
			hot[c.ID] = true
		}
	}
	if inflight || now.Sub(m.lastMove) < moveCooldown {
		return
	}
	tasks := m.movableTasks(ctx, now)
	mv, ok := planMove(nodes, hot, tasks)
	if !ok {
		return
	}
	m.move(ctx, mv, now)
}

// moveTask is a task the rebalancer may move.
type moveTask struct {
	ID, ServiceID, Node string
	CPU                 float64 // cores in use
	Memory              int     // MiB in use
	// Fits holds the nodes the task may run on; SvcRuns its service's tasks
	// per node.
	Fits    map[string]bool
	SvcRuns map[string]int
}

type plannedMove struct {
	Task moveTask
	To   string
	Why  string
}

// planMove picks at most one move: off a node that stayed busy first, then
// from the node with the most tasks to the one with the fewest.
func planMove(nodes []Candidate, hot map[string]bool, tasks []moveTask) (plannedMove, bool) {
	byNode := map[string][]moveTask{}
	for _, t := range tasks {
		byNode[t.Node] = append(byNode[t.Node], t)
	}
	canTake := func(c Candidate, from string, t moveTask) bool {
		if !c.Eligible || c.ID == from || !t.Fits[c.ID] || c.busy() || c.FreeMemory() < t.Memory {
			return false
		}
		if t.SvcRuns[c.ID] >= t.SvcRuns[from] {
			return false // the service would no longer be spread as well
		}
		cpu := c.load()
		if c.CPU > 0 {
			cpu = (c.CPULoad + t.CPU) / c.CPU
		}
		mem := float64(max(c.ReservedMemory, c.UsedMemory)+t.Memory) / float64(c.MemoryMiB)
		return cpu < busyCPU-moveHeadroom && mem < busyMemory-moveHeadroom
	}
	// Targets with the fewest tasks first, then the least loaded.
	targets := append([]Candidate(nil), nodes...)
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if a.TotalRuns != b.TotalRuns {
			return a.TotalRuns < b.TotalRuns
		}
		return a.load()+a.memoryShare() < b.load()+b.memoryShare()
	})

	// Busy nodes: move the task that relieves the most and fits elsewhere.
	var sources []Candidate
	for _, c := range nodes {
		if hot[c.ID] {
			sources = append(sources, c)
		}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		return max(sources[i].load(), sources[i].memoryShare()) > max(sources[j].load(), sources[j].memoryShare())
	})
	for _, src := range sources {
		cpuBound := src.load() >= busyCPU
		ts := append([]moveTask(nil), byNode[src.ID]...)
		sort.SliceStable(ts, func(i, j int) bool {
			if cpuBound {
				return ts[i].CPU > ts[j].CPU
			}
			return ts[i].Memory > ts[j].Memory
		})
		why := fmt.Sprintf("%s is busy (CPU %.0f%%, memory %.0f%%)", src.Name, src.load()*100, src.memoryShare()*100)
		for _, t := range ts {
			for _, dst := range targets {
				if canTake(dst, src.ID, t) {
					return plannedMove{Task: t, To: dst.ID, Why: why}, true
				}
			}
		}
	}

	// Task counts: the lightest task that can go from a crowded node.
	crowded := append([]Candidate(nil), nodes...)
	sort.SliceStable(crowded, func(i, j int) bool { return crowded[i].TotalRuns > crowded[j].TotalRuns })
	for _, src := range crowded {
		if !src.Eligible {
			continue // tasks pinned to a node that takes no others
		}
		ts := append([]moveTask(nil), byNode[src.ID]...)
		sort.SliceStable(ts, func(i, j int) bool {
			return ts[i].CPU+float64(ts[i].Memory)/1024 < ts[j].CPU+float64(ts[j].Memory)/1024
		})
		for _, dst := range targets {
			if src.TotalRuns-dst.TotalRuns < 2 {
				break
			}
			for _, t := range ts {
				if canTake(dst, src.ID, t) {
					why := fmt.Sprintf("%s runs %d tasks and %s %d", src.Name, src.TotalRuns, dst.Name, dst.TotalRuns)
					return plannedMove{Task: t, To: dst.ID, Why: why}, true
				}
			}
		}
	}
	return plannedMove{}, false
}

// movableTasks is the tasks of services at rest: no deployment, every task
// of the current revision serving for minTaskAge, and not moved lately.
func (m *Manager) movableTasks(ctx context.Context, now time.Time) []moveTask {
	all, err := m.st.ActiveTasks(ctx)
	if err != nil {
		return nil
	}
	bySvc := map[string][]store.Task{}
	seen := map[string]bool{}
	for _, t := range all {
		seen[t.ID] = true
		if t.ServiceID != "" {
			bySvc[t.ServiceID] = append(bySvc[t.ServiceID], t)
		}
	}
	m.usage.Range(func(k, _ any) bool {
		if !seen[k.(string)] {
			m.usage.Delete(k)
		}
		return true
	})
	var out []moveTask
	for id, ts := range bySvc {
		if now.Sub(m.movedSvc[id]) < serviceCooldown {
			continue
		}
		sv, err := m.st.ServiceByID(ctx, id)
		if err != nil || sv.Held || sv.Deleting || len(ts) != sv.DesiredCount {
			continue
		}
		if _, err := m.st.ActiveDeployment(ctx, id); err == nil {
			continue
		}
		spec, err := m.SpecFor(ctx, id, sv.Revision)
		if err != nil || spec.Image == AwaitingBuild || spec.Placement.Strategy == "binpack" {
			continue
		}
		spec = spec.WithProjectNodes(m.projectNodes(ctx, sv.Project))
		settled := true
		for _, t := range ts {
			if t.Desired != "running" || t.Revision != sv.Revision || !m.serving(ctx, t) ||
				t.StartedAt == nil || now.Sub(*t.StartedAt) < minTaskAge {
				settled = false
			}
		}
		if !settled {
			continue
		}
		cands, err := m.candidates(ctx, spec, id, sv.Revision, false)
		if err != nil {
			continue
		}
		fits, runs := map[string]bool{}, map[string]int{}
		for _, c := range cands {
			fits[c.ID] = c.Eligible && (spec.Resources.CPUMode != ResourceReserved || c.CPU-c.ReservedCPU >= spec.Resources.CPU)
			runs[c.ID] = c.ServiceRuns
		}
		for _, t := range ts {
			mt := moveTask{ID: t.ID, ServiceID: id, Node: t.NodeID, CPU: spec.Resources.CPU, Memory: spec.Resources.Memory, Fits: fits, SvcRuns: runs}
			if v, ok := m.usage.Load(t.ID); ok {
				u := v.(taskUsage)
				mt.CPU, mt.Memory = u.CPU, u.Memory
			}
			if r := spec.Resources.ReservedMemory(); r > mt.Memory {
				mt.Memory = r
			}
			out = append(out, mt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// move starts the task's replacement on the target; the reconciler retires
// the task once the replacement serves.
func (m *Manager) move(ctx context.Context, mv plannedMove, now time.Time) {
	m.serialize(mv.Task.ServiceID, func() {
		sv, err := m.st.ServiceByID(ctx, mv.Task.ServiceID)
		if err != nil {
			return
		}
		t, err := m.st.TaskByID(ctx, mv.Task.ID)
		if err != nil || t.Desired != "running" || t.NodeID != mv.Task.Node || t.Revision != sv.Revision {
			return
		}
		spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			return
		}
		m.moving.Store(t.ID, now)
		nt, err := m.startTask(ctx, sv, spec, mv.To, now)
		if err != nil {
			m.moving.Delete(t.ID)
			return
		}
		m.lastMove, m.movedSvc[sv.ID] = now, now
		from, to := t.NodeID, mv.To
		if n, ok := m.nodes.Get(from); ok {
			from = n.Name
		}
		if n, ok := m.nodes.Get(to); ok {
			to = n.Name
		}
		m.log.Info("rebalance: moving task", "service", sv.Project+"/"+sv.Environment+"/"+sv.Name,
			"task", t.ID, "from", from, "to", to, "replacement", nt.ID, "why", mv.Why)
	})
	m.Enqueue(mv.Task.ServiceID)
}
