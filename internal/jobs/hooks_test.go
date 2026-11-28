package jobs

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// A service built from Git has no image until its first build. The empty
// deployment it is created with succeeds at once, and must not run its
// release commands: there is nothing to run them in.
func TestHooksWaitForTheFirstBuild(t *testing.T) {
	for _, c := range []struct {
		name, image string
		runs        int
	}{
		{"waiting for the first build", workload.AwaitingBuild, 0},
		{"with an image", "busybox:1.37", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			bus := events.NewBus()
			reg := nodes.NewRegistry(st, bus, log)
			wl := workload.NewManager(st, nil, reg, nil, bus, log)
			m := NewManager(st, nil, wl, reg, bus, log)
			now := time.Now().UTC().Truncate(time.Second)
			env := store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}
			if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, env); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateService(ctx, store.Service{ID: "svc_1", EnvironmentID: env.ID, Name: "web", DesiredCount: 1, CreatedAt: now},
				`{"image":"`+c.image+`","resources":{"cpu":0.1,"memory":64}}`, "test"); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Apply(ctx, env, "web-post-1", Spec{Kind: KindPostDeploy, Service: "web", Command: []string{"true"}}); err != nil {
				t.Fatal(err)
			}
			dep := store.Deployment{ID: "dep_1", ServiceID: "svc_1", ToRev: 1, Status: store.DeployInProgress, StartedAt: now, Trigger: "manual"}
			if err := st.StartDeployment(ctx, dep); err != nil {
				t.Fatal(err)
			}
			if err := st.FinishDeployment(ctx, dep.ID, store.DeploySucceeded, "", now); err != nil {
				t.Fatal(err)
			}
			m.RunPostDeploy(ctx, store.Service{}, dep)
			runs, err := st.DeploymentRuns(ctx, dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != c.runs {
				t.Errorf("%d job runs, want %d: %+v", len(runs), c.runs, runs)
			}
		})
	}
}
