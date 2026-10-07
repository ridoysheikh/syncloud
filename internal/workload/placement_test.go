package workload

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncloud/internal/events"
	"syncloud/internal/nodes"
	"syncloud/internal/store"
)

func TestPlacementAllows(t *testing.T) {
	for _, c := range []struct {
		name string
		p    Placement
		node string
		want bool
	}{
		{"no limits", Placement{}, "w1", true},
		{"service list", Placement{Nodes: []string{"w1", "w2"}}, "w2", true},
		{"outside service list", Placement{Nodes: []string{"w1"}}, "w2", false},
		{"project list", Spec{}.WithProjectNodes([]string{"w1"}).Placement, "w1", true},
		{"outside project list", Spec{}.WithProjectNodes([]string{"w1"}).Placement, "w3", false},
		{"both lists", Spec{Placement: Placement{Nodes: []string{"w1", "w2"}}}.WithProjectNodes([]string{"w2", "w3"}).Placement, "w2", true},
		{"service yes, project no", Spec{Placement: Placement{Nodes: []string{"w1", "w2"}}}.WithProjectNodes([]string{"w2", "w3"}).Placement, "w1", false},
		{"pinned", Placement{Node: "w1"}, "w2", false},
	} {
		if got := c.p.Allows(c.node); got != c.want {
			t.Errorf("%s: Allows(%s) = %v, want %v", c.name, c.node, got, c.want)
		}
	}
}

func TestTakesTasks(t *testing.T) {
	named := Placement{Nodes: []string{"ctl-0"}}
	for _, c := range []struct {
		name                  string
		node                  string
		schedulable, draining bool
		p                     Placement
		platform, want        bool
	}{
		{"schedulable worker", "w1", true, false, Placement{}, false, true},
		{"cordoned worker", "w1", false, false, Placement{Nodes: []string{"w1"}}, false, false},
		{"controller, general task", "ctl-0", false, false, Placement{}, false, false},
		{"controller, platform task", "ctl-0", false, false, Placement{}, true, true},
		{"controller named by the service", "ctl-0", false, false, named, false, true},
		{"controller named by the project", "ctl-0", false, false, Spec{}.WithProjectNodes([]string{"ctl-0"}).Placement, false, true},
		{"controller named but draining", "ctl-0", false, true, named, false, false},
	} {
		if got := takesTasks(c.node, c.schedulable, c.draining, c.p, c.platform); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProjectNodesLimitServices(t *testing.T) {
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
	if err := st.SetProjectNodes(ctx, "prj_1", []string{"ctl-0", "w1"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.ProjectByName(ctx, "shop"); strings.Join(p.Nodes, ",") != "ctl-0,w1" {
		t.Fatalf("project nodes %v", p.Nodes)
	}
	// Narrowing is fine; naming a node outside the project's list is not.
	v, _, err := m.Apply(ctx, env, "web", Spec{Image: "nginx:1.27", Placement: Placement{Nodes: []string{"w1", "w1"}}}, 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := m.SpecFor(ctx, v.ID, v.Revision)
	if strings.Join(spec.Placement.Nodes, ",") != "w1" {
		t.Errorf("nodes not normalized: %v", spec.Placement.Nodes)
	}
	_, _, err = m.Apply(ctx, env, "api", Spec{Image: "nginx:1.27", Placement: Placement{Nodes: []string{"w2"}}}, 1, "test")
	var inv ErrInvalid
	if !errors.As(err, &inv) || !strings.Contains(err.Error(), "w2 is not allowed") {
		t.Errorf("node outside the project accepted: %v", err)
	}
	// The project's list is applied at placement and never stored in revisions.
	if c := spec.WithProjectNodes([]string{"w1"}).Canonical(); c != spec.Canonical() {
		t.Errorf("project nodes leak into the stored spec: %s", c)
	}
	if err := (&Spec{Image: "x", Placement: Placement{Nodes: []string{"Bad Name"}}}).Normalize(); err == nil {
		t.Error("invalid node name accepted")
	}
}
