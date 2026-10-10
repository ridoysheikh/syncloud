package workload

import "testing"

func TestPlace(t *testing.T) {
	r := Resources{CPU: 0.5, Memory: 256}
	cands := []Candidate{
		{ID: "a", Name: "a", CPU: 2, MemoryMiB: 2048, ServiceRuns: 1, Eligible: true},
		{ID: "b", Name: "b", CPU: 2, MemoryMiB: 2048, Eligible: true},
		{ID: "c", Name: "c", CPU: 2, MemoryMiB: 2048, Eligible: false, Why: "not ready"},
		{ID: "d", Name: "d", CPU: 2, MemoryMiB: 2048, UsedMemory: 1900, Eligible: true},
	}
	if id, _ := Place(cands, r, "spread"); id != "b" {
		t.Fatalf("spread picked %s", id)
	}
	cands[1].UsedMemory = 1500 // b: 548 free, a: 2048 free
	if id, _ := Place(cands, r, "binpack"); id != "b" {
		t.Fatalf("binpack picked %s", id)
	}
	// Shared CPU never rules a node out, however much is asked.
	if id, why := Place(cands, Resources{CPU: 8, Memory: 64}, "spread"); id == "" {
		t.Fatalf("shared CPU blocked placement: %s", why)
	}
	// Reserved CPU needs that many unreserved cores.
	id, why := Place(cands, Resources{CPU: 8, Memory: 64, CPUMode: ResourceReserved}, "spread")
	if id != "" || why != "no node fits (3 not enough unreserved CPU, 1 not ready)" {
		t.Fatalf("%q %q", id, why)
	}
}

// The real model: a node full of idle shared tasks has room; reserved
// resources count whether used or not; memory goes by the larger of the two.
func TestPlaceSharedAndReserved(t *testing.T) {
	busy := Candidate{ID: "busy", Name: "busy", CPU: 3.9, MemoryMiB: 3600, CPULoad: 3.5, UsedMemory: 800, Eligible: true}
	idle := Candidate{ID: "idle", Name: "idle", CPU: 3.9, MemoryMiB: 3600, CPULoad: 0.2, UsedMemory: 800, Eligible: true}
	shared := Resources{CPU: 1, Memory: 512}
	if id, _ := Place([]Candidate{busy, idle}, shared, "spread"); id != "idle" {
		t.Errorf("spread ignored the real CPU load: %s", id)
	}
	// Reserved by others but idle: reserved CPU only stops reserved tasks.
	busy.ReservedCPU, idle.ReservedCPU = 3.5, 3.5
	if id, _ := Place([]Candidate{idle}, shared, "spread"); id != "idle" {
		t.Error("reserved CPU of others blocked a shared task")
	}
	if id, why := Place([]Candidate{idle}, Resources{CPU: 1, Memory: 64, CPUMode: ResourceReserved}, "spread"); id != "" {
		t.Error("reserved CPU over-committed")
	} else if why != "no node fits (1 not enough unreserved CPU)" {
		t.Error(why)
	}
	// Memory: reserved but unused still counts, and so does used but unreserved.
	idle.ReservedMemory, idle.UsedMemory = 3300, 500
	if id, _ := Place([]Candidate{idle}, shared, "spread"); id != "" {
		t.Error("reserved memory was handed out")
	}
	idle.ReservedMemory, idle.UsedMemory = 0, 3300
	if id, why := Place([]Candidate{idle}, shared, "spread"); id != "" || why != "no node fits (1 not enough memory)" {
		t.Errorf("memory in use was handed out: %q %q", id, why)
	}
	idle.UsedMemory = 3000 // 600 free
	if id, _ := Place([]Candidate{idle}, shared, "spread"); id != "idle" {
		t.Error("free memory refused")
	}
}

func TestCPUShares(t *testing.T) {
	for _, c := range []struct {
		r    Resources
		want int64
	}{
		{Resources{CPU: 0.1}, 102},
		{Resources{CPU: 1}, 1024},
		{Resources{CPU: 1, CPUMode: ResourceReserved}, 8192},
		{Resources{CPU: 0.001}, 2},
		{Resources{CPU: 256, CPUMode: ResourceReserved}, 262144},
	} {
		if got := CPUShares(c.r); got != c.want {
			t.Errorf("%+v: %d, want %d", c.r, got, c.want)
		}
	}
}

func TestSpecNormalize(t *testing.T) {
	s := Spec{Image: "nginx:1.27", Ports: []Port{{Container: 80}, {Container: 9000, Protocol: "tcp"}}}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Ports[0].Name != "http" || s.Ports[1].Name != "9000" || s.Resources.MemoryLimit != 256 || s.Placement.Strategy != "spread" {
		t.Fatalf("%+v", s)
	}
	m := Spec{Image: "x", Resources: Resources{CPUMode: "shared", MemoryMode: "reserved"}}
	if err := m.Normalize(); err != nil || m.Resources.CPUMode != ResourceShared || m.Resources.ReservedMemory() != 128 || m.Resources.ReservedCPU() != 0 {
		t.Fatalf("modes: %v %+v", err, m.Resources)
	}
	for _, bad := range []Spec{{}, {Image: "a b"}, {Image: "x", Resources: Resources{CPUMode: "dedicated"}}, {Image: "x", Env: map[string]string{"1X": "y"}}, {Image: "x", Env: map[string]string{"SYNCLOUD_X": "y"}},
		{Image: "x", Ports: []Port{{Container: 0}}}, {Image: "x", Ports: []Port{{Name: "a", Container: 1}, {Name: "a", Container: 2}}}} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
