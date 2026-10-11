package workload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/imageref"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// A tag that moves: revisions pin the digest it points to; config changes
// keep the pin, redeploys and image auto-deploys take the newest push.
func TestImagePinningAndAutoDeploy(t *testing.T) {
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
	if err := st.SetEnvironmentPolicy(ctx, env.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	registry := map[string]string{"acme/app:latest": "sha256:aaa"}
	var unreachable bool
	m.ResolveDigest = func(_ context.Context, image string) (string, error) {
		if unreachable {
			return "", errors.New("dial tcp: no route to host")
		}
		if d, ok := registry[image]; ok {
			return d, nil
		}
		return "", fmt.Errorf("%w: %s", imageref.ErrNotFound, image)
	}
	current := func() (store.Service, Spec) {
		t.Helper()
		sv, err := st.ServiceByName(ctx, env.ID, "app")
		if err != nil {
			t.Fatal(err)
		}
		s, err := m.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			t.Fatal(err)
		}
		return sv, s
	}

	if _, _, err := m.Apply(ctx, env, "app", Spec{Image: "acme/app:latest"}, 1, "u"); err != nil {
		t.Fatal(err)
	}
	sv, s := current()
	if s.ImageDigest != "sha256:aaa" || s.ImageRef() != "acme/app:latest@sha256:aaa" {
		t.Fatalf("not pinned: %+v", s)
	}
	if ts := TaskSpec(sv, s, store.Task{ID: "t_1"}); ts.Image != "acme/app:latest@sha256:aaa" {
		t.Errorf("task runs %s", ts.Image)
	}

	// A push of the same tag; a config change does not take it.
	registry["acme/app:latest"] = "sha256:bbb"
	s.Env = map[string]string{"A": "1"}
	if _, _, err := m.Apply(ctx, env, "app", s, -1, "u"); err != nil {
		t.Fatal(err)
	}
	if _, s := current(); s.ImageDigest != "sha256:aaa" {
		t.Errorf("a config change upgraded the image: %s", s.ImageDigest)
	}
	// A redeploy does.
	if _, err := m.Redeploy(ctx, sv.ID, "u", false); err != nil {
		t.Fatal(err)
	}
	if _, s := current(); s.ImageDigest != "sha256:bbb" {
		t.Errorf("redeploy kept %s", s.ImageDigest)
	}

	// Auto-deploy: nothing new, then a push.
	up, err := m.DeployNewImage(ctx, sv.ID, "image-watch")
	if err != nil || up.Deployed {
		t.Fatalf("no new push: %+v %v", up, err)
	}
	registry["acme/app:latest"] = "sha256:ccc"
	if up, err := m.DeployNewImage(ctx, sv.ID, "image-watch"); err != nil || !up.Deployed || up.Digest != "sha256:ccc" {
		t.Fatalf("push: %+v %v", up, err)
	}
	if _, s := current(); s.ImageDigest != "sha256:ccc" {
		t.Errorf("auto-deploy kept %s", s.ImageDigest)
	}
	ds, _ := st.ListDeployments(ctx, sv.ID, 1, "")
	if len(ds) == 0 || ds[0].Trigger != store.TriggerImage {
		t.Errorf("deployment trigger: %+v", ds)
	}

	// The environment's policy applies.
	if err := st.SetEnvironmentPolicy(ctx, env.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	registry["acme/app:latest"] = "sha256:ddd"
	if _, err := m.DeployNewImage(ctx, sv.ID, "image-watch"); err == nil {
		t.Error("deployed to an environment that deploys by hand")
	}

	// A registry that is down never blocks a deploy; a missing tag does.
	unreachable = true
	s.Env = map[string]string{"A": "2"}
	if _, _, err := m.Apply(ctx, env, "app", s, -1, "u"); err != nil {
		t.Errorf("registry down blocked a config change: %v", err)
	}
	unreachable = false
	if _, _, err := m.Apply(ctx, env, "app", Spec{Image: "acme/typo:latest"}, -1, "u"); err == nil {
		t.Error("an image that does not exist was accepted")
	}
	// An image that names its digest is used as it is.
	if _, _, err := m.Apply(ctx, env, "app", Spec{Image: "acme/app@sha256:fff"}, -1, "u"); err != nil {
		t.Fatal(err)
	}
	if _, s := current(); s.ImageDigest != "" || s.ImageRef() != "acme/app@sha256:fff" {
		t.Errorf("digest reference: %+v", s)
	}
	if sv, _ := current(); m.Watchable(ctx, sv) == nil {
		t.Error("a digest reference is watchable")
	}
}
