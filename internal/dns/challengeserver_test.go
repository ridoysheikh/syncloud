package dns

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	mdns "github.com/miekg/dns"

	"github.com/ridoysheikh/syncloud/internal/acmedns"
)

// withBuiltin gives the service a challenge server on a free local port.
func withBuiltin(t *testing.T, s *Service) *acmedns.Server {
	t.Helper()
	b := acmedns.New(s.st, slog.New(slog.NewTextHandler(io.Discard, nil)), "127.0.0.1:0")
	t.Cleanup(b.Close)
	s.Builtin = b
	return b
}

func TestBuiltinServerIsTheDefaultForDomainsWithoutAProvider(t *testing.T) {
	ctx := context.Background()
	s, memA, _, _ := delegationSvc(t)
	b := withBuiltin(t, s)
	pa, _ := s.AddProvider(ctx, "a", "cloudflare", "a")
	pb, _ := s.AddProvider(ctx, "b", "cloudflare", "b")
	s.AddZone(ctx, AddZoneInput{Name: "shop.com"})                        // manual
	s.AddZone(ctx, AddZoneInput{Name: "validate.net", ProviderID: pb.ID}) // a provider
	_ = pa

	// Off: nothing changes for anybody.
	if r := s.Route(ctx, "shop.com"); r.Mode != "manual" {
		t.Fatalf("%+v", r)
	}
	if _, err := s.UpdateZone(ctx, "validate.net", UpdateZoneInput{ChallengeBuiltin: ptr(true)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the server must be set up first: %v", err)
	}

	// One click: no zone to choose, no address to give when it is detected.
	if _, err := s.ConfigureChallengeServer(ctx, true, "", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an address is needed: %v", err)
	}
	v, err := s.ConfigureChallengeServer(ctx, true, "203.0.113.9", false)
	zone := "_acme-challenge.203-0-113-9.sslip.io"
	if err != nil || !v.Listening || v.Zone != zone || v.NS != "203-0-113-9.sslip.io" {
		t.Fatalf("%+v %v", v, err)
	}
	if !slices.Equal(v.Domains, []string{"shop.com"}) {
		t.Fatalf("only the manual domain uses it by default: %v", v.Domains)
	}

	// shop.com has no provider: its challenge is answered by SynCloud.
	want := BuiltinTarget("shop.com", zone)
	r := s.Route(ctx, "*.shop.com")
	if r.Mode != "builtin" || !r.Auto || r.Target != want || r.TargetZone != zone || !strings.HasSuffix(want, "."+zone) {
		t.Fatalf("%+v", r)
	}
	// Names outside every registered domain too.
	if r := s.Route(ctx, "other.org"); r.Mode != "builtin" || r.Zone != "" {
		t.Fatalf("%+v", r)
	}
	// A provider keeps answering directly unless the domain chooses the server.
	if r := s.Route(ctx, "validate.net"); r.Mode != "direct" {
		t.Fatalf("%+v", r)
	}

	// The TXT value goes to the server, is answered over DNS, and is removed.
	id, err := s.PutTXT(ctx, "_acme-challenge.shop.com", "tok1")
	if err != nil || !strings.HasPrefix(id, "builtin:") {
		t.Fatalf("%q %v", id, err)
	}
	in := ask(t, b.Addr(), want, mdns.TypeTXT)
	if len(in.Answer) != 1 || in.Answer[0].(*mdns.TXT).Txt[0] != "tok1" {
		t.Fatalf("%v", in)
	}
	if err := s.DeleteTXT(ctx, "_acme-challenge.shop.com", id); err != nil {
		t.Fatal(err)
	}
	if in := ask(t, b.Addr(), want, mdns.TypeTXT); len(in.Answer) != 0 {
		t.Fatalf("not removed: %v", in)
	}
	if len(memA.Snapshot()) != 0 {
		t.Fatal("a provider was touched")
	}

	// A provider domain can choose the server; its CNAME is then made for it.
	z, err := s.UpdateZone(ctx, "validate.net", UpdateZoneInput{ChallengeBuiltin: ptr(true)})
	if err != nil || !z.ChallengeBuiltin {
		t.Fatalf("%+v %v", z, err)
	}
	if rr, err := s.EnsureDelegation(ctx, "validate.net", false); err != nil || rr.Mode != "builtin" || rr.CNAME == nil || !rr.CNAME.Managed {
		t.Fatalf("%+v %v", rr, err)
	}
	// An external validation domain is chosen instead of the server: they exclude each other.
	vz := "validate.net"
	z, err = s.UpdateZone(ctx, "shop.com", UpdateZoneInput{ChallengeZone: &vz})
	if err != nil || z.ChallengeBuiltin || s.Route(ctx, "shop.com").Mode != "delegated" {
		t.Fatalf("external is optional and wins when chosen: %+v %v", z, err)
	}
	empty := ""
	s.UpdateZone(ctx, "shop.com", UpdateZoneInput{ChallengeZone: &empty})
	if s.Route(ctx, "shop.com").Mode != "builtin" {
		t.Fatal("back to the server")
	}

	// Switching the server off: manual domains are manual again.
	if _, err := s.ConfigureChallengeServer(ctx, false, "", false); err != nil {
		t.Fatal(err)
	}
	if r := s.Route(ctx, "shop.com"); r.Mode != "manual" {
		t.Fatalf("%+v", r)
	}
}

func ptr[T any](v T) *T { return &v }

func ask(t *testing.T, addr, name string, qtype uint16) *mdns.Msg {
	t.Helper()
	m := new(mdns.Msg)
	m.SetQuestion(mdns.Fqdn(name), qtype)
	in, _, err := new(mdns.Client).Exchange(m, addr)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestChallengeServerCheck(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := delegationSvc(t)
	b := withBuiltin(t, s)
	if _, err := s.CheckChallengeServer(ctx); !errors.Is(err, ErrInvalid) {
		t.Fatalf("off: %v", err)
	}
	if _, err := s.ConfigureChallengeServer(ctx, true, "203.0.113.9", false); err != nil {
		t.Fatal(err)
	}

	// Until the internet reaches the server, the check says so.
	resolver, raddr := startRecursor(t, b)
	s.Resolvers = []string{raddr}
	checks, err := s.CheckChallengeServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	if len(checks) != 2 || !byName["Listening"].OK || byName["Reachable from the internet"].OK {
		t.Fatalf("not reachable yet: %+v", checks)
	}

	// sslip.io delegates the zone to the address: the probe comes back.
	resolver.publish(b.Zone(), b.Config().NS, "203.0.113.9")
	checks, _ = s.CheckChallengeServer(ctx)
	for _, c := range checks {
		if !c.OK {
			t.Fatalf("%+v", checks)
		}
	}
	if left := ask(t, b.Addr(), "probe-x."+b.Zone(), mdns.TypeTXT); len(left.Answer) != 0 {
		t.Fatal("the probe value was left behind")
	}
}

func TestChallengeServerFollowsTheAddress(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := delegationSvc(t)
	b := withBuiltin(t, s)
	if changed, err := s.FollowChallengeServerIP(ctx, "198.51.100.7"); changed || err != nil || b.Enabled() {
		t.Fatalf("off stays off: %v %v", changed, err)
	}
	s.ConfigureChallengeServer(ctx, true, "203.0.113.9", false)
	if changed, err := s.FollowChallengeServerIP(ctx, "203.0.113.9"); changed || err != nil {
		t.Fatalf("same address: %v %v", changed, err)
	}
	changed, err := s.FollowChallengeServerIP(ctx, "198.51.100.7")
	if !changed || err != nil || b.Zone() != "_acme-challenge.198-51-100-7.sslip.io" {
		t.Fatalf("%v %v %q", changed, err, b.Zone())
	}
	// An address given by hand (NAT) is kept.
	s.ConfigureChallengeServer(ctx, true, "203.0.113.9", true)
	if changed, _ := s.FollowChallengeServerIP(ctx, "192.0.2.1"); changed || b.Config().IP != "203.0.113.9" {
		t.Fatal("a pinned address was moved")
	}
	// A zone of the older kind (under one of your domains) is left alone.
	b.Configure(ctx, acmedns.Config{Zone: "acme.example.org", NS: "acme-ns.example.org", IP: "203.0.113.9"})
	if changed, _ := s.FollowChallengeServerIP(ctx, "192.0.2.1"); changed || b.Zone() != "acme.example.org" {
		t.Fatal("an older zone was moved")
	}
}

// The controller's own *.<ip>.sslip.io wildcard: its challenge name is the
// zone itself, so it needs no CNAME and the authority asks the server directly.
func TestOwnSslipWildcardNeedsNoCNAME(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := delegationSvc(t)
	b := withBuiltin(t, s)
	if r := s.Route(ctx, "*.203-0-113-9.sslip.io"); r.Mode != "manual" {
		t.Fatalf("off: %+v", r)
	}
	s.ConfigureChallengeServer(ctx, true, "203.0.113.9", false)
	r := s.Challenge(ctx, "*.203-0-113-9.sslip.io")
	if r.Mode != "builtin" || !r.Auto || r.Target != b.Zone() || r.CNAME != nil {
		t.Fatalf("%+v", r)
	}
	if info := (CertHost{Service: s}).Challenge(ctx, "_acme-challenge.203-0-113-9.sslip.io"); !info.CNAMEOK || info.Mode != "builtin" {
		t.Fatalf("nothing to wait for: %+v", info)
	}
	id, err := s.PutTXT(ctx, "_acme-challenge.203-0-113-9.sslip.io", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if in := ask(t, b.Addr(), b.Zone(), mdns.TypeTXT); len(in.Answer) != 1 {
		t.Fatalf("%v", in)
	}
	s.DeleteTXT(ctx, "_acme-challenge.203-0-113-9.sslip.io", id)
	// Another address's sslip.io name is not the controller's: it needs a CNAME it can never get.
	if r := s.Route(ctx, "*.198-51-100-7.sslip.io"); r.Target == r.Name {
		t.Fatalf("%+v", r)
	}
}

// recursor is a pretend public resolver: it knows the delegation it is told
// and sends questions inside the challenge zone to the challenge server.
type recursor struct {
	mu    sync.Mutex
	ns, a map[string]string
	b     *acmedns.Server
}

// publish makes the delegation visible, as it becomes on the internet.
func (r *recursor) publish(zone, ns, ip string) {
	r.mu.Lock()
	r.ns[zone], r.a[ns] = ns, ip
	r.mu.Unlock()
}

func startRecursor(t *testing.T, b *acmedns.Server) (*recursor, string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &recursor{ns: map[string]string{}, a: map[string]string{}, b: b}
	srv := &mdns.Server{PacketConn: pc, Handler: mdns.HandlerFunc(func(w mdns.ResponseWriter, q *mdns.Msg) {
		a := new(mdns.Msg)
		a.SetReply(q)
		name := strings.TrimSuffix(q.Question[0].Name, ".")
		hdr := mdns.RR_Header{Name: q.Question[0].Name, Class: mdns.ClassINET, Ttl: 60}
		r.mu.Lock()
		defer r.mu.Unlock()
		switch q.Question[0].Qtype {
		case mdns.TypeNS:
			if to, ok := r.ns[name]; ok {
				a.Answer = append(a.Answer, &mdns.NS{Hdr: withType(hdr, mdns.TypeNS), Ns: mdns.Fqdn(to)})
			}
		case mdns.TypeA:
			if ip, ok := r.a[name]; ok {
				a.Answer = append(a.Answer, &mdns.A{Hdr: withType(hdr, mdns.TypeA), A: net.ParseIP(ip)})
			}
		default:
			if _, delegated := r.ns[b.Zone()]; delegated && (name == b.Zone() || strings.HasSuffix(name, "."+b.Zone())) {
				if in, _, err := new(mdns.Client).Exchange(q, b.Addr()); err == nil {
					a.Answer = in.Answer
				}
			}
		}
		_ = w.WriteMsg(a)
	})}
	go srv.ActivateAndServe()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return r, pc.LocalAddr().String()
}

func withType(h mdns.RR_Header, t uint16) mdns.RR_Header { h.Rrtype = t; return h }
