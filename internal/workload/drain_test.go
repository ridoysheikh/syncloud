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

func TestDrainTime(t *testing.T) {
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
	zero := 0
	for _, c := range []struct {
		name string
		spec Spec
		want time.Duration
	}{
		{"web", Spec{Image: "nginx:1.27", Ports: []Port{{Container: 80}}}, DefaultDrain},
		{"fast", Spec{Image: "nginx:1.27", Ports: []Port{{Container: 80}}, Deployment: Deployment{DrainSeconds: &zero}}, 0},
		{"worker", Spec{Image: "busybox:1.37"}, 0}, // nothing routes to it
	} {
		v, _, err := m.Apply(ctx, env, c.name, c.spec, 0, "test")
		if err != nil {
			t.Fatal(err)
		}
		if got := m.drainTime(ctx, store.Task{ServiceID: v.ID, Revision: v.Revision}); got != c.want {
			t.Errorf("%s: drain %s, want %s", c.name, got, c.want)
		}
	}
	bad := 301
	if err := (&Spec{Image: "nginx", Deployment: Deployment{DrainSeconds: &bad}}).Normalize(); err == nil {
		t.Error("drainSeconds 301 accepted")
	}
}
