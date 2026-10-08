package workload

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestRoutingAndDomains(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	m := NewManager(st, nil, nodes.NewRegistry(st, bus, log), nil, bus, log)
	m.PublicPortRange = [2]int{20000, 20001}
	publicChanges := 0
	m.OnPublicPorts = func() { publicChanges++ }
	now := time.Now().UTC().Truncate(time.Second)
	env := store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}
	if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, env); err != nil {
		t.Fatal(err)
	}
	apply := func(name string, ports []Port) ServiceView {
		t.Helper()
		v, _, err := m.Apply(ctx, env, name, Spec{Image: "app:1", Ports: ports}, 1, "test")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	web := apply("web", []Port{{Name: "http", Container: 8080}, {Name: "admin", Container: 9000}, {Name: "game", Container: 7777, Protocol: "udp"}})
	db := apply("db", []Port{{Name: "pg", Container: 5432, Protocol: "tcp"}})
	other := apply("other", []Port{{Name: "pg", Container: 5432, Protocol: "tcp"}})
	apply("api", []Port{{Name: "http", Container: 80}})
	const base = "example.com"
	hosts := func() []string {
		var out []string
		m.routesDirty()
		for _, r := range m.Routes(ctx, base) {
			out = append(out, r.Host+r.Path)
		}
		slices.Sort(out)
		return out
	}
	if got := hosts(); !slices.Equal(got, []string{"api-production-shop.example.com", "web-admin-production-shop.example.com", "web-production-shop.example.com"}) {
		t.Fatalf("default hosts %v", got)
	}

	// A label replaces the generated name; turning it off removes the route.
	str := func(s string) *string { return &s }
	off, on := false, true
	if _, err := m.SetRouting(ctx, web.ID, map[string]RoutingInput{"http": {Label: str("Shop")}, "admin": {Generated: &off}}, base); err != nil {
		t.Fatal(err)
	}
	if got := hosts(); !slices.Equal(got, []string{"api-production-shop.example.com", "shop.example.com"}) {
		t.Fatalf("hosts after label %v", got)
	}
	for _, bad := range []map[string]RoutingInput{
		{"http": {Label: str("registry")}},            // reserved
		{"http": {Label: str("api-production-shop")}}, // another service's address
		{"http": {Label: str("-x")}},                  // invalid
		{"http": {Public: &on}},                       // http is not public
		{"game": {Label: str("x")}},                   // labels are for http
		{"nope": {Public: &on}},                       // no such port
	} {
		if _, err := m.SetRouting(ctx, web.ID, bad, base); !errors.As(err, new(ErrInvalid)) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	if _, err := m.SetRouting(ctx, other.ID, map[string]RoutingInput{}, base); err != nil {
		t.Fatal(err)
	}

	// Public ports come from the range, one per port; the range can run out.
	rs, err := m.SetRouting(ctx, db.ID, map[string]RoutingInput{"pg": {Public: &on, Allow: []string{"203.0.113.7", "10.0.0.0/8"}}}, base)
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].PublicPort != 20000 || rs[0].Address != "example.com:20000" || !slices.Equal(rs[0].Allow, []string{"10.0.0.0/8", "203.0.113.7/32"}) {
		t.Fatalf("public pg %+v", rs[0])
	}
	rs, err = m.SetRouting(ctx, web.ID, map[string]RoutingInput{"game": {Public: &on}}, base)
	if err != nil || rs[2].PublicPort != 20001 || rs[2].Protocol != "udp" {
		t.Fatalf("public udp %+v %v", rs, err)
	}
	if _, err := m.SetRouting(ctx, other.ID, map[string]RoutingInput{"pg": {Public: &on}}, base); !errors.As(err, new(ErrInvalid)) {
		t.Fatalf("assigned beyond the range: %v", err)
	}
	if publicChanges != 2 {
		t.Fatalf("public port changes notified %d times, want 2", publicChanges)
	}
	routes := m.PublicRoutes(ctx)
	if len(routes) != 2 || routes[0].Entrypoint() != "tcp-20000" || routes[1].Entrypoint() != "udp-20001" {
		t.Fatalf("public routes %+v", routes)
	}
	if _, err := m.SetRouting(ctx, db.ID, map[string]RoutingInput{"pg": {Public: &off}}, base); err != nil {
		t.Fatal(err)
	}
	if rs, _ := m.SetRouting(ctx, other.ID, map[string]RoutingInput{"pg": {Public: &on}}, base); rs[0].PublicPort != 20000 {
		t.Fatalf("released port not reused: %+v", rs)
	}

	// Custom domains: two services share a host by path; redirects.
	if _, err := m.AddDomain(ctx, web.ID, DomainInput{Host: "shop.io"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddDomain(ctx, web.ID, DomainInput{Host: "shop.io", Port: "admin", Path: "/admin/", StripPrefix: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddDomain(ctx, web.ID, DomainInput{Host: "www.shop.io", RedirectTo: "shop.io"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []DomainInput{
		{Host: "shop.io"},                              // taken
		{Host: "x.io", Path: "api"},                    // no leading slash
		{Host: "x.io", StripPrefix: true},              // strip without a path
		{Host: "x.io", Path: "/a", RedirectTo: "y.io"}, // redirect with a path
		{Host: "x.io", RedirectTo: "x.io"},             // to itself
	} {
		if _, err := m.AddDomain(ctx, web.ID, bad); !errors.As(err, new(ErrInvalid)) {
			t.Errorf("accepted domain %+v: %v", bad, err)
		}
	}
	if got := hosts(); !slices.Equal(got, []string{"api-production-shop.example.com", "shop.example.com", "shop.io", "shop.io/admin", "www.shop.io"}) {
		t.Fatalf("hosts with domains %v", got)
	}
	if err := m.RemoveDomain(ctx, web.ID, "shop.io"); err != nil {
		t.Fatal(err)
	}
	if got := hosts(); !slices.Equal(got, []string{"api-production-shop.example.com", "shop.example.com", "www.shop.io"}) {
		t.Fatalf("removing a host removes its paths: %v", got)
	}
}
