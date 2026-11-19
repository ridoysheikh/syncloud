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

	// The challenge zone cannot be a registered domain.
	if _, err := s.ConfigureChallengeServer(ctx, "validate.net", "", "203.0.113.9"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	v, err := s.ConfigureChallengeServer(ctx, "acme.example.org", "", "203.0.113.9")
	if err != nil || !v.Listening || v.NS != "acme-ns.example.org" {
		t.Fatalf("%+v %v", v, err)
	}
	if len(v.Needed) != 2 || v.Needed[0].Type != "A" || v.Needed[1].Type != "NS" || v.Needed[0].Managed {
		t.Fatalf("records to create: %+v", v.Needed)
	}
	if !slices.Equal(v.Domains, []string{"shop.com"}) {
		t.Fatalf("only the manual domain uses it by default: %v", v.Domains)
	}

	// shop.com has no provider: its challenge is answered by SynCloud.
	want := BuiltinTarget("shop.com", "acme.example.org")
	r := s.Route(ctx, "*.shop.com")
	if r.Mode != "builtin" || !r.Auto || r.Target != want || r.TargetZone != "acme.example.org" || !strings.HasSuffix(want, ".acme.example.org") {
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
	if _, err := s.ConfigureChallengeServer(ctx, "", "", ""); err != nil {
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

func TestChallengeServerRecordsAndCheck(t *testing.T) {
	ctx := context.Background()
	s, memA, _, _ := delegationSvc(t)
	b := withBuiltin(t, s)
	pa, _ := s.AddProvider(ctx, "a", "cloudflare", "a")
	s.AddZone(ctx, AddZoneInput{Name: "shop.com", ProviderID: pa.ID})
	v, err := s.ConfigureChallengeServer(ctx, "acme.shop.com", "ns.acme-dns.shop.com", "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Needed[0].Managed || v.Needed[0].Zone != "shop.com" || v.Needed[1].Zone != "shop.com" {
		t.Fatalf("the records belong in shop.com, which has a provider: %+v", v.Needed)
	}

	// The check says what is missing, then passes once DNS is right.
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
	if !byName["Listening"].OK || byName["NS record"].OK || byName["Name server address"].OK || byName["Reachable from the internet"].OK {
		t.Fatalf("nothing is published yet: %+v", checks)
	}

	n, err := s.CreateChallengeServerRecords(ctx, false)
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if got := memA.Snapshot(); !slices.Contains(got, "A ns.acme-dns.shop.com 203.0.113.9") || !slices.Contains(got, "NS acme.shop.com ns.acme-dns.shop.com") {
		t.Fatalf("%v", got)
	}
	memA.Calls = nil
	if n, err := s.CreateChallengeServerRecords(ctx, false); err != nil || n != 0 || len(memA.Calls) != 0 {
		t.Fatalf("idempotent: %d %v %v", n, err, memA.Calls)
	}

	// The "internet" now has the delegation: the recursor knows it.
	resolver.publish("acme.shop.com", "ns.acme-dns.shop.com", "203.0.113.9")
	checks, _ = s.CheckChallengeServer(ctx)
	for _, c := range checks {
		if !c.OK {
			t.Fatalf("%+v", checks)
		}
	}
	if left := ask(t, b.Addr(), "probe-x.acme.shop.com", mdns.TypeTXT); len(left.Answer) != 0 {
		t.Fatal("the probe value was left behind")
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
			if _, delegated := r.ns[b.Zone()]; delegated && strings.HasSuffix(name, "."+b.Zone()) {
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
