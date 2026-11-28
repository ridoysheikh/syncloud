package dbs

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/agentgw"
	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// A member that was sent to its node and then stopped must be sent again: the
// agent starts the existing container, or reports that it is running when the
// controller's view is out of date. Every send touches updated_at, which is
// what these tests watch (no agent is connected).
var recoverCases = []struct {
	name      string
	state     string
	age       time.Duration
	connected bool
	resent    bool
}{
	{"exited", store.TaskExited, time.Minute, true, true},
	{"failed", store.TaskFailed, time.Minute, true, true},
	{"pending", store.TaskPending, time.Minute, true, true},
	{"just exited", store.TaskExited, 5 * time.Second, true, false}, // not in a tight loop
	// Without the node, the spec lacks its resolver: its hash would be wrong.
	{"exited, node away", store.TaskExited, time.Minute, false, false},
	{"running", store.TaskRunning, time.Minute, true, false},
	{"starting", store.TaskStarting, time.Minute, true, false},
	{"pulling", store.TaskPulling, time.Minute, true, false},
}

func testManager(t *testing.T, connected bool) (*Manager, time.Time) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	reg := nodes.NewRegistry(st, bus, log)
	reg.Added(store.Node{ID: "node_1", Name: "ctl-0", Status: store.NodeReady, StatusAt: time.Now()})
	if connected {
		reg.Connected(context.Background(), "node_1", nodes.Info{})
	}
	m := New(st, agentgw.New(st, reg, log), nil, reg, box, bus, log)
	now := time.Now().UTC().Truncate(time.Second)
	m.now = func() time.Time { return now }
	return m, now
}

func TestEtcdMemberIsRestarted(t *testing.T) {
	for _, c := range recoverCases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			m, now := testManager(t, c.connected)
			then := now.Add(-c.age)
			mb := store.EtcdMember{ID: EtcdPrefix + "1", NodeID: "node_1", State: store.TaskPending,
				Cluster: "e0=" + etcdPeerURL(0), NewCluster: true, CreatedAt: then}
			if err := m.st.CreateEtcdMember(ctx, mb); err != nil {
				t.Fatal(err)
			}
			mb.State, mb.SpecHash = c.state, specHash(m.etcdSpec(mb))
			if err := m.st.UpdateEtcdMember(ctx, mb, then); err != nil {
				t.Fatal(err)
			}
			m.reconcileEtcd(ctx)
			got, err := m.st.EtcdMemberByID(ctx, mb.ID)
			if err != nil {
				t.Fatal(err)
			}
			if resent := got.UpdatedAt.Equal(now); resent != c.resent {
				t.Errorf("resent = %v, want %v", resent, c.resent)
			}
		})
	}
}

func TestDatabaseMemberIsRestarted(t *testing.T) {
	for _, c := range recoverCases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			m, now := testManager(t, c.connected)
			v, err := m.Create(ctx, CreateRequest{Name: "cache", Engine: EngineValkey, Actor: "test"})
			if err != nil {
				t.Fatal(err)
			}
			d, err := m.st.DatabaseByID(ctx, v.ID)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := parseSpec(d.Spec)
			if err != nil {
				t.Fatal(err)
			}
			sec, err := m.secrets(d)
			if err != nil {
				t.Fatal(err)
			}
			then := now.Add(-c.age)
			mb := store.DatabaseMember{ID: MemberPrefix + "1", DatabaseID: d.ID, Kind: KindData, NodeID: "node_1",
				Desired: "running", State: store.TaskPending, CreatedAt: then}
			if err := m.st.CreateDatabaseMember(ctx, mb); err != nil {
				t.Fatal(err)
			}
			mb.State, mb.SpecHash = c.state, specHash(m.taskSpec(d, spec, parseState(d.State), sec, mb))
			if err := m.st.UpdateDatabaseMember(ctx, mb, then); err != nil {
				t.Fatal(err)
			}
			m.reconcile(ctx, d.ID)
			got, err := m.st.DatabaseMemberByID(ctx, mb.ID)
			if err != nil {
				t.Fatal(err)
			}
			if resent := got.UpdatedAt.Equal(now); resent != c.resent {
				t.Errorf("resent = %v, want %v", resent, c.resent)
			}
		})
	}
}
