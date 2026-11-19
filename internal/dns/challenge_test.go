package dns

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	mdns "github.com/miekg/dns"
)

// cnameResolver is a DNS server that answers CNAME questions from a map.
type cnameResolver struct {
	mu sync.Mutex
	m  map[string]string
}

func (r *cnameResolver) set(name, target string) {
	r.mu.Lock()
	r.m[Fqdn(name)] = Fqdn(target)
	r.mu.Unlock()
}

func startCNAMEResolver(t *testing.T) (*cnameResolver, string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &cnameResolver{m: map[string]string{}}
	srv := &mdns.Server{PacketConn: pc, Handler: mdns.HandlerFunc(func(w mdns.ResponseWriter, q *mdns.Msg) {
		a := new(mdns.Msg)
		a.SetReply(q)
		name := Fqdn(q.Question[0].Name)
		r.mu.Lock()
		to, ok := r.m[name]
		r.mu.Unlock()
		if ok && q.Question[0].Qtype == mdns.TypeCNAME {
			a.Answer = append(a.Answer, &mdns.CNAME{Hdr: mdns.RR_Header{Name: q.Question[0].Name, Rrtype: mdns.TypeCNAME, Class: mdns.ClassINET, Ttl: 60}, Target: mdns.Fqdn(to)})
		}
		_ = w.WriteMsg(a)
	})}
	go srv.ActivateAndServe()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return r, pc.LocalAddr().String()
}

// two zones, each at its own provider: shop.com (A) and validate.net (B).
func delegationSvc(t *testing.T) (s *Service, memA, memB *Mem, res *cnameResolver) {
	t.Helper()
	memA, memB = NewMem("shop.com"), NewMem("validate.net")
	s = newSvc(t, memA)
	s.Factory = func(typ, token string) (Provider, error) {
		if token == "b" {
			return memB, nil
		}
		return memA, nil
	}
	var addr string
	res, addr = startCNAMEResolver(t)
	s.Resolvers = []string{addr}
	return
}

