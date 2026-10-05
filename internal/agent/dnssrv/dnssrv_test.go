package dnssrv

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestAnswers(t *testing.T) {
	s := New("syncloud.internal", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetRecords(map[string][]string{
		"web.production.shop.syncloud.internal":        {"10.92.0.1"},
		"tasks.web.production.shop.syncloud.internal":  {"10.91.1.2", "10.91.2.2"},
		"tasks.idle.production.shop.syncloud.internal": nil,
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: s}
	started := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(started) }
	go srv.ActivateAndServe()
	<-started
	defer srv.Shutdown()

	ask := func(name string, qt uint16) *dns.Msg {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), qt)
		r, err := dns.Exchange(m, pc.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := ask("WEB.production.shop.syncloud.internal", dns.TypeA); len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != "10.92.0.1" {
		t.Fatalf("service: %v", r)
	}
	if r := ask("tasks.web.production.shop.syncloud.internal", dns.TypeA); len(r.Answer) != 2 {
		t.Fatalf("tasks: %v", r)
	}
	if r := ask("tasks.idle.production.shop.syncloud.internal", dns.TypeA); r.Rcode != dns.RcodeSuccess || len(r.Answer) != 0 {
		t.Fatalf("known but empty: %v", r)
	}
	if r := ask("nope.syncloud.internal", dns.TypeA); r.Rcode != dns.RcodeNameError {
		t.Fatalf("unknown: %v", r)
	}
	if r := ask("web.production.shop.syncloud.internal", dns.TypeAAAA); r.Rcode != dns.RcodeSuccess || len(r.Answer) != 0 {
		t.Fatalf("AAAA: %v", r)
	}
}
