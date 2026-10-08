package nodes

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// A controller restart must not make nodes Not ready (and reschedule their
// tasks) before agents had time to reconnect: the stored last-seen time is
// only written on status changes, so it is usually old.
func TestRestartGivesAgentsTimeToReconnect(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := time.Now().Add(-time.Hour).Unix()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO nodes (id, name, status, cert_serial, created_at, status_at, last_seen_at) VALUES ('node_a', 'w1', 'ready', 's', ?, ?, ?)`, old, old, old); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(st, events.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Now()
	r.now = func() time.Time { return now }
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	r.checkTimeouts(ctx)
	if v, _ := r.Get("node_a"); v.Status != store.NodeReady {
		t.Fatalf("right after a restart: %s", v.Status)
	}
	now = now.Add(SuspectAfter + time.Second)
	r.checkTimeouts(ctx)
	if v, _ := r.Get("node_a"); v.Status != store.NodeSuspect {
		t.Fatalf("after %s: %s", SuspectAfter, v.Status)
	}
	now = now.Add(NotReadyAfter)
	r.checkTimeouts(ctx)
	if v, _ := r.Get("node_a"); v.Status != store.NodeNotReady {
		t.Fatalf("after %s: %s", NotReadyAfter, v.Status)
	}
}
