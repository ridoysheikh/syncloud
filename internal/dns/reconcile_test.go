package dns

import (
	"context"
	"reflect"
	"slices"
	"testing"
)

type memTracker struct{ managed map[string]bool }

func (t *memTracker) Created(r Record, _ Owner) error { t.managed[r.ID] = true; return nil }
func (t *memTracker) Removed(id string) error         { delete(t.managed, id); return nil }

func run(t *testing.T, m *Mem, tr *memTracker, wants []Want, confirmed bool) (PlanResult, Applied) {
	t.Helper()
	recs, _ := m.Records(context.Background(), m.Zone.ID)
	plan := Plan(recs, wants, tr.managed)
	got, err := Apply(context.Background(), m, m.Zone.ID, plan, confirmed, tr)
	if err != nil {
		t.Fatal(err)
	}
	return plan, got
}

func noop(a Applied) bool { return a.Created+a.Updated+a.Deleted == 0 && len(a.Skipped) == 0 }

func aWants(name string, ips ...string) []Want {
	var w []Want
	for _, ip := range ips {
		w = append(w, Want{Type: "A", Name: name, Content: ip, Exclusive: true, Owner: Owner{Kind: "domain", ID: name}})
	}
	return w
}

func TestReconcileCreatesAndIsIdempotent(t *testing.T) {
	m, tr := NewMem("example.com"), &memTracker{managed: map[string]bool{}}
	w := aWants("app.example.com", "203.0.113.1", "203.0.113.2")
	_, got := run(t, m, tr, w, false)
	if got.Created != 2 {
		t.Fatalf("%+v", got)
	}
	m.Calls = nil
	plan, got := run(t, m, tr, w, false) // second run: nothing
	if plan.Changes() != 0 || !noop(got) || len(m.Calls) != 0 {
		t.Fatalf("not idempotent: %+v %+v %v", plan, got, m.Calls)
	}
}

func TestReconcileCollapsesExactDuplicates(t *testing.T) {
	m := NewMem("example.com",
		Record{Type: "A", Name: "app.example.com", Content: "203.0.113.1"},
		Record{Type: "A", Name: "app.example.com", Content: "203.0.113.1"},
		Record{Type: "A", Name: "app.example.com", Content: "203.0.113.1"})
	tr := &memTracker{managed: map[string]bool{}}
	_, got := run(t, m, tr, aWants("app.example.com", "203.0.113.1"), false)
	if got.Deleted != 2 || got.Created != 0 || !reflect.DeepEqual(m.Snapshot(), []string{"A app.example.com 203.0.113.1"}) {
		t.Fatalf("%+v %v", got, m.Snapshot())
	}
}

func TestStaleManagedRecordIsRemoved(t *testing.T) {
	m, tr := NewMem("example.com"), &memTracker{managed: map[string]bool{}}
	run(t, m, tr, aWants("app.example.com", "203.0.113.1"), false)
	// The project moved to another load balancer.
	_, got := run(t, m, tr, aWants("app.example.com", "203.0.113.9"), false)
	if got.Deleted != 1 || got.Created != 1 || !reflect.DeepEqual(m.Snapshot(), []string{"A app.example.com 203.0.113.9"}) {
		t.Fatalf("%+v %v", got, m.Snapshot())
	}
}

func TestUnmanagedConflictNeedsConfirmation(t *testing.T) {
	m := NewMem("example.com",
		Record{Type: "A", Name: "app.example.com", Content: "198.51.100.7"}, // somebody else's
		Record{Type: "MX", Name: "app.example.com", Content: "mail.example.com"})
	tr := &memTracker{managed: map[string]bool{}}
	w := aWants("app.example.com", "203.0.113.1")

	plan, got := run(t, m, tr, w, false)
	if len(plan.Conflicts()) != 1 || got.Deleted != 0 || len(got.Skipped) != 1 || got.Created != 1 {
		t.Fatalf("%+v %+v", plan, got)
	}
	// The unmanaged A stays until confirmed; the MX is never touched.
	if !slices.Contains(m.Snapshot(), "A app.example.com 198.51.100.7") || !slices.Contains(m.Snapshot(), "MX app.example.com mail.example.com") {
		t.Fatal(m.Snapshot())
	}
	_, got = run(t, m, tr, w, true)
	if got.Deleted != 1 || slices.Contains(m.Snapshot(), "A app.example.com 198.51.100.7") || !slices.Contains(m.Snapshot(), "MX app.example.com mail.example.com") {
		t.Fatalf("%+v %v", got, m.Snapshot())
	}
}

func TestCNAMENeverBesideOtherRecords(t *testing.T) {
	m := NewMem("example.com", Record{Type: "CNAME", Name: "www.example.com", Content: "old.example.net"})
	tr := &memTracker{managed: map[string]bool{}}
	// Wanting an A where somebody's CNAME is: a conflict, not silently removed.
	plan, _ := run(t, m, tr, aWants("www.example.com", "203.0.113.1"), false)
	if len(plan.Conflicts()) != 1 {
		t.Fatalf("%+v", plan)
	}
	// Wanting a CNAME where records are.
	m2 := NewMem("example.com", Record{Type: "A", Name: "www.example.com", Content: "198.51.100.7"})
	tr2 := &memTracker{managed: map[string]bool{}}
	cname := []Want{{Type: "CNAME", Name: "www.example.com", Content: "lb.example.com", Exclusive: true}}
	_, got := run(t, m2, tr2, cname, true)
	if got.Deleted != 1 || !reflect.DeepEqual(m2.Snapshot(), []string{"CNAME www.example.com lb.example.com"}) {
		t.Fatalf("%+v %v", got, m2.Snapshot())
	}
}

func TestAdditiveTXTLeavesOtherChallengeRecords(t *testing.T) {
	// Another tool's _acme-challenge value is never touched, and several values at one name all stay.
	m := NewMem("example.com", Record{Type: "TXT", Name: "_acme-challenge.example.com", Content: `"someone-elses"`})
	tr := &memTracker{managed: map[string]bool{}}
	w := []Want{
		{Type: "TXT", Name: "_acme-challenge.example.com", Content: "value-a"},
		{Type: "TXT", Name: "_acme-challenge.example.com", Content: "value-b"},
	}
	plan, got := run(t, m, tr, w, false)
	if got.Created != 2 || len(plan.Conflicts()) != 0 || len(m.Snapshot()) != 3 {
		t.Fatalf("%+v %+v %v", plan, got, m.Snapshot())
	}
	if _, got = run(t, m, tr, w, false); !noop(got) {
		t.Fatalf("not idempotent: %+v", got)
	}
}

func TestProxiedMismatchUpdatesInPlace(t *testing.T) {
	m := NewMem("example.com", Record{Type: "A", Name: "app.example.com", Content: "203.0.113.1", Proxied: false})
	tr := &memTracker{managed: map[string]bool{}}
	yes := true
	w := []Want{{Type: "A", Name: "app.example.com", Content: "203.0.113.1", Proxied: &yes, Exclusive: true}}
	_, got := run(t, m, tr, w, false)
	if got.Updated != 1 || got.Created != 0 || got.Deleted != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestOnlyTouchedNamesChange(t *testing.T) {
	m := NewMem("example.com", Record{Type: "A", Name: "other.example.com", Content: "198.51.100.7"})
	tr := &memTracker{managed: map[string]bool{}}
	run(t, m, tr, aWants("app.example.com", "203.0.113.1"), true)
	if !slices.Contains(m.Snapshot(), "A other.example.com 198.51.100.7") {
		t.Fatal("touched a name nobody asked about")
	}
}
