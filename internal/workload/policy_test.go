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

func TestDeployLockCloneAndDeleteEverything(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	m := NewManager(st, nil, nodes.NewRegistry(st, bus, log), nil, bus, log)
	now := time.Now().UTC().Truncate(time.Second)
	prod := store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}
	if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, prod); err != nil {
		t.Fatal(err)
	}
	v, _, err := m.Apply(ctx, prod, "web", Spec{Image: "app:1", Env: map[string]string{"A": "1"}}, 3, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Apply(ctx, prod, "web", Spec{Image: "app:2"}, -1, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetSharedEnv(ctx, prod, map[string]string{"REGION": "eu"}, "test"); err != nil {
		t.Fatal(err)
	}

	// A lock blocks deploys, not rollbacks or scaling.
	lock := &store.DeployLock{Reason: "release freeze", By: "ops@example.com", At: now}
	if err := st.SetEnvironmentPolicy(ctx, prod.ID, true, lock); err != nil {
		t.Fatal(err)
	}
	var locked ErrLocked
	if _, _, err := m.Apply(ctx, prod, "web", Spec{Image: "app:3"}, -1, "test"); !errors.As(err, &locked) || locked.Lock.Reason != "release freeze" {
		t.Fatalf("apply while locked: %v", err)
	}
	if _, _, err := m.Apply(ctx, prod, "new", Spec{Image: "app:1"}, 1, "test"); !errors.As(err, &locked) {
		t.Fatalf("create while locked: %v", err)
	}
	if _, err := m.Redeploy(ctx, v.ID, "test", false); !errors.As(err, &locked) {
		t.Fatalf("redeploy while locked: %v", err)
	}
	if _, err := m.SetSharedEnv(ctx, prod, map[string]string{"REGION": "us"}, "test"); !errors.As(err, &locked) {
		t.Fatalf("shared variables while locked: %v", err)
	}
	if e, _ := st.EnvironmentByID(ctx, prod.ID); e.SharedEnv["REGION"] != "eu" {
		t.Fatalf("locked shared variables changed: %v", e.SharedEnv)
	}
	if _, err := m.Rollback(ctx, v.ID, 1, "test", false); err != nil {
		t.Fatalf("rollback while locked: %v", err)
	}
	if _, err := m.Scale(ctx, v.ID, 1, "test"); err != nil {
		t.Fatalf("scale while locked: %v", err)
	}
	if err := st.SetEnvironmentPolicy(ctx, prod.ID, false, nil); err != nil {
		t.Fatal(err)
	}

	// Cloning copies the shared variables and services, at 0 tasks.
	src, _ := st.EnvironmentByID(ctx, prod.ID)
	res, err := m.CloneEnvironment(ctx, src, "staging", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	staging := res.Environment
	cv, _ := st.ServiceByName(ctx, staging.ID, "web")
	cspec, _ := m.SpecFor(ctx, cv.ID, cv.Revision)
	if len(res.Services) != 1 || cv.DesiredCount != 0 || cspec.Image != "app:1" || cspec.SharedEnv["REGION"] != "eu" ||
		!staging.AutoDeploy || staging.Lock != nil || res.ServiceIDs[v.ID] != cv.ID {
		t.Fatalf("clone: %+v, service %+v, spec %+v", res, cv, cspec)
	}
	if _, err := m.CloneEnvironment(ctx, src, "staging", false, "test"); !errors.As(err, new(ErrInvalid)) {
		t.Fatalf("cloned onto an existing name: %v", err)
	}

	// Deleting everything: services first, then the environment, then the
	// project; an empty environment goes at once.
	if _, err := m.DeleteEnvironment(ctx, staging, false); !errors.As(err, new(ErrInvalid)) {
		t.Fatalf("deleted a non-empty environment without force: %v", err)
	}
	p, _ := st.ProjectByName(ctx, "shop")
	pending, err := m.DeleteProject(ctx, p, true)
	if err != nil || !pending {
		t.Fatalf("delete project: pending %v, %v", pending, err)
	}
	for _, e := range []string{prod.ID, staging.ID} {
		svcs, _ := st.ListServicesIn(ctx, e)
		for _, sv := range svcs {
			if !sv.Deleting {
				t.Fatalf("service %s not deleting", sv.Name)
			}
			if _, _, err := m.Apply(ctx, store.Environment{ID: e}, "other", Spec{Image: "x"}, 1, "test"); !errors.As(err, new(ErrInvalid)) {
				t.Fatalf("created a service in a deleting environment: %v", err)
			}
			// What the reconciler does once a service's tasks are gone.
			if err := st.DeleteService(ctx, sv.ID); err != nil {
				t.Fatal(err)
			}
			m.finishDeletions(ctx, e)
		}
	}
	if _, err := st.ProjectByName(ctx, "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("project still there: %v", err)
	}
}
