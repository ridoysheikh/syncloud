// Package dnssrv is the agent's DNS server for tasks (§8.1). It answers the
// internal zone from the last service directory the controller sent (so it
// keeps working during a controller outage) and forwards other names.
package dnssrv

import (
	"bufio"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const ttl = 5 // seconds: task sets change often

type Server struct {
	zone string // e.g. "syncloud.internal."
	log  *slog.Logger

	mu       sync.RWMutex
	records  map[string][]net.IP // fqdn with trailing dot -> IPv4s
	upstream []string
	servers  []*dns.Server
	addr     string
}

func New(zone string, log *slog.Logger) *Server {
	return &Server{zone: dns.Fqdn(strings.ToLower(zone)), log: log, records: map[string][]net.IP{}, upstream: upstreams("/etc/resolv.conf")}
}

// upstreams reads the host's resolvers (systemd-resolved's stub included:
// the agent runs in the host network namespace).
func upstreams(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{"1.1.1.1:53"}
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			out = append(out, net.JoinHostPort(fields[1], "53"))
		}
	}
	if len(out) == 0 {
		out = []string{"1.1.1.1:53"}
	}
	return out
}

// SetRecords replaces the internal records (names without trailing dots).
func (s *Server) SetRecords(recs map[string][]string) {
	m := make(map[string][]net.IP, len(recs))
	for name, ips := range recs {
		for _, ip := range ips {
			if p := net.ParseIP(ip).To4(); p != nil {
				m[dns.Fqdn(strings.ToLower(name))] = append(m[dns.Fqdn(strings.ToLower(name))], p)
			}
		}
		if _, ok := m[dns.Fqdn(strings.ToLower(name))]; !ok {
			m[dns.Fqdn(strings.ToLower(name))] = nil // known name, no addresses yet
		}
	}
	s.mu.Lock()
	s.records = m
	s.mu.Unlock()
}

// Listen (re)starts the server on ip:53 for UDP and TCP.
func (s *Server) Listen(ip string) error {
	addr := net.JoinHostPort(ip, "53")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addr == addr && len(s.servers) > 0 {
		return nil
	}
	for _, srv := range s.servers {
		_ = srv.Shutdown()
	}
	s.servers = nil
	for _, network := range []string{"udp", "tcp"} {
		srv := &dns.Server{Addr: addr, Net: network, Handler: s, ReusePort: true}
		started := make(chan error, 1)
		srv.NotifyStartedFunc = func() { started <- nil }
		go func() {
			if err := srv.ListenAndServe(); err != nil {
				select {
				case started <- err:
				default:
					s.log.Warn("DNS server stopped", "addr", addr, "net", network, "err", err)
				}
			}
		}()
		select {
		case err := <-started:
			if err != nil {
				return err
			}
		case <-time.After(3 * time.Second):
		}
		s.servers = append(s.servers, srv)
	}
	s.addr = addr
	s.log.Info("DNS server listening", "addr", addr)
	return nil
}

// Close stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, srv := range s.servers {
		_ = srv.Shutdown()
	}
	s.servers, s.addr = nil, ""
}

func (s *Server) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) != 1 {
		resp := new(dns.Msg)
		resp.SetRcode(req, dns.RcodeFormatError)
		_ = w.WriteMsg(resp)
		return
	}
	q := req.Question[0]
	name := strings.ToLower(q.Name)
	if name == s.zone || strings.HasSuffix(name, "."+s.zone) {
		_ = w.WriteMsg(s.answer(req, q, name))
		return
	}
	_ = w.WriteMsg(s.forward(req, w.RemoteAddr().Network()))
}

func (s *Server) answer(req *dns.Msg, q dns.Question, name string) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true
	s.mu.RLock()
	ips, ok := s.records[name]
	s.mu.RUnlock()
	if !ok {
		resp.SetRcode(req, dns.RcodeNameError)
		return resp
	}
	if q.Qtype != dns.TypeA && q.Qtype != dns.TypeANY {
		return resp // NODATA: the name exists, but only with A records
	}
	ips = append([]net.IP(nil), ips...)
	rand.Shuffle(len(ips), func(i, j int) { ips[i], ips[j] = ips[j], ips[i] })
	for _, ip := range ips {
		resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl}, A: ip})
	}
	return resp
}

func (s *Server) forward(req *dns.Msg, network string) *dns.Msg {
	c := &dns.Client{Net: network, Timeout: 3 * time.Second}
	if network != "tcp" {
		c.Net = "udp"
	}
	s.mu.RLock()
	ups := s.upstream
	s.mu.RUnlock()
	for _, up := range ups {
		resp, _, err := c.Exchange(req, up)
		if err == nil {
			return resp
		}
	}
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeServerFailure)
	return resp
}