func TestChallengeRouteModes(t *testing.T) {
	ctx := context.Background()
	s, memA, memB, _ := delegationSvc(t)
	pa, _ := s.AddProvider(ctx, "a", "cloudflare", "a")
	pb, _ := s.AddProvider(ctx, "b", "cloudflare", "b")

	// A domain without a provider: manual.
	if _, err := s.AddZone(ctx, AddZoneInput{Name: "shop.com"}); err != nil {
		t.Fatal(err)
	}
	r := s.Route(ctx, "*.shop.com")
	if r.Mode != "manual" || r.Auto || r.Name != "_acme-challenge.shop.com" || r.Zone != "shop.com" {
		t.Fatalf("%+v", r)
	}
	if r := s.Route(ctx, "other.org"); r.Mode != "manual" || r.Zone != "" {
		t.Fatalf("outside every domain: %+v", r)
	}

	// The validation domain must exist, have a provider, and not delegate itself.
	if _, err := s.AddZone(ctx, AddZoneInput{Name: "validate.net"}); err != nil {
		t.Fatal(err)
	}
	bad := func(name string) {
		t.Helper()
		if _, err := s.UpdateZone(ctx, "shop.com", UpdateZoneInput{ChallengeZone: &name}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	bad("shop.com")     // itself
	bad("nowhere.org")  // not registered
	bad("validate.net") // registered but without a provider
	if _, err := s.UpdateZone(ctx, "validate.net", UpdateZoneInput{Provider: &pb.ID}); err != nil {
		t.Fatal(err)
	}

	vz := "validate.net"
	z, err := s.UpdateZone(ctx, "shop.com", UpdateZoneInput{ChallengeZone: &vz})
	if err != nil || z.ChallengeZone != vz {
		t.Fatalf("%+v %v", z, err)
	}
	// A validation domain cannot itself delegate.
	sz := "shop.com"
	if _, err := s.UpdateZone(ctx, "validate.net", UpdateZoneInput{ChallengeZone: &sz}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no chains: %v", err)
	}

	want := DelegationTarget("_acme-challenge.shop.com", vz)
	if !strings.HasSuffix(want, ".acme.validate.net") || DelegationTarget("*.shop.com", vz) != want {
		t.Fatalf("the target is per challenge name, not per wildcard: %s", want)
	}
	r = s.Route(ctx, "*.shop.com")
	if r.Mode != "delegated" || !r.Auto || r.Target != want || r.TargetZone != vz {
		t.Fatalf("%+v", r)
	}
	if other := s.Route(ctx, "*.sub.shop.com"); other.Target == want || other.Name != "_acme-challenge.sub.shop.com" {
		t.Fatalf("each challenge name gets its own target: %+v", other)
	}

	// The TXT goes to the validation zone; the domain's own zone is untouched.
	id, err := s.PutTXT(ctx, "_acme-challenge.shop.com", "tok1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(memB.Snapshot(), "TXT "+want+" tok1") || len(memA.Snapshot()) != 0 {
		t.Fatalf("B %v A %v", memB.Snapshot(), memA.Snapshot())
	}
	if id2, _ := s.PutTXT(ctx, "_acme-challenge.shop.com", "tok1"); id2 != id {
		t.Fatal("reused")
	}
	if err := s.DeleteTXT(ctx, "_acme-challenge.shop.com", id); err != nil || len(memB.Snapshot()) != 0 {
		t.Fatalf("%v %v", err, memB.Snapshot())
	}

	// The CNAME is checked at public resolvers: absent, wrong, right.
	_ = pa
	c := s.Challenge(ctx, "shop.com")
	if c.CNAME == nil || c.CNAME.Present || c.CNAME.Managed {
		t.Fatalf("a manual domain: the person makes the CNAME: %+v", c.CNAME)
	}
	// (the resolver map is shared through delegationSvc's return)
}

func TestChallengeCNAMEVisibility(t *testing.T) {
	ctx := context.Background()
	s, _, _, res := delegationSvc(t)
	pb, _ := s.AddProvider(ctx, "b", "cloudflare", "b")
	s.AddZone(ctx, AddZoneInput{Name: "shop.com"})
	s.AddZone(ctx, AddZoneInput{Name: "validate.net", ProviderID: pb.ID})
	vz := "validate.net"
	s.UpdateZone(ctx, "shop.com", UpdateZoneInput{ChallengeZone: &vz})
	target := DelegationTarget("shop.com", vz)

	if c := s.Challenge(ctx, "shop.com"); c.CNAME.Present {
		t.Fatalf("nothing published yet: %+v", c.CNAME)
	}
	res.set("_acme-challenge.shop.com", "elsewhere.example.org")
	c := s.Challenge(ctx, "shop.com")
	if c.CNAME.Present || !slices.Contains(c.CNAME.Seen, "elsewhere.example.org") {
		t.Fatalf("a CNAME to somewhere else is shown as such: %+v", c.CNAME)
	}
	res.set("_acme-challenge.shop.com", target+".")
	if c := s.Challenge(ctx, "shop.com"); !c.CNAME.Present {
		t.Fatalf("%+v", c.CNAME)
	}
}

func TestEnsureDelegationInManagedZone(t *testing.T) {
	ctx := context.Background()
	s, memA, memB, _ := delegationSvc(t)
	pa, _ := s.AddProvider(ctx, "a", "cloudflare", "a")
	pb, _ := s.AddProvider(ctx, "b", "cloudflare", "b")
	s.AddZone(ctx, AddZoneInput{Name: "shop.com", ProviderID: pa.ID})
	s.AddZone(ctx, AddZoneInput{Name: "validate.net", ProviderID: pb.ID})

	// Direct while nothing delegates: the TXT goes to shop.com's own provider.
	if r := s.Route(ctx, "shop.com"); r.Mode != "direct" || !r.Auto {
		t.Fatalf("%+v", r)
	}
	if _, err := s.EnsureDelegation(ctx, "shop.com", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not delegated: %v", err)
	}

	vz := "validate.net"
	if _, err := s.UpdateZone(ctx, "shop.com", UpdateZoneInput{ChallengeZone: &vz}); err != nil {
		t.Fatal(err)
	}
	target := DelegationTarget("shop.com", vz)
	r, err := s.EnsureDelegation(ctx, "*.shop.com", false)
	if err != nil || r.CNAME == nil || !r.CNAME.Managed {
		t.Fatalf("%+v %v", r, err)
	}
	cn := "CNAME _acme-challenge.shop.com " + target
	if !slices.Equal(memA.Snapshot(), []string{cn}) {
		t.Fatalf("%v", memA.Snapshot())
	}
	memA.Calls = nil
	if _, err := s.EnsureDelegation(ctx, "shop.com", false); err != nil || len(memA.Calls) != 0 {
		t.Fatalf("idempotent: %v %v", err, memA.Calls)
	}

	// A leftover TXT at the delegation target is a challenge leftover: cleanup removes it, the CNAME stays.
	if _, err := s.PutTXT(ctx, "_acme-challenge.shop.com", "left-over"); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Cleanup(ctx, "validate.net", true); err != nil || res.Removed != 1 || len(memB.Snapshot()) != 0 {
		t.Fatalf("%+v %v %v", res, err, memB.Snapshot())
	}
	if !slices.Equal(memA.Snapshot(), []string{cn}) {
		t.Fatalf("the CNAME is not a leftover: %v", memA.Snapshot())
	}

	// The TXT is written in the validation zone, not beside the CNAME.
	if _, err := s.PutTXT(ctx, "_acme-challenge.shop.com", "v"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(memB.Snapshot(), "TXT "+target+" v") || slices.ContainsFunc(memA.Snapshot(), func(x string) bool { return strings.HasPrefix(x, "TXT") }) {
		t.Fatalf("A %v B %v", memA.Snapshot(), memB.Snapshot())
	}

	// Clearing the delegation removes the CNAME the platform made.
	empty := ""
	if _, err := s.UpdateZone(ctx, "shop.com", UpdateZoneInput{ChallengeZone: &empty}); err != nil {
		t.Fatal(err)
	}
	if len(memA.Snapshot()) != 0 {
		t.Fatalf("the CNAME stays after delegation is cleared: %v", memA.Snapshot())
	}
	if r := s.Route(ctx, "shop.com"); r.Mode != "direct" {
		t.Fatalf("%+v", r)
	}
}

func TestEnsureDelegationDoesNotReplaceOthersRecords(t *testing.T) {
	ctx := context.Background()
	s, memA, _, _ := delegationSvc(t)
	pa, _ := s.AddProvider(ctx, "a", "cloudflare", "a")
	pb, _ := s.AddProvider(ctx, "b", "cloudflare", "b")
	s.AddZone(ctx, AddZoneInput{Name: "shop.com", ProviderID: pa.ID})
	s.AddZone(ctx, AddZoneInput{Name: "validate.net", ProviderID: pb.ID})
	vz := "validate.net"
	s.UpdateZone(ctx, "shop.com", UpdateZoneInput{ChallengeZone: &vz})
	memA.add(Record{Type: "TXT", Name: "_acme-challenge.shop.com", Content: "from-another-tool"})

	if _, err := s.EnsureDelegation(ctx, "shop.com", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("somebody else's TXT is in the way: %v", err)
	}
	if !slices.Contains(memA.Snapshot(), "TXT _acme-challenge.shop.com from-another-tool") {
		t.Fatal("it must stay until confirmed")
	}
	if _, err := s.EnsureDelegation(ctx, "shop.com", true); err != nil {
		t.Fatal(err)
	}
	if len(memA.Snapshot()) != 1 || !strings.HasPrefix(memA.Snapshot()[0], "CNAME _acme-challenge.shop.com") {
		t.Fatalf("%v", memA.Snapshot())
	}
}

func TestExistingCNAMEIsFollowed(t *testing.T) {
	ctx := context.Background()
	s, memA, memB, _ := delegationSvc(t)
	pa, _ := s.AddProvider(ctx, "a", "cloudflare", "a")
	pb, _ := s.AddProvider(ctx, "b", "cloudflare", "b")
	s.AddZone(ctx, AddZoneInput{Name: "shop.com", ProviderID: pa.ID})
	s.AddZone(ctx, AddZoneInput{Name: "validate.net", ProviderID: pb.ID})

	// The person delegated by hand to a name in a domain SynCloud can write.
	memA.add(Record{Type: "CNAME", Name: "_acme-challenge.shop.com", Content: "x.validate.net"})
	r := s.Route(ctx, "shop.com")
	if r.Mode != "delegated" || r.Target != "x.validate.net" || !r.Auto {
		t.Fatalf("%+v", r)
	}
	if _, err := s.PutTXT(ctx, "_acme-challenge.shop.com", "v"); err != nil || !slices.Contains(memB.Snapshot(), "TXT x.validate.net v") {
		t.Fatalf("%v %v", err, memB.Snapshot())
	}

	// A CNAME to a domain SynCloud cannot write: the person adds the TXT there.
	memA.Fail = map[string]error{}
	memA2 := NewMem("shop.com", Record{Type: "CNAME", Name: "_acme-challenge.shop.com", Content: "auth.acme-dns.io"})
	s.Factory = func(string, string) (Provider, error) { return memA2, nil }
	s.mu.Lock()
	s.cache = map[string]cachedProvider{}
	s.mu.Unlock()
	r = s.Route(ctx, "shop.com")
	if r.Mode != "manual" || r.Auto || !strings.Contains(r.Note, "auth.acme-dns.io") {
		t.Fatalf("%+v", r)
	}
}
