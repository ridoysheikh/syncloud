package workload

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
)

// A service has recovered from a failed deployment only when the revision
// that deployment rolled out is fully serving, and has been for a while.
func TestRecovered(t *testing.T) {
	type task struct {
		rev    int
		age    time.Duration
		state  string
		health string
	}
	up := func(rev int) task { return task{rev, time.Hour, store.TaskRunning, ""} }
	for _, c := range []struct {
		name    string
		desired int
		health  bool   // the spec has a health check
		failed  int    // the revision the last deployment rolled out
		status  string // how it ended
		tasks   []task
		want    bool
	}{
		{"all serving", 2, false, 1, store.DeployFailed, []task{up(1), up(1)}, true},
		{"one missing", 2, false, 1, store.DeployFailed, []task{up(1)}, false},
		// A crashing task runs for a moment before it dies.
		{"only just started", 1, false, 1, store.DeployFailed, []task{{1, 10 * time.Second, store.TaskRunning, ""}}, false},
		{"not running", 1, false, 1, store.DeployFailed, []task{{1, time.Hour, store.TaskStarting, ""}}, false},
		{"health check passing", 1, true, 1, store.DeployFailed, []task{{1, time.Hour, store.TaskRunning, "healthy"}}, true},
		{"health check not passing", 1, true, 1, store.DeployFailed, []task{{1, time.Hour, store.TaskRunning, "starting"}}, false},
		// A failed pre-deploy job: the old revision still runs, and the new
		// one never shipped.
		{"the failed revision never became current", 1, false, 2, store.DeployFailed, []task{up(1)}, false},
		{"deploying", 1, false, 1, store.DeployInProgress, []task{up(1)}, false},
		{"stopped", 0, false, 1, store.DeployFailed, nil, false},
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
			m := NewManager(st, nil, nodes.NewRegistry(st, bus, log), nil, bus, log)
			now := time.Now().UTC().Truncate(time.Second)
			if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			spec := `{"image":"nginx:1.27"}`
			if c.health {
				spec = `{"image":"nginx:1.27","health":{"type":"tcp"}}`
			}
			if err := st.CreateService(ctx, store.Service{ID: "svc_1", EnvironmentID: "env_1", Name: "web", DesiredCount: c.desired, CreatedAt: now}, spec, "test"); err != nil {
				t.Fatal(err)
			}
			if err := st.StartDeployment(ctx, store.Deployment{ID: "dep_1", ServiceID: "svc_1", ToRev: c.failed, Status: store.DeployInProgress, StartedAt: now, Trigger: "manual"}); err != nil {
				t.Fatal(err)
			}
			if c.status != store.DeployInProgress {
				if err := st.FinishDeployment(ctx, "dep_1", c.status, "boom", now); err != nil {
					t.Fatal(err)
				}
			}
			for i, x := range c.tasks {
				at := now.Add(-x.age)
				tk := store.Task{ID: TaskIDPrefix + string(rune('a'+i)), ServiceID: "svc_1", Revision: x.rev,
					Desired: "running", State: store.TaskPending, CreatedAt: at, UpdatedAt: at}
				if err := st.CreateTask(ctx, tk); err != nil {
					t.Fatal(err)
				}
				tk.State, tk.Health = x.state, x.health
				if err := st.UpdateTaskStatus(ctx, tk); err != nil {
					t.Fatal(err)
				}
			}
			got, err := m.Recovered(ctx, "svc_1")
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("recovered = %v, want %v", got, c.want)
			}
		})
	}
}
