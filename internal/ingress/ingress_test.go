package ingress

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
)

func setup(t *testing.T) (*Manager, *store.Store, map[string][]string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	env := store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}
	if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, env); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, store.Service{ID: "svc_1", EnvironmentID: "env_1", Name: "web", Revision: 1, DesiredCount: 1, CreatedAt: now, UpdatedAt: now}, `{"image":"nginx"}`, "test"); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"shop.example.com", "www.shop.example.com"} {
		if err := st.AddDomain(ctx, store.Domain{ID: "dom_" + h[:3] + h[len(h)-5:len(h)-4], ServiceID: "svc_1", Host: h, PortName: "http", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	dns := map[string][]string{"shop.example.com": {"203.0.113.1"}, "www.shop.example.com": {"203.0.113.1"}}
	m := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.ControllerAddr = func(context.Context) string { return "203.0.113.1" }
	m.NodeAddrs = func(context.Context) (map[string]string, error) {
		return map[string]string{"lb1": "198.51.100.1", "lb2": "198.51.100.2", "lb3": "198.51.100.3"}, nil
	}
	m.Resolve = func(_ context.Context, host string) ([]string, error) { return dns[host], nil }
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return m, st, dns
}

func TestMoveToNodesWaitsForDNS(t *testing.T) {
	ctx := context.Background()
	m, _, dns := setup(t)
	if got := m.Current("shop"); !got.Default || len(got.Nodes) > 0 {
		t.Fatalf("default ingress %+v", got)
	}
	if b := m.Base("shop", "203-0-113-1.sslip.io"); b != "203-0-113-1.sslip.io" {
		t.Errorf("default base %s", b)
	}
	if _, err := m.Set(ctx, "prj_1", []string{"lb1", "lb2"}); err != nil {
		t.Fatal(err)
	}
	if b := m.Base("shop", "203-0-113-1.sslip.io"); b != "198-51-100-1.sslip.io" {
		t.Errorf("generated base %s, want the first node's", b)
	}
	if pb, ok := m.PreviousBase("shop", "203-0-113-1.sslip.io"); !ok || pb != "203-0-113-1.sslip.io" {
		t.Errorf("previous base %s %v", pb, ok)
	}
	if cur := m.Current("shop"); cur.Default || !slices.Equal(cur.Nodes, []string{"lb1", "lb2"}) {
		t.Errorf("current %+v", cur)
	}
	// Both serve the domains until DNS moves.
	d := m.Domain("shop", "shop.example.com")
	if !d.Default || !d.Served("lb2", false) || !d.Served("ctl", true) {
		t.Errorf("domain target during the move %+v", d)
	}
	if got := m.Expected(ctx, "shop"); !slices.Equal(got, []string{"198.51.100.1", "198.51.100.2"}) {
		t.Errorf("expected %v", got)
	}
	// Half-moved (one record still on the controller): not moved.
	dns["shop.example.com"] = []string{"198.51.100.1", "203.0.113.1"}
	if m.Check(ctx) {
		t.Error("a domain still pointing at the controller moved")
	}
	dns["shop.example.com"] = []string{"198.51.100.1", "198.51.100.2"}
	if !m.Check(ctx) {
		t.Fatal("no change after DNS moved")
	}
	_ = m.Load(ctx)
	if d := m.Domain("shop", "shop.example.com"); d.Default {
		t.Errorf("the controller still serves a moved domain: %+v", d)
	}
	if d := m.Domain("shop", "www.shop.example.com"); !d.Default {
		t.Errorf("the controller stopped serving a domain whose DNS has not moved: %+v", d)
	}
	if p := m.Pending(ctx, "shop"); !slices.Equal(p, []string{"www.shop.example.com"}) {
		t.Errorf("pending %v", p)
	}
	dns["www.shop.example.com"] = []string{"198.51.100.2"} // a subset of the nodes is enough
	m.Check(ctx)
	_ = m.Load(ctx)
	if m.Move("shop") != nil {
		t.Error("the move did not end once every domain moved")
	}
	if a := m.Assigned(); !slices.Equal(a["lb1"], []string{"shop"}) || len(a) != 2 {
		t.Errorf("assigned %v", a)
	}
}

func TestMoveBackCutOverAndNodeRemoval(t *testing.T) {
	ctx := context.Background()
	m, _, _ := setup(t)
	if _, err := m.Set(ctx, "prj_1", []string{"lb1"}); err != nil {
		t.Fatal(err)
	}
	// Going back to the controller cancels the move.
	if _, err := m.Set(ctx, "prj_1", nil); err != nil {
		t.Fatal(err)
	}
	if m.Move("shop") != nil || len(m.Nodes("shop")) != 0 {
		t.Errorf("not back on the controller: %v %+v", m.Nodes("shop"), m.Move("shop"))
	}
	if _, err := m.Set(ctx, "prj_1", nil); err != ErrNoChange {
		t.Errorf("same ingress again: %v", err)
	}
	// Moving again mid-move: the controller and lb1 both keep serving.
	_, _ = m.Set(ctx, "prj_1", []string{"lb1"})
	if _, err := m.Set(ctx, "prj_1", []string{"lb2"}); err != nil {
		t.Fatal(err)
	}
	d := m.Domain("shop", "shop.example.com")
	if !d.Default || !d.Served("lb1", false) || !d.Served("lb2", false) {
		t.Errorf("after a second move %+v", d)
	}
	if _, err := m.CutOver(ctx, "prj_1"); err != nil {
		t.Fatal(err)
	}
	if d := m.Domain("shop", "shop.example.com"); d.Default || d.Served("lb1", false) {
		t.Errorf("after the cut-over %+v", d)
	}
	if all, last := m.Using("lb2"); !slices.Equal(all, []string{"shop"}) || !slices.Equal(last, []string{"shop"}) {
		t.Errorf("using lb2: %v %v", all, last)
	}
	if err := m.RemoveNode(ctx, "lb2"); err != nil {
		t.Fatal(err)
	}
	if n := m.Nodes("shop"); len(n) != 0 || !m.Current("shop").Default {
		t.Errorf("removing its only node leaves %v", n)
	}
}

func TestSwapBase(t *testing.T) {
	if h := SwapBase("web-production-shop.198-51-100-1.sslip.io", "198-51-100-1.sslip.io", "203-0-113-1.sslip.io"); h != "web-production-shop.203-0-113-1.sslip.io" {
		t.Errorf("swap %s", h)
	}
	if h := SwapBase("shop.example.com", "198-51-100-1.sslip.io", "x"); h != "" {
		t.Errorf("swap of a foreign host %s", h)
	}
}
