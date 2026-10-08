package workload

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestSharedEnvRollsOutAsRevisions(t *testing.T) {
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
	env := store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}
	if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, env); err != nil {
		t.Fatal(err)
	}
	apply := func(name string, spec Spec) ServiceView {
		t.Helper()
		v, _, err := m.Apply(ctx, env, name, spec, 1, "test")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	web := apply("web", Spec{Image: "nginx:1.27", Env: map[string]string{"LOG_LEVEL": "debug"}})
	apply("worker", Spec{Image: AwaitingBuild})
	if web.Spec.SharedEnv != nil {
		t.Fatalf("shared env without variables: %v", web.Spec.SharedEnv)
	}

	redeployed, err := m.SetSharedEnv(ctx, env, map[string]string{"DATABASE_URL": "postgres://db/shop", "LOG_LEVEL": "info"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(redeployed)
	if !slices.Equal(redeployed, []string{"web", "worker"}) {
		t.Fatalf("redeployed %v", redeployed)
	}
	sv, _ := st.ServiceByName(ctx, env.ID, "web")
	if sv.Revision != 2 {
		t.Fatalf("web revision %d, want 2", sv.Revision)
	}
	spec, _ := m.SpecFor(ctx, sv.ID, sv.Revision)
	ts := TaskSpec(sv, spec, store.Task{ID: "task_1", Revision: 2})
	if ts.Env["DATABASE_URL"] != "postgres://db/shop" || ts.Env["LOG_LEVEL"] != "debug" {
		t.Fatalf("task env %v: want the shared DATABASE_URL and the service's own LOG_LEVEL", ts.Env)
	}

	// Unchanged variables do not redeploy; a plain apply keeps the snapshot.
	if redeployed, _ := m.SetSharedEnv(ctx, env, map[string]string{"DATABASE_URL": "postgres://db/shop", "LOG_LEVEL": "info"}, "test"); len(redeployed) != 0 {
		t.Fatalf("unchanged variables redeployed %v", redeployed)
	}
	v := apply("web", Spec{Image: "nginx:1.28", Env: map[string]string{"LOG_LEVEL": "debug"}, SharedEnv: map[string]string{"EVIL": "1"}})
	if v.Spec.SharedEnv["DATABASE_URL"] == "" || v.Spec.SharedEnv["EVIL"] != "" {
		t.Fatalf("apply must take the environment's variables, got %v", v.Spec.SharedEnv)
	}

	if _, err := m.SetSharedEnv(ctx, env, map[string]string{"SYNCLOUD_X": "1"}, "test"); err == nil {
		t.Fatal("reserved prefix accepted")
	}
}
