package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// A failed deployment fires an alert that stays active through more
// failures and resolves once a later deployment of the service succeeds.
func TestFailureAlertResolvesOnSuccess(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	now := time.Now().UTC().Truncate(time.Second)
	clock := now
	m.now = func() time.Time { return clock }
	if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, store.Service{ID: "svc_1", EnvironmentID: "env_1", Name: "web", DesiredCount: 1, CreatedAt: now}, `{"v":1}`, "test"); err != nil {
		t.Fatal(err)
	}
	r, err := m.PutRule(ctx, Rule{Name: "deploys", Type: "deployment", Severity: "critical", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m.cursor = now.Add(-time.Second)
	deploy := func(id, status string, at time.Time) {
		t.Helper()
		if err := st.StartDeployment(ctx, store.Deployment{ID: id, ServiceID: "svc_1", ToRev: 1, Status: store.DeployInProgress, StartedAt: at, Trigger: "manual"}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishDeployment(ctx, id, status, "boom", at); err != nil {
			t.Fatal(err)
		}
	}
	active := func() []store.AlertState {
		t.Helper()
		a, err := m.Active(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	kinds := func() []string {
		t.Helper()
		ev, err := st.ListAlertEvents(ctx, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := len(ev) - 1; i >= 0; i-- {
			out = append(out, ev[i].Kind)
		}
		return out
	}

	deploy("dep_1", "failed", now)
	clock = now.Add(30 * time.Second)
	m.Evaluate(ctx)
	if a := active(); len(a) != 1 || a[0].State != "firing" || a[0].Key != "svc:svc_1" {
		t.Fatalf("after failure: %+v", a)
	}

	// A second failure keeps the one alert and does not notify again.
	deploy("dep_2", "failed", now.Add(40*time.Second))
	clock = now.Add(60 * time.Second)
	m.Evaluate(ctx)
	if a := active(); len(a) != 1 {
		t.Fatalf("after second failure: %+v", a)
	}
	if k := kinds(); len(k) != 1 || k[0] != "firing" {
		t.Fatalf("events after second failure: %v", k)
	}

	// The service is live again: the alert resolves.
	deploy("dep_3", "succeeded", now.Add(70*time.Second))
	clock = now.Add(90 * time.Second)
	m.Evaluate(ctx)
	if a := active(); len(a) != 0 {
		t.Fatalf("after success: %+v", a)
	}
	if k := kinds(); len(k) != 2 || k[1] != "resolved" {
		t.Fatalf("events after success: %v", k)
	}

	// The active alert survives a restart and still resolves.
	deploy("dep_4", "failed", now.Add(100*time.Second))
	clock = now.Add(120 * time.Second)
	m.Evaluate(ctx)
	m2, _ := newTestManager(t)
	m2.st, m2.now, m2.cursor = st, m.now, clock
	states, _ := st.ListAlertStates(ctx)
	for i := range states {
		s := states[i]
		if m2.states[s.RuleID] == nil {
			m2.states[s.RuleID] = map[string]*store.AlertState{}
		}
		m2.states[s.RuleID][s.Key] = &s
	}
	deploy("dep_5", "succeeded", now.Add(130*time.Second))
	clock = now.Add(150 * time.Second)
	m2.Evaluate(ctx)
	if a, _ := m2.Active(ctx); len(a) != 0 {
		t.Fatalf("after restart and success: %+v", a)
	}

	// Disabling the rule clears its active alerts.
	deploy("dep_6", "failed", now.Add(160*time.Second))
	clock = now.Add(180 * time.Second)
	m2.Evaluate(ctx)
	if a, _ := m2.Active(ctx); len(a) != 1 {
		t.Fatalf("before disable: %+v", a)
	}
	r.Enabled = false
	if _, err := m2.PutRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	m2.Evaluate(ctx)
	if a, _ := m2.Active(ctx); len(a) != 0 {
		t.Fatalf("after disable: %+v", a)
	}
}
