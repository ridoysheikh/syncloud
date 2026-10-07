package dbs

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"syncloud/internal/workload"
)

// Autoscaling timings (Phase 12).
const (
	memoryUpAfter     = 30 * time.Second
	memoryDownAfter   = 30 * time.Minute
	memoryLow         = 40.0 // % of maxmemory below which memory shrinks
	replicasUpAfter   = time.Minute
	replicasDownAfter = 10 * time.Minute
	cooldown          = 2 * time.Minute
)

// autoState tracks how long a condition has held, per database.
type autoState struct {
	memHigh, memLow, cpuHigh, cpuLow time.Time
	lastMemory, lastReplicas         time.Time
	// Last measurements and why nothing changed, for the dashboard.
	MemoryPercent float64
	ReadCPU       float64
	Blocked       string
}

// AutoscaleStatus is the autoscaler's view of a database.
type AutoscaleStatus struct {
	MemoryPercent float64 `json:"memoryPercent"` // used / maxmemory on the primary
	ReadCPU       float64 `json:"readCpu"`       // avg CPU % of the members serving reads
	Blocked       string  `json:"blocked,omitempty"`
}

// Autoscale returns the latest autoscaler measurements.
func (m *Manager) Autoscale(id string) AutoscaleStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.auto[id]
	if a == nil {
		return AutoscaleStatus{}
	}
	return AutoscaleStatus{MemoryPercent: a.MemoryPercent, ReadCPU: a.ReadCPU, Blocked: a.Blocked}
}

// since keeps the start time of a condition while it holds.
func since(t *time.Time, cond bool, now time.Time) time.Duration {
	if !cond {
		*t = time.Time{}
		return 0
	}
	if t.IsZero() {
		*t = now
	}
	return now.Sub(*t)
}

// roundUp64 rounds MiB up to a multiple of 64.
func roundUp64(v int) int { return (v + 63) / 64 * 64 }

