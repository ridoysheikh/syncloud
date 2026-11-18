package dbs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/ports"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestPublicPortsAssignedAndFreed(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := &Manager{st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now, PublicPortRange: [2]int{21000, 21004}}
	create := func(id, name string, n Network) {
		b, _ := json.Marshal(n)
		if err := st.CreateDatabase(ctx, store.Database{ID: id, Name: name, Engine: EngineValkey, Network: string(b), Secrets: []byte("x"), Spec: "{}", State: "{}", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	public := Network{Public: store.DatabasePublic{Enabled: true, Allow: []string{"0.0.0.0/0"}}}
	// "old" predates dedicated ports; "taken" already holds 21000-21001.
	create("db_old", "old", public)
	taken := public
	taken.Public.Port, taken.Public.ReadPort = 21000, 21001
	create("db_taken", "taken", taken)
	create("db_private", "private", Network{Public: store.DatabasePublic{Port: 21003}}) // a stale port
	if changed, err := m.assignPorts(ctx); err != nil || !changed {
		t.Fatalf("assign: %v %v", changed, err)
	}
	get := func(id string) store.DatabasePublic {
		d, err := st.DatabaseByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.ParseNetwork().Public
	}
	if p := get("db_old"); p.Port != 21002 || p.ReadPort != 21003 {
		t.Errorf("backfill: %+v", p)
	}
	if p := get("db_taken"); p.Port != 21000 || p.ReadPort != 21001 {
		t.Errorf("kept: %+v", p)
	}
	if p := get("db_private"); p.Port != 0 {
		t.Errorf("a private database keeps no port: %+v", p)
	}
	if changed, _ := m.assignPorts(ctx); changed {
		t.Error("a second pass changed something")
	}
	// The range is full: one port left for two.
	create("db_new", "new", public)
	if _, err := m.assignPorts(ctx); err != nil {
		t.Fatal(err)
	}
	if p := get("db_new"); p.Port != 21004 || p.ReadPort != 0 {
		t.Errorf("full range: %+v", p)
	}
	routes := m.PublicRoutes(ctx, "example.com")
	names := map[string]PublicRoute{}
	for _, r := range routes {
		names[r.Name] = r
	}
	if r := names["db-old-port"]; r.Entrypoint != "db-21002" || r.Host != "" || r.Plain {
		t.Errorf("TLS route of the port: %+v", r)
	}
	if r := names["db-old-ro-port-plain"]; r.Entrypoint != "db-21003" || !r.Plain {
		t.Errorf("plain read-only route: %+v", r)
	}
	if hosts := m.PublicHosts(ctx, "example.com"); len(hosts) != 6 { // SNI names only
		t.Errorf("certificate hosts: %v", hosts)
	}
	// Require TLS: no plain routes; turning the endpoint off frees the ports.
	d, _ := st.DatabaseByID(ctx, "db_old")
	n := d.ParseNetwork()
	n.Public.RequireTLS = true
	b, _ := json.Marshal(n)
	_ = st.SetDatabaseNetwork(ctx, "db_old", string(b), time.Now())
	for _, r := range m.PublicRoutes(ctx, "") {
		if r.Plain && r.Name == "db-old-port-plain" {
			t.Error("plain route while TLS is required")
		}
		if r.Host != "" {
			t.Errorf("an SNI route without a base domain: %+v", r)
		}
	}
	n.Public.Enabled = false
	b, _ = json.Marshal(n)
	_ = st.SetDatabaseNetwork(ctx, "db_old", string(b), time.Now())
	if _, err := m.assignPorts(ctx); err != nil {
		t.Fatal(err)
	}
	if p := get("db_old"); p.Port != 0 || p.ReadPort != 0 {
		t.Errorf("not freed: %+v", p)
	}
	// The freed port goes to the database that was waiting for one.
	if p := get("db_new"); p.Port != 21004 || p.ReadPort != 21002 {
		t.Errorf("reuse: %+v", p)
	}
	if got := len(m.PublicPorts(ctx)); got != 4 {
		t.Errorf("open ports: %d", got)
	}
}

// Chosen database ports (Phase 26): kept when sent back, checked in the
// port table, refused in the automatic range, back to automatic on ask.
func TestChosenDatabasePorts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	led := ports.New(st, log)
	led.Ranges = [][2]int{{20000, 20999}, {21000, 21004}}
	m := &Manager{st: st, log: log, bus: events.NewBus(), now: time.Now, PublicPortRange: [2]int{21000, 21004}, Ports: led}
	public := Network{Public: store.DatabasePublic{Enabled: true, Allow: []string{"0.0.0.0/0"}}}
	for _, id := range []string{"db_a", "db_b"} {
		b, _ := json.Marshal(public)
		if err := st.CreateDatabase(ctx, store.Database{ID: id, Name: strings.TrimPrefix(id, "db_"), Engine: EngineValkey, Network: string(b), Secrets: []byte("x"), Spec: "{}", State: "{}", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.assignPorts(ctx); err != nil {
		t.Fatal(err)
	}
	get := func(id string) store.DatabasePublic {
		d, _ := st.DatabaseByID(ctx, id)
		return d.ParseNetwork().Public
	}
	set := func(id string, pub store.DatabasePublic) error {
		n := public
		n.Public.Port, n.Public.ReadPort, n.Public.PortMode, n.Public.ReadPortMode = pub.Port, pub.ReadPort, pub.PortMode, pub.ReadPortMode
		_, err := m.SetNetwork(ctx, id, n, nil, "test")
		return err
	}
	if a := get("db_a"); a.Port != 21000 || a.ReadPort != 21001 {
		t.Fatalf("automatic %+v", a)
	}
	// Sending the same numbers back changes nothing.
	if err := set("db_a", store.DatabasePublic{Port: 21000, ReadPort: 21001}); err != nil {
		t.Fatal(err)
	}
	if a := get("db_a"); a.PortMode == store.PortChosen {
		t.Errorf("unchanged ports became chosen: %+v", a)
	}
	// Leaving them out keeps them too (older clients).
	if err := set("db_a", store.DatabasePublic{}); err != nil {
		t.Fatal(err)
	}
	if a := get("db_a"); a.Port != 21000 || a.ReadPort != 21001 {
		t.Errorf("left-out ports changed: %+v", a)
	}
	if err := set("db_a", store.DatabasePublic{Port: 15432, ReadPort: 15433}); err != nil {
		t.Fatal(err)
	}
	if a := get("db_a"); a.Port != 15432 || a.PortMode != store.PortChosen || a.ReadPort != 15433 {
		t.Errorf("chosen %+v", a)
	}
	for name, pub := range map[string]store.DatabasePublic{
		"taken":          {Port: 15432},
		"automatic":      {Port: 21003},
		"reserved":       {Port: 6379},
		"the same twice": {Port: 16000, ReadPort: 16000},
	} {
		if err := set("db_b", pub); !errors.As(err, new(ErrInvalid)) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	claims, _ := st.ListPortClaims(ctx)
	if len(claims) != 4 { // two databases × two ports on the controller
		t.Errorf("claims %+v", claims)
	}
	// Back to automatic: the chosen port is given up.
	if err := set("db_a", store.DatabasePublic{PortMode: store.PortAuto, ReadPort: 15433}); err != nil {
		t.Fatal(err)
	}
	if a := get("db_a"); a.Port != 21000 || a.PortMode != "" || a.ReadPort != 15433 {
		t.Errorf("back to automatic %+v", a)
	}
}
