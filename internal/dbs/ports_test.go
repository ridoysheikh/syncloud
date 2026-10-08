package dbs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

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