// autoscale moves memory and the replica count towards the load.
func (m *Manager) autoscale(ctx context.Context, id string) {
	d, err := m.st.DatabaseByID(ctx, id)
	if err != nil || d.Deleting {
		return
	}
	spec, err := parseSpec(d.Spec)
	if err != nil {
		return
	}
	st := parseState(d.State)
	members, err := m.st.DatabaseMembers(ctx, id)
	if err != nil {
		return
	}
	now := m.now()
	m.mu.Lock()
	a := m.auto[id]
	if a == nil {
		a = &autoState{}
		m.auto[id] = a
	}
	m.mu.Unlock()

	var primary *Live
	var readers []*Live
	var dataNodes []string
	for _, mb := range members {
		if mb.Kind != KindData {
			continue
		}
		dataNodes = append(dataNodes, mb.NodeID)
		l := m.liveOf(mb.ID)
		if l == nil || l.Info == nil || now.Sub(l.At) > 3*probeEvery {
			continue
		}
		if mb.Ordinal == st.Primary {
			primary = l
		} else {
			readers = append(readers, l)
		}
	}
	if primary == nil {
		return // nothing fresh to go on
	}
	if len(readers) == 0 {
		readers = []*Live{primary} // reads hit the primary
	}
	var cpu float64
	for _, l := range readers {
		cpu += l.Info.Float("_cpu_percent")
	}
	cpu /= float64(len(readers))
	used := primary.Info.Float("used_memory")
	memPct := 0.0
	if mx := primary.Info.Float("maxmemory"); mx > 0 {
		memPct = used / mx * 100
	}
	m.mu.Lock()
	a.MemoryPercent, a.ReadCPU, a.Blocked = memPct, cpu, ""
	m.mu.Unlock()

	changed := false
	// Memory: up quickly when full, down slowly when mostly empty.
	if spec.Memory.Min < spec.Memory.Max && now.Sub(a.lastMemory) > cooldown {
		switch {
		case since(&a.memHigh, memPct > spec.Autoscaling.MemoryHigh && st.MemoryMiB < spec.Memory.Max, now) >= memoryUpAfter:
			next := min(spec.Memory.Max, roundUp64(st.MemoryMiB*3/2))
			if why := m.room(ctx, dataNodes, reservation(next)-reservation(st.MemoryMiB)); why != "" {
				m.mu.Lock()
				a.Blocked = "memory cannot grow: " + why
				m.mu.Unlock()
				break
			}
			m.event(ctx, id, "memory", fmt.Sprintf("%d MiB", st.MemoryMiB), fmt.Sprintf("%d MiB", next),
				fmt.Sprintf("used %.0f%% of maxmemory for %s", memPct, memoryUpAfter), "autoscaler")
			st.MemoryMiB, a.lastMemory, a.memHigh, changed = next, now, time.Time{}, true
		case since(&a.memLow, memPct < memoryLow && st.MemoryMiB > spec.Memory.Min, now) >= memoryDownAfter:
			next := max(spec.Memory.Min, roundUp64(st.MemoryMiB*3/4), roundUp64(int(used*13/10)>>20))
			if next < st.MemoryMiB {
				m.event(ctx, id, "memory", fmt.Sprintf("%d MiB", st.MemoryMiB), fmt.Sprintf("%d MiB", next),
					fmt.Sprintf("used %.0f%% of maxmemory for %s", memPct, memoryDownAfter), "autoscaler")
				st.MemoryMiB, a.lastMemory, changed = next, now, true
			}
			a.memLow = time.Time{}
		}
	}
	// Read replicas: out on sustained CPU above target, in well below it.
	target := spec.Autoscaling.CPUTarget
	if spec.Replicas.Min < spec.Replicas.Max && now.Sub(a.lastReplicas) > cooldown {
		switch {
		case since(&a.cpuHigh, cpu > target && st.Replicas < spec.Replicas.Max, now) >= replicasUpAfter:
			m.event(ctx, id, "replicas", strconv.Itoa(st.Replicas), strconv.Itoa(st.Replicas+1),
				fmt.Sprintf("read CPU %.0f%% above the %.0f%% target for %s", cpu, target, replicasUpAfter), "autoscaler")
			st.Replicas, a.lastReplicas, a.cpuHigh, changed = st.Replicas+1, now, time.Time{}, true
		case since(&a.cpuLow, cpu < target/2 && st.Replicas > spec.Replicas.Min, now) >= replicasDownAfter:
			m.event(ctx, id, "replicas", strconv.Itoa(st.Replicas), strconv.Itoa(st.Replicas-1),
				fmt.Sprintf("read CPU %.0f%% below half the %.0f%% target for %s", cpu, target, replicasDownAfter), "autoscaler")
			st.Replicas, a.lastReplicas, a.cpuLow, changed = st.Replicas-1, now, time.Time{}, true
		}
	}
	if changed {
		if err := m.st.SetDatabaseState(ctx, id, encode(st)); err != nil {
			m.log.Error("database autoscale", "database", id, "err", err)
			return
		}
		m.log.Info("database autoscaled", "database", d.Name, "memoryMiB", st.MemoryMiB, "replicas", st.Replicas)
		m.Enqueue(id) // members follow; memory is applied on the next probe
		m.publish(ctx, id)
	}
}

// room checks every node of the database can reserve extra MiB more.
func (m *Manager) room(ctx context.Context, nodeIDs []string, extra int) string {
	reserved := m.wl.Reserved(ctx)
	for _, id := range nodeIDs {
		n, ok := m.nodes.Get(id)
		if !ok {
			continue
		}
		_, alloc := workload.Allocatable(n.Info.CPUCores, n.Info.MemoryBytes)
		if free := alloc - reserved[id].MemoryMiB; free < extra {
			return fmt.Sprintf("node %s has %d MiB free, %d MiB needed", n.Name, max(free, 0), extra)
		}
	}
	return ""
}
