package workload

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"syncloud/internal/events"
	"syncloud/internal/nodes"
	"syncloud/internal/store"
)

type fakeHooks struct {
	pre       bool
	started   []string // deployment IDs
	cancelled []string
}

func (f *fakeHooks) HasPreDeploy(context.Context, store.Service) bool { return f.pre }
func (f *fakeHooks) RunPreDeploy(_ context.Context, _ store.Service, _ int, depID string) {
	f.started = append(f.started, depID)
}
func (f *fakeHooks) RunPostDeploy(context.Context, store.Service, store.Deployment) {}
func (f *fakeHooks) CancelHooks(_ context.Context, depID string) {
	f.cancelled = append(f.cancelled, depID)
}

func TestDeploymentHistoryAndActions(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	m := NewManager(st, nil, nodes.NewRegistry(st, bus, log), nil, bus, log)
	hooks := &fakeHooks{}
	m.DeployHooks = hooks
	now := time.Now().UTC().Truncate(time.Second)
	env := store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}
	if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, env); err != nil {
		t.Fatal(err)
	}
	latest := func() store.Deployment {
		t.Helper()
		sv, _ := st.ServiceByName(ctx, env.ID, "web")
		ds, err := st.ListDeployments(ctx, sv.ID, 1, "")
		if err != nil || len(ds) == 0 {
			t.Fatalf("deployments: %v %v", ds, err)
		}
		return ds[0]
	}
	apply := func(spec Spec, actor string) ServiceView {
		t.Helper()
		v, _, err := m.Apply(ctx, env, "web", spec, 1, actor)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// First deployment: manual, by the user, nothing to compare with.
	v := apply(Spec{Image: "app:1", Env: map[string]string{"A": "1"}}, "usr_1")
	d := latest()
	if d.Trigger != store.TriggerManual || d.Actor != "usr_1" || d.Image != "app:1" || d.FromRev != 0 || len(DecodeChanges(d.Changes)) != 0 {
		t.Fatalf("first deployment %+v", d)
	}

	// A build deploys with its ID; the change summary names variables only.
	v = apply(Spec{Image: "app:2", Env: map[string]string{"A": "secret"}}, "build:bld_9")
	d = latest()
	ch := DecodeChanges(d.Changes)
	if d.Trigger != store.TriggerGit || d.BuildID != "bld_9" || d.Actor != "system" || len(ch) != 2 ||
		ch[0].Field != "image" || ch[1].Field != "variable A" || ch[1].To == "secret" {
		t.Fatalf("git deployment %+v changes %+v", d, ch)
	}
	ev, _ := st.DeploymentEvents(ctx, d.ID)
	if len(ev) != 1 || ev[0].Kind != "started" {
		t.Fatalf("timeline %+v", ev)
	}

	// Redeploy: a new revision with only the marker changed; applying the
	// same spec again (without the marker) changes nothing.
	v, err = m.Redeploy(ctx, v.ID, "usr_1", false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Revision != 3 || latest().Trigger != store.TriggerRedeploy {
		t.Fatalf("redeploy: revision %d, %+v", v.Revision, latest())
	}
	if ch := DecodeChanges(latest().Changes); len(ch) != 1 || ch[0].Field != "redeploy" {
		t.Fatalf("redeploy changes %+v", ch)
	}
	if v = apply(Spec{Image: "app:2", Env: map[string]string{"A": "secret"}}, "usr_1"); v.Revision != 3 {
		t.Fatalf("re-applying the same spec made revision %d", v.Revision)
	}

	// Rollback to revision 1: a new revision with its spec.
	v, err = m.Rollback(ctx, v.ID, 1, "usr_1", false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Revision != 4 || v.Spec.Image != "app:1" || latest().Trigger != store.TriggerRollback {
		t.Fatalf("rollback: %+v %+v", v, latest())
	}
	if _, err := m.Rollback(ctx, v.ID, 4, "usr_1", false); err == nil {
		t.Fatal("rolled back to the current revision")
	}

	// Cancel the running deployment 3 → 4: it returns to revision 3.
	running := latest()
	v, err = m.CancelDeployment(ctx, v.ID, running.ID, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.DeploymentByID(ctx, running.ID); got.Status != store.DeployCancelled {
		t.Fatalf("cancelled deployment is %s", got.Status)
	}
	if v.Revision != 5 || v.Spec.Image != "app:2" || latest().FromRev != 4 {
		t.Fatalf("after cancel: revision %d image %s, %+v", v.Revision, v.Spec.Image, latest())
	}
	if _, err := m.CancelDeployment(ctx, v.ID, running.ID, "usr_1"); !errors.As(err, new(ErrInvalid)) {
		t.Fatalf("cancelling a finished deployment: %v", err)
	}

	// With pre-deploy hooks, an apply waits for them; cancelling it keeps
	// the old revision and stops the hook runs.
	hooks.pre = true
	apply(Spec{Image: "app:3"}, "usr_1")
	waiting := latest()
	if waiting.Status != store.DeployWaitingHook || len(hooks.started) != 1 {
		t.Fatalf("hook deployment %+v, started %v", waiting, hooks.started)
	}
	v, err = m.CancelDeployment(ctx, v.ID, waiting.ID, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.DeploymentByID(ctx, waiting.ID); got.Status != store.DeployCancelled || v.Revision != 5 ||
		len(hooks.cancelled) != 1 || hooks.cancelled[0] != waiting.ID {
		t.Fatalf("cancel while waiting: %s, revision %d, cancelled hooks %v", got.Status, v.Revision, hooks.cancelled)
	}
	// Rollbacks skip the hooks unless asked.
	if _, err := m.Rollback(ctx, v.ID, 1, "usr_1", false); err != nil {
		t.Fatal(err)
	}
	if len(hooks.started) != 1 || latest().Status != store.DeployInProgress {
		t.Fatalf("rollback ran hooks: %v %+v", hooks.started, latest())
	}
	if _, err := m.Rollback(ctx, v.ID, 2, "usr_1", true); err != nil {
		t.Fatal(err)
	}
	if len(hooks.started) != 2 || latest().Status != store.DeployWaitingHook {
		t.Fatalf("rollback with hooks: %v %+v", hooks.started, latest())
	}

	// A revision whose image cleanup removed can't be rolled back to.
	m.ImageAvailable = func(_ context.Context, image string) (bool, error) { return image != "app:1", nil }
	if _, err := m.Rollback(ctx, v.ID, 1, "usr_1", false); !errors.As(err, new(ErrInvalid)) {
		t.Fatalf("rollback to a removed image: %v", err)
	}

	// The project listing finds them all, newest first, filtered by status.
	all, err := st.ListProjectDeployments(ctx, "prj_1", store.DeploymentFilter{}, 50, "")
	if err != nil || len(all) < 7 || all[0].Service != "web" || all[0].Environment != "production" {
		t.Fatalf("project deployments: %d %v", len(all), err)
	}
	cancelled, _ := st.ListProjectDeployments(ctx, "prj_1", store.DeploymentFilter{Status: store.DeployCancelled}, 50, "")
	if len(cancelled) != 2 {
		t.Fatalf("cancelled deployments: %d", len(cancelled))
	}
}
