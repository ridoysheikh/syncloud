package workload

import "testing"

func node(id string, runs int, load float64) Candidate {
	return Candidate{ID: id, Name: id, CPU: 4, MemoryMiB: 4096, CPULoad: load * 4, UsedMemory: 512, TotalRuns: runs, Eligible: true}
}

func task(id, on string, cpu float64, mem int, runs map[string]int) moveTask {
	return moveTask{ID: id, ServiceID: "svc-" + id, Node: on, CPU: cpu, Memory: mem,
		Fits: map[string]bool{"a": true, "b": true, "c": true}, SvcRuns: runs}
}

func TestPlanMoveCounts(t *testing.T) {
	nodes := []Candidate{node("a", 6, 0.1), node("b", 1, 0.1), node("c", 3, 0.1)}
	one := map[string]int{"a": 1}
	tasks := []moveTask{task("heavy", "a", 1, 900, one), task("light", "a", 0.05, 50, one)}
	mv, ok := planMove(nodes, nil, tasks)
	if !ok || mv.Task.ID != "light" || mv.To != "b" {
		t.Fatalf("%+v %v", mv, ok)
	}
	// Two apart at most: nothing to do.
	nodes[0].TotalRuns, nodes[1].TotalRuns, nodes[2].TotalRuns = 3, 2, 2
	if mv, ok := planMove(nodes, nil, tasks); ok {
		t.Fatalf("moved %+v", mv)
	}
	// Never doubles a service up on the target.
	nodes[0].TotalRuns = 6
	both := map[string]int{"a": 1, "b": 1, "c": 1}
	if mv, ok := planMove(nodes, nil, []moveTask{task("x", "a", 0.1, 64, both)}); ok {
		t.Fatalf("moved %+v", mv)
	}
	// Nor to a node it may not run on, or a busy one.
	x := task("x", "a", 0.1, 64, one)
	x.Fits = map[string]bool{"a": true, "c": true}
	nodes[2].CPULoad = 3.5
	if mv, ok := planMove(nodes, nil, []moveTask{x}); ok {
		t.Fatalf("moved %+v", mv)
	}
}

func TestPlanMoveBusy(t *testing.T) {
	nodes := []Candidate{node("a", 3, 0.9), node("b", 3, 0.1), node("c", 3, 0.6)}
	one := map[string]int{"a": 1}
	tasks := []moveTask{task("small", "a", 0.2, 64, one), task("big", "a", 2, 256, one), task("huge", "a", 3.5, 256, one)}
	if mv, ok := planMove(nodes, nil, tasks); ok {
		t.Fatalf("moved before the node stayed busy: %+v", mv)
	}
	// huge would make b busy; big fits.
	mv, ok := planMove(nodes, map[string]bool{"a": true}, tasks)
	if !ok || mv.Task.ID != "big" || mv.To != "b" {
		t.Fatalf("%+v %v", mv, ok)
	}
	// Memory pressure moves the task using the most memory.
	nodes[0].CPULoad, nodes[0].UsedMemory = 0.4, 3700
	tasks[0].Memory = 1200
	mv, ok = planMove(nodes, map[string]bool{"a": true}, tasks)
	if !ok || mv.Task.ID != "small" {
		t.Fatalf("%+v %v", mv, ok)
	}
}
