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
	id, why := Place(cands, Resources{CPU: 8, Memory: 64}, "spread")
	if id != "" || why != "no node fits (3 not enough CPU, 1 not ready)" {
		t.Fatalf("%q %q", id, why)
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
	for _, bad := range []Spec{{}, {Image: "a b"}, {Image: "x", Env: map[string]string{"1X": "y"}}, {Image: "x", Env: map[string]string{"SYNCLOUD_X": "y"}},
		{Image: "x", Ports: []Port{{Container: 0}}}, {Image: "x", Ports: []Port{{Name: "a", Container: 1}, {Name: "a", Container: 2}}}} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
