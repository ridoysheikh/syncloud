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

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/ingress"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/ports"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Chosen public ports, the port table, and the same number on different
// load balancers (Phase 26).
func TestChosenPublicPorts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	m := NewManager(st, nil, nodes.NewRegistry(st, bus, log), nil, bus, log)
	m.PublicPortRange = [2]int{20000, 20009}
	// shop and blog have their own load balancers; news uses the shared one
	// (the controller and edge node e1).
	targets := map[string]ingress.Target{"shop": {Nodes: []string{"n1"}}, "blog": {Nodes: []string{"n2"}}}
	led := ports.New(st, log)
	led.Target = func(p string) ingress.Target {
		if t, ok := targets[p]; ok {
			return t
		}
		return ingress.DefaultTarget
	}
	led.Shared = func() []string { return []string{"ctl-0", "e1"} }
	led.Holders = func(node string) map[string]string {
		if node == "e1" {
			return map[string]string{"tcp/2525": "postfix"}
		}
		return nil
	}
	led.Ranges = [][2]int{{20000, 20009}, {21000, 21009}}
	led.Sources = []ports.Source{m.PortWants}
	m.Ports = led
	now := time.Now().UTC().Truncate(time.Second)
	svc := func(project, name string, ps []Port) ServiceView {
		t.Helper()
		env := store.Environment{ID: "env_" + project, ProjectID: "prj_" + project, Name: "production", CreatedAt: now}
		if _, err := st.ProjectByName(ctx, project); errors.Is(err, store.ErrNotFound) {
			if err := st.CreateProject(ctx, store.Project{ID: "prj_" + project, Name: project, CreatedAt: now}, env); err != nil {
				t.Fatal(err)
			}
		}
		v, _, err := m.Apply(ctx, env, name, Spec{Image: "app:1", Ports: ps}, 1, "test")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	num := func(n int) *int { return &n }
	yes, no := true, false
	str := func(s string) *string { return &s }
	mail := svc("shop", "mail", []Port{{Name: "smtp", Container: 25, Protocol: "tcp"}, {Name: "smtps", Container: 465, Protocol: "tcp"}, {Name: "game", Container: 19132, Protocol: "udp"}})
	blogMail := svc("blog", "mail", []Port{{Name: "smtp", Container: 25, Protocol: "tcp"}})
	news := svc("news", "mail", []Port{{Name: "smtp", Container: 25, Protocol: "tcp"}, {Name: "alt", Container: 2525, Protocol: "tcp"}})
	news2 := svc("news", "other", []Port{{Name: "smtp", Container: 25, Protocol: "tcp"}})

	rs, err := m.SetRouting(ctx, mail.ID, map[string]RoutingInput{
		"smtp":  {PublicPort: num(25), ProxyProtocol: &yes},
		"smtps": {PublicPort: num(465), ProxyProtocol: &yes, TLS: str(TLSTerminate)},
		"game":  {PublicPort: num(19132)},
	}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !rs[0].Public || rs[0].PublicPort != 25 || rs[0].PortMode != store.PortChosen || !rs[0].ProxyProtocol || rs[0].TLS != "" {
		t.Errorf("smtp %+v", rs[0])
	}
	if rs[1].TLS != TLSTerminate || rs[2].PublicPort != 19132 {
		t.Errorf("smtps/game %+v %+v", rs[1], rs[2])
	}
	// The same number on another project's own load balancer, and on the
	// shared one.
	if _, err := m.SetRouting(ctx, blogMail.ID, map[string]RoutingInput{"smtp": {PublicPort: num(25)}}, "example.com"); err != nil {
		t.Fatalf("25 on another load balancer: %v", err)
	}
	if _, err := m.SetRouting(ctx, news.ID, map[string]RoutingInput{"smtp": {PublicPort: num(25)}}, "example.com"); err != nil {
		t.Fatalf("25 on the shared load balancer: %v", err)
	}
	// A second holder on the shared load balancer is refused.
	_, err = m.SetRouting(ctx, news2.ID, map[string]RoutingInput{"smtp": {PublicPort: num(25)}}, "example.com")
	if !errors.As(err, new(ErrInvalid)) || !strings.Contains(err.Error(), "service production/mail") {
		t.Fatalf("second holder: %v", err)
	}
	for name, in := range map[string]RoutingInput{
		"reserved 443":       {PublicPort: num(443)},
		"reserved 22":        {PublicPort: num(22)},
		"automatic range":    {PublicPort: num(20005)},
		"held by a process":  {PublicPort: num(2525)},
		"not a port":         {PublicPort: num(70000)},
		"proxy without port": {Public: &no, ProxyProtocol: &yes},
		"tls bad":            {PublicPort: num(2600), TLS: str("passthrough")},
	} {
		svcID := news2.ID
		port := "smtp"
		if name == "held by a process" {
			svcID, port = news.ID, "alt"
		}
		if _, err := m.SetRouting(ctx, svcID, map[string]RoutingInput{port: in}, "example.com"); !errors.As(err, new(ErrInvalid)) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	// Without a base domain there is no certificate for TLS.
	if _, err := m.SetRouting(ctx, news.ID, map[string]RoutingInput{"smtp": {TLS: str(TLSTerminate)}}, ""); !errors.As(err, new(ErrInvalid)) {
		t.Errorf("TLS without a base domain: %v", err)
	}
	// UDP can't take PROXY protocol or TLS.
	if _, err := m.SetRouting(ctx, mail.ID, map[string]RoutingInput{"game": {ProxyProtocol: &yes}}, "example.com"); !errors.As(err, new(ErrInvalid)) {
		t.Errorf("PROXY on udp: %v", err)
	}
	// The table: three nodes hold 25 (n1, n2 and both shared nodes).
	claims, _ := st.ListPortClaims(ctx)
	holders := map[string]string{}
	for _, c := range claims {
		if c.Protocol == "tcp" && c.Port == 25 {
			holders[c.Node] = c.OwnerID
		}
	}
	if holders["n1"] != mail.ID || holders["n2"] != blogMail.ID || holders["ctl-0"] != news.ID || holders["e1"] != news.ID {
		t.Errorf("holders of 25: %v", holders)
	}
	// Traefik names: the first holder keeps the plain name.
	names := map[string]PublicRoute{}
	for _, r := range m.PublicRoutes(ctx) {
		names[r.ServiceID+"/"+r.Name] = r
	}
	if _, ok := names[mail.ID+"/pub-tcp-25"]; !ok {
		t.Errorf("first holder renamed: %v", names)
	}
	if _, ok := names[blogMail.ID+"/pub-tcp-25-"+strings.TrimPrefix(blogMail.ID, "svc_")]; !ok {
		t.Errorf("second holder not suffixed: %v", names)
	}
	if r := names[mail.ID+"/pub-tcp-465"]; !r.ProxyProtocol || r.TLS != TLSTerminate {
		t.Errorf("smtps route %+v", r)
	}
	// Moving blog onto n1 clashes with shop's 25.
	clashes, err := led.CheckTarget(ctx, "blog", ingress.Target{Nodes: []string{"n1"}})
	if err != nil || len(clashes) != 1 || clashes[0].Node != "n1" || clashes[0].Port != 25 {
		t.Errorf("clashes %+v %v", clashes, err)
	}
	// Automatic ports stay cluster-wide unique, and the same number is kept
	// when it is sent back unchanged.
	rs, err = m.SetRouting(ctx, news2.ID, map[string]RoutingInput{"smtp": {Public: &yes}}, "example.com")
	if err != nil || rs[0].PublicPort != 20000 || rs[0].PortMode != store.PortAuto {
		t.Fatalf("automatic %+v %v", rs, err)
	}
	rs, err = m.SetRouting(ctx, news2.ID, map[string]RoutingInput{"smtp": {PublicPort: num(20000)}}, "example.com")
	if err != nil || rs[0].PortMode != store.PortAuto {
		t.Fatalf("same number sent back: %+v %v", rs, err)
	}
	// Back to automatic from chosen; closing frees the claims.
	rs, err = m.SetRouting(ctx, mail.ID, map[string]RoutingInput{"smtp": {PublicPort: num(0)}}, "example.com")
	if err != nil || rs[0].PublicPort != 20001 || rs[0].PortMode != store.PortAuto || !rs[0].ProxyProtocol {
		t.Fatalf("back to automatic %+v %v", rs, err)
	}
	if _, err := m.SetRouting(ctx, mail.ID, map[string]RoutingInput{"smtp": {Public: &no}, "smtps": {Public: &no}, "game": {Public: &no}}, "example.com"); err != nil {
		t.Fatal(err)
	}
	claims, _ = st.ListPortClaims(ctx)
	for _, c := range claims {
		if c.OwnerID == mail.ID {
			t.Errorf("claim left after closing: %+v", c)
		}
	}
	// A resync keeps the table as it is.
	before := len(claims)
	if err := led.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if claims, _ = st.ListPortClaims(ctx); len(claims) != before || len(led.Conflicts()) != 0 {
		t.Errorf("resync changed the table: %d -> %d, conflicts %v", before, len(claims), led.Conflicts())
	}
	// Deleting a service frees its claims.
	if err := st.DeleteService(ctx, news.ID); err != nil {
		t.Fatal(err)
	}
	claims, _ = st.ListPortClaims(ctx)
	for _, c := range claims {
		if c.OwnerID == news.ID {
			t.Errorf("claim left after delete: %+v", c)
		}
	}
}
