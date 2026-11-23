package acmedns

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"testing"

	mdns "github.com/miekg/dns"

	"github.com/ridoysheikh/syncloud/internal/store"
)

func newServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "127.0.0.1:0")
	t.Cleanup(s.Close)
	return s, st
}

func ask(t *testing.T, addr, name string, qtype uint16, network string) *mdns.Msg {
	t.Helper()
	m := new(mdns.Msg)
	m.SetQuestion(mdns.Fqdn(name), qtype)
	c := &mdns.Client{Net: network}
	in, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func txts(m *mdns.Msg) []string {
	var out []string
	for _, rr := range m.Answer {
		if t, ok := rr.(*mdns.TXT); ok {
			out = append(out, t.Txt...)
		}
	}
	slices.Sort(out)
	return out
}

func TestAnswersOnlyItsZone(t *testing.T) {
	ctx := context.Background()
	s, _ := newServer(t)
	if s.Enabled() || s.Addr() != "" {
		t.Fatal("off until a zone is set")
	}
	if _, err := s.Put(ctx, "x.acme.example.com", "v"); err == nil {
		t.Fatal("a value cannot be stored while off")
	}
	if err := s.Configure(ctx, Config{Zone: "acme.example.com", NS: "acme-ns.example.com", IP: "203.0.113.9"}); err != nil {
		t.Fatal(err)
	}
	if !s.Enabled() || s.Addr() == "" || !s.Status(ctx).Listening {
		t.Fatalf("%+v", s.Status(ctx))
	}
	addr := s.Addr()

	id1, err := s.Put(ctx, "ab12.acme.example.com", "value-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "AB12.acme.example.com.", "value-two"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "other.example.com", "x"); err == nil {
		t.Fatal("outside the zone")
	}

	for _, network := range []string{"udp", "tcp"} {
		in := ask(t, addr, "Ab12.acme.example.com", mdns.TypeTXT, network)
		if !in.Authoritative || !slices.Equal(txts(in), []string{"value-one", "value-two"}) {
			t.Fatalf("%s: %v", network, in)
		}
	}
	if in := ask(t, addr, "nothing.acme.example.com", mdns.TypeTXT, "udp"); in.Rcode != mdns.RcodeNameError || len(in.Answer) != 0 {
		t.Fatalf("unknown name: %v", in)
	}
	ns := ask(t, addr, "acme.example.com", mdns.TypeNS, "udp")
	if len(ns.Answer) != 1 || ns.Answer[0].(*mdns.NS).Ns != "acme-ns.example.com." {
		t.Fatalf("%v", ns)
	}
	if soa := ask(t, addr, "acme.example.com", mdns.TypeSOA, "udp"); len(soa.Answer) != 1 {
		t.Fatalf("%v", soa)
	}
	// Never an open resolver or a general name server.
	if in := ask(t, addr, "www.google.com", mdns.TypeA, "udp"); in.Rcode != mdns.RcodeRefused || len(in.Answer) != 0 {
		t.Fatalf("%v", in)
	}
	if in := ask(t, addr, "ab12.acme.example.com", mdns.TypeA, "udp"); len(in.Answer) != 0 || in.Rcode != mdns.RcodeSuccess {
		t.Fatalf("an A question is answered empty: %v", in)
	}

	// Removing one value leaves the other.
	if err := s.Delete(ctx, id1); err != nil {
		t.Fatal(err)
	}
	if got := txts(ask(t, addr, "ab12.acme.example.com", mdns.TypeTXT, "udp")); !slices.Equal(got, []string{"value-two"}) {
		t.Fatalf("%v", got)
	}
}

func TestConfigSurvivesRestartAndValidates(t *testing.T) {
	ctx := context.Background()
	s, st := newServer(t)
	for _, c := range []Config{
		{Zone: "acme", NS: "ns.example.com", IP: "203.0.113.9"},
		{Zone: "acme.example.com", NS: "ns.example.com", IP: "not-an-ip"},
		{Zone: "Acme.Example.com", NS: "ns.example.com", IP: "203.0.113.9"},
		{Zone: "acme.example.com", NS: "", IP: "203.0.113.9"},
	} {
		if err := s.Configure(ctx, c); err == nil {
			t.Errorf("%+v should be refused", c)
		}
	}
	good := Config{Zone: "acme.example.com", NS: "acme-ns.example.com", IP: "203.0.113.9"}
	if err := s.Configure(ctx, good); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "k.acme.example.com", "v"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2 := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "127.0.0.1:0")
	t.Cleanup(s2.Close)
	if err := s2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if s2.Config() != good || !s2.Status(ctx).Listening {
		t.Fatalf("%+v", s2.Status(ctx))
	}
	if got := txts(ask(t, s2.Addr(), "k.acme.example.com", mdns.TypeTXT, "udp")); !slices.Equal(got, []string{"v"}) {
		t.Fatalf("an order's value survives a restart: %v", got)
	}
	// Switching it off stops answering.
	addr := s2.Addr()
	if err := s2.Configure(ctx, Config{}); err != nil {
		t.Fatal(err)
	}
	if s2.Enabled() || s2.Addr() != "" {
		t.Fatal("still on")
	}
	m := new(mdns.Msg)
	m.SetQuestion("k.acme.example.com.", mdns.TypeTXT)
	if _, _, err := (&mdns.Client{Timeout: 500e6}).Exchange(m, addr); err == nil {
		t.Fatal("it still answers")
	}
}

