package lbprotect

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/dbs"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func setup(t *testing.T) (*Manager, store.Project, *dbs.ValkeyTarget) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := store.Project{ID: "prj_ab12", Name: "shop", CreatedAt: now}
	if err := st.CreateProject(ctx, p, store.Environment{ID: "env_1", ProjectID: p.ID, Name: "production", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	m := New(st, box, slog.New(slog.NewTextHandler(io.Discard, nil)))
	vk := &dbs.ValkeyTarget{ID: "db_1", Name: "shop-ratelimit", Project: "shop", Endpoint: "10.91.1.5:6379", Password: "pw", Health: "healthy"}
	m.Valkey = func(context.Context, string) (dbs.ValkeyTarget, error) { return *vk, nil }
	m.Assigned = func() map[string][]string { return map[string][]string{"lb1": {"shop"}, "lb2": {"shop", "blog"}} }
	return m, p, vk
}

func TestProtectionRendersAndFallsBack(t *testing.T) {
	ctx := context.Background()
	m, p, vk := setup(t)
	if m.Protection("shop") != nil || len(m.Middlewares()) != 0 {
		t.Fatal("protection before any is saved")
	}
	s := Default()
	s.Enabled, s.MaxConcurrent, s.MaxBodyMB = true, 20, 8
	s.ClientIP = ClientIP{Source: "header", Header: "CF-Connecting-IP"}
	s.Block = []string{"203.0.113.9", "198.51.100.0/24"}
	s.Bypass.IPs, s.Bypass.Paths = []string{"192.0.2.1"}, []string{"/healthz"}
	s.Redis.DatabaseID = "db_1"
	s.Guard = Guard{Rate: 50, MaxConnections: 100, BanMinutes: 15}
	out, err := m.Put(ctx, p, s, SecretUpdate{Set: "abcdefghijklmnop1234"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Bypass.HasSecret || out.Block[0] != "203.0.113.9/32" || out.Guard.Burst != 100 {
		t.Errorf("saved %+v", out)
	}
	mws := m.Middlewares()
	rl := mws["lb-ab12-rate"].RateLimit
	if rl == nil || rl.Average != 100 || rl.Redis == nil || rl.Redis.Endpoints[0] != "10.91.1.5:6379" || rl.SourceCriterion.RequestHeaderName != "CF-Connecting-IP" {
		t.Fatalf("rate limit %+v", rl)
	}
	if mws["lb-ab12-inflight"].InFlightReq.Amount != 20 || mws["lb-ab12-body"].Buffering.MaxRequestBodyBytes != 8<<20 {
		t.Errorf("middlewares %+v", mws)
	}
	pr := m.Protection("shop")
	if pr == nil || len(pr.Limits) != 2 || pr.Always[0] != "lb-ab12-body" || pr.BypassSecret != "abcdefghijklmnop1234" || len(pr.Deny) != 2 {
		t.Errorf("protection %+v", pr)
	}
	// The Valkey goes down: per-node counters, and an alert.
	vk.Health = "down"
	m.refreshRedis(ctx)
	if m.Middlewares()["lb-ab12-rate"].RateLimit.Redis != nil || !m.Redis("shop").Fallback {
		t.Error("still using a Valkey that is down")
	}
	if c := m.Components(); len(c) != 1 || c[0].State != "down" {
		t.Errorf("components %+v", c)
	}
	// The secret survives a reload, sealed in the store.
	m2 := New(m.st, m.box, m.log)
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m2.Protection("shop"); got == nil || got.BypassSecret != "abcdefghijklmnop1234" {
		t.Errorf("after reload %+v", got)
	}
	// Off keeps the settings, applies nothing.
	s.Enabled = false
	if _, err := m.Put(ctx, p, s, SecretUpdate{}); err != nil {
		t.Fatal(err)
	}
	if m.Protection("shop") != nil || len(m.Middlewares()) != 0 || m.Get("shop").Guard.Rate != 50 {
		t.Error("protection still applied while off")
	}
}

func TestGuardsMergePermissively(t *testing.T) {
	ctx := context.Background()
	m, p, _ := setup(t)
	s := Default()
	s.Enabled = true
	s.Guard = Guard{Rate: 50, Burst: 60, MaxConnections: 100, BanMinutes: 15}
	if _, err := m.Put(ctx, p, s, SecretUpdate{}); err != nil {
		t.Fatal(err)
	}
	g := m.Guards("lb1")
	if len(g) != 1 || g[0].GetRate() != 50 || g[0].GetMaxConnections() != 100 || g[0].GetPorts() != "80,443" {
		t.Errorf("lb1 guards %+v", g)
	}
	if err := ValidFirewallGuard(g[0]); err != nil {
		t.Error(err)
	}
	// lb2 also serves blog, which has no guard: none there.
	if g := m.Guards("lb2"); len(g) != 0 {
		t.Errorf("lb2 guards %+v", g)
	}
	if d, _, ok := m.AutoBan("lb1", 443); !ok || d != 15*time.Minute {
		t.Errorf("auto-ban %v %v", d, ok)
	}
	if _, _, ok := m.AutoBan("lb1", 22); ok {
		t.Error("auto-ban on SSH from the load-balancer guard")
	}
}

func TestSpecValidation(t *testing.T) {
	bad := []func(*Spec){
		func(s *Spec) { s.RateLimit.Period = "2s" },
		func(s *Spec) { s.ClientIP = ClientIP{Source: "forwarded"} },
		func(s *Spec) { s.ClientIP = ClientIP{Source: "header", Header: "bad header"} },
		func(s *Spec) { s.Block = []string{"nope"} },
		func(s *Spec) { s.Bypass.Paths = []string{"healthz"} },
		func(s *Spec) { s.Bypass.Header = "X Bad" },
		func(s *Spec) { s.Guard = Guard{BanMinutes: 5} },
	}
	for i, f := range bad {
		s := Default()
		f(&s)
		if s.Validate() == nil {
			t.Errorf("case %d accepted: %+v", i, s)
		}
	}
}
