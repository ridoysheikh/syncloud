package workload

import (
	"testing"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// memoryLimit -1 means no limit (Phase 27); left out it stays 2 × memory.
func TestNoMemoryLimit(t *testing.T) {
	s := Spec{Image: "app:1", Resources: Resources{MemoryLimit: NoMemoryLimit}}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Resources.Memory != 128 || s.Resources.MemoryLimit != NoMemoryLimit {
		t.Fatalf("resources %+v", s.Resources)
	}
	if ts := TaskSpec(store.Service{ID: "svc_1"}, s, store.Task{ID: "task_1"}); ts.GetMemoryLimitBytes() != 0 {
		t.Errorf("limit bytes %d, want none", ts.GetMemoryLimitBytes())
	}
	old := Spec{Image: "app:1"}
	if err := old.Normalize(); err != nil || old.Resources.MemoryLimit != 256 {
		t.Fatalf("default limit %+v %v", old.Resources, err)
	}
	if ts := TaskSpec(store.Service{ID: "svc_1"}, old, store.Task{ID: "task_1"}); ts.GetMemoryLimitBytes() != 256<<20 {
		t.Errorf("default limit bytes %d", ts.GetMemoryLimitBytes())
	}
	bad := Spec{Image: "app:1", Resources: Resources{Memory: 512, MemoryLimit: 100}}
	if bad.Normalize() == nil {
		t.Error("a limit below memory was accepted")
	}
}