func TestListenFailureIsShownNotFatal(t *testing.T) {
	ctx := context.Background()
	taken, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	s, _ := newServer(t)
	s.Listen = taken.LocalAddr().String()
	if err := s.Configure(ctx, Config{Zone: "acme.example.com", NS: "acme-ns.example.com", IP: "203.0.113.9"}); err != nil {
		t.Fatalf("a busy port must not fail the configuration: %v", err)
	}
	if st := s.Status(ctx); !st.Enabled || st.Listening || st.Error == "" {
		t.Fatalf("%+v", st)
	}
}

func TestForIPIsTheSslipZone(t *testing.T) {
	c := ForIP("203.0.113.9")
	if c.Zone != "_acme-challenge.203-0-113-9.sslip.io" || c.NS != "203-0-113-9.sslip.io" || c.IP != "203.0.113.9" {
		t.Fatalf("%+v", c)
	}
	if err := ValidConfig(c); err != nil {
		t.Fatal(err)
	}
	if !IsSslip(c) || IsSslip(Config{Zone: "acme.example.com", NS: "acme-ns.example.com", IP: "203.0.113.9"}) {
		t.Fatal("IsSslip")
	}
}

// sslip.io hands _acme-challenge.<ip>.sslip.io and every name below it to the
// IP: the server answers a CNAME target below the zone and, for the
// controller's own *.<ip>.sslip.io wildcard, the zone apex itself.
func TestAnswersTheSslipZoneAndItsApex(t *testing.T) {
	ctx := context.Background()
	s, _ := newServer(t)
	if err := s.Configure(ctx, ForIP("203.0.113.9")); err != nil {
		t.Fatal(err)
	}
	zone := "_acme-challenge.203-0-113-9.sslip.io"
	if _, err := s.Put(ctx, "3f9a1c2b7d4e."+zone, "below"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, zone, "apex"); err != nil {
		t.Fatal(err)
	}
	addr := s.Addr()
	if got := txts(ask(t, addr, "3f9a1c2b7d4e."+zone, mdns.TypeTXT, "udp")); !slices.Equal(got, []string{"below"}) {
		t.Fatalf("below: %v", got)
	}
	if in := ask(t, addr, zone, mdns.TypeTXT, "udp"); !in.Authoritative || !slices.Equal(txts(in), []string{"apex"}) {
		t.Fatalf("apex: %v", in)
	}
	if in := ask(t, addr, "203-0-113-9.sslip.io", mdns.TypeA, "udp"); in.Rcode != mdns.RcodeRefused {
		t.Fatalf("the parent is sslip.io's, not ours: %v", in)
	}
}
