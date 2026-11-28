package alerts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// A failed deployment's alert resolves once the service is healthy again by
// itself, for example when what it depends on comes back: nothing new is
// deployed then. Job failures only resolve when the job succeeds.
func TestDeploymentAlertResolvesWhenServiceRecovers(t *testing.T) {
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
	if _, err := m.PutRule(ctx, Rule{Name: "deploys", Type: "deployment", Severity: "critical", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	recovered, recoveredErr := false, error(nil)
	m.Sources.ServiceRecovered = func(_ context.Context, id string) (bool, error) {
		if id != "svc_1" {
			t.Errorf("asked about %q", id)
		}
		return recovered, recoveredErr
	}
	if err := st.StartDeployment(ctx, store.Deployment{ID: "dep_1", ServiceID: "svc_1", ToRev: 1, Status: store.DeployInProgress, StartedAt: now, Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishDeployment(ctx, "dep_1", store.DeployFailed, "boom", now); err != nil {
		t.Fatal(err)
	}
	m.cursor = now.Add(-time.Second)
	eval := func(want int, when string) {
		t.Helper()
		clock = clock.Add(Interval)
		m.Evaluate(ctx)
		a, err := m.Active(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(a) != want {
			t.Fatalf("%s: %d active alerts, want %d: %+v", when, len(a), want, a)
		}
	}
	eval(1, "after the failure")
	eval(1, "while the service is still broken")
	recovered, recoveredErr = true, errors.New("database is busy")
	eval(1, "unknown is not recovered")
	recoveredErr = nil
	eval(0, "once the service is healthy again")
	ev, err := st.ListAlertEvents(ctx, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 || ev[0].Kind != "resolved" || !strings.Contains(ev[0].Message, "healthy again") {
		t.Fatalf("events: %+v", ev)
	}
}
