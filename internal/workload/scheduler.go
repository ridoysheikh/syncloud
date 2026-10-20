package workload

import (
	"fmt"
	"sort"
)

// Candidate is a node as the scheduler sees it.
type Candidate struct {
	ID, Name  string
	CPU       float64 // allocatable cores
	MemoryMiB int     // allocatable MiB
	// ReservedCPU and ReservedMemory are set aside by reserved tasks and
	// database members.
	ReservedCPU    float64
	ReservedMemory int
	// UsedMemory is what the node really uses (its last report), plus what
	// tasks still starting are expected to use. CPULoad is the cores in use.
	UsedMemory  int
	CPULoad     float64
	ServiceRuns int // active tasks of the service being placed
	TotalRuns   int // active tasks of all services
	Eligible    bool
	Why         string // why not eligible
}

// Allocatable keeps headroom for the OS, Docker and the agent.
func Allocatable(cores int, memoryBytes uint64) (float64, int) {
	mem := int(memoryBytes>>20) * 9 / 10
	cpu := float64(cores) - 0.1
	return max(cpu, 0), max(mem, 0)
}

// A node whose CPU or memory use is at least this share of what it can give
// is busy: spreading skips it while another node is not.
const (
	busyCPU    = 0.75
	busyMemory = 0.85
)

// FreeMemory is the memory a new task can have: what is neither reserved
// nor in use, whichever is more.
func (c Candidate) FreeMemory() int { return c.MemoryMiB - max(c.ReservedMemory, c.UsedMemory) }

// load is the share of the node's CPU in use.
func (c Candidate) load() float64 {
	if c.CPU <= 0 {
		return 1
	}
	return c.CPULoad / c.CPU
}

// memoryShare is the share of the node's memory reserved or in use.
func (c Candidate) memoryShare() float64 {
	if c.MemoryMiB <= 0 {
		return 1
	}
	return float64(max(c.ReservedMemory, c.UsedMemory)) / float64(c.MemoryMiB)
}

// busy reports a node near its limits: it takes new tasks only once the
// others are too, and the rebalancer moves tasks off it. Below that, real
// usage does not steer spreading: idle nodes differ in their baseline load
// (the controller runs the platform, a bigger node shows a lower share), and
// ranking by it sent every new service's first task to the same node.
func (c Candidate) busy() bool { return c.load() >= busyCPU || c.memoryShare() >= busyMemory }

// Place picks a node for one task (§5.3): filter, then score. It returns ""
// and a reason when no node fits.
//
// Shared CPU never rules a node out: CPU is shared out by weight, so a busy
// node is slower, not full. Reserved CPU needs that many unreserved cores.
// Memory, shared or reserved, needs that much free on the node (FreeMemory).
func Place(cands []Candidate, r Resources, strategy string) (string, string) {
	var fit []Candidate
	reasons := map[string]int{}
	for _, c := range cands {
		switch {
		case !c.Eligible:
			reasons[c.Why]++
		case r.CPUMode == ResourceReserved && c.CPU-c.ReservedCPU < r.CPU:
			reasons["not enough unreserved CPU"]++
		case c.FreeMemory() < r.Memory:
			reasons["not enough memory"]++
		default:
			fit = append(fit, c)
		}
	}
	if len(fit) == 0 {
		if len(cands) == 0 {
			return "", "no nodes"
		}
		return "", fmt.Sprintf("no node fits (%s)", summarize(reasons))
	}
	sort.SliceStable(fit, func(i, j int) bool {
		a, b := fit[i], fit[j]
		if strategy == "binpack" {
			// Least free memory first, so nodes fill up one by one.
			if fa, fb := a.FreeMemory(), b.FreeMemory(); fa != fb {
				return fa < fb
			}
		} else {
			if a.ServiceRuns != b.ServiceRuns {
				return a.ServiceRuns < b.ServiceRuns // spread the service across nodes
			}
			if ba, bb := a.busy(), b.busy(); ba != bb {
				return bb
			}
			if a.TotalRuns != b.TotalRuns {
				return a.TotalRuns < b.TotalRuns // then the same number of tasks on each node
			}
			// Then the node with the most CPU to spare, in 10% steps so
			// noise in the load does not reshuffle similar nodes.
			if la, lb := int(a.load()*10), int(b.load()*10); la != lb {
				return la < lb
			}
		}
		if a.TotalRuns != b.TotalRuns {
			return a.TotalRuns < b.TotalRuns
		}
		return a.Name < b.Name
	})
	return fit[0].ID, ""
}

func summarize(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for i, k := range keys {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%d %s", m[k], k)
	}
	return s
}
