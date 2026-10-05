package workload

import (
	"fmt"
	"sort"
)

// Candidate is a node as the scheduler sees it.
type Candidate struct {
	ID, Name    string
	CPU         float64 // allocatable cores
	MemoryMiB   int     // allocatable MiB
	UsedCPU     float64 // reserved by active tasks
	UsedMemory  int
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

// Place picks a node for one task (§5.3): filter, then score. It returns ""
// and a reason when no node fits.
func Place(cands []Candidate, r Resources, strategy string) (string, string) {
	var fit []Candidate
	reasons := map[string]int{}
	for _, c := range cands {
		switch {
		case !c.Eligible:
			reasons[c.Why]++
		case c.CPU-c.UsedCPU < r.CPU:
			reasons["not enough CPU"]++
		case c.MemoryMiB-c.UsedMemory < r.Memory:
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
			fa, fb := a.MemoryMiB-a.UsedMemory, b.MemoryMiB-b.UsedMemory
			if fa != fb {
				return fa < fb
			}
		} else if a.ServiceRuns != b.ServiceRuns {
			return a.ServiceRuns < b.ServiceRuns // spread the service across nodes
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
