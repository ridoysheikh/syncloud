// Package acmedns is SynCloud's own ACME challenge server (acme-dns style):
// a small authoritative DNS server that answers the TXT records of DNS-01
// challenges for one zone, so a domain at any registrar can renew wildcard
// certificates with a single CNAME and no DNS provider.
//
// The zone is _acme-challenge.<a-b-c-d>.sslip.io (ForIP): sslip.io already
// delegates every name under it to the IP a-b-c-d, so turning the server on
// needs no NS or A record anywhere, only port 53 open on that IP.
//
// It is not a general name server. It answers TXT, plus the zone's SOA and
// NS, for names under its challenge zone, and refuses everything else.
package acmedns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// SettingKey holds the configuration (JSON) in the settings table.
const SettingKey = "acme_dns"

// DefaultListen is where the server listens (UDP and TCP).
const DefaultListen = ":53"

// Config is the zone the server answers for, the name server host that
// points at it, and the controller's public address. ForIP makes it.
type Config struct {
	Zone string `json:"zone"` // _acme-challenge.203-0-113-9.sslip.io ("" = off)
	NS   string `json:"ns"`   // 203-0-113-9.sslip.io: the host sslip.io delegates the zone to
	IP   string `json:"ip"`   // the controller's public address
	// Pinned: the address was given by hand (NAT): it is not followed.
	Pinned bool `json:"pinned,omitempty"`
}

// SslipSuffix is the wildcard DNS service whose _acme-challenge delegation
// the zone relies on.
const SslipSuffix = "sslip.io"

// ForIP is the configuration for a public address: the zone sslip.io
// delegates to it, with nothing to create at any DNS host.
func ForIP(ip string) Config {
	host := strings.NewReplacer(".", "-", ":", "-").Replace(strings.TrimSpace(ip)) + "." + SslipSuffix
	return Config{Zone: "_acme-challenge." + host, NS: host, IP: strings.TrimSpace(ip)}
}

// IsSslip reports whether a configuration is the sslip.io form of ForIP
// (an older one may be a zone under one of the person's domains).
func IsSslip(c Config) bool {
	f := ForIP(c.IP)
	return c.Zone != "" && c.Zone == f.Zone && c.NS == f.NS
}

// Status is what the server is doing.
type Status struct {
	Config
	Enabled   bool   `json:"enabled"`
	Listen    string `json:"listen"`
	Listening bool   `json:"listening"`
	// Addresses it listens on: Listen itself, or each address that is not
	// loopback when another server (systemd-resolved) holds the port on one.
	Addresses []string `json:"addresses,omitempty"`
	Error     string   `json:"error,omitempty"`
	Records   int      `json:"records"`
}

// Server is the challenge server.
type Server struct {
	st  *store.Store
	log *slog.Logger
	now func() time.Time
	// Listen is the address for UDP and TCP; empty means DefaultListen.
	Listen string

	mu        sync.Mutex
	cfg       Config
	servers   []*mdns.Server // UDP and TCP for each address
	addrs     []string       // where it listens
	listening bool
	err       string
}

// New makes a server that is off until Load or Configure gives it a zone.
func New(st *store.Store, log *slog.Logger, listen string) *Server {
	return &Server{st: st, log: log, now: time.Now, Listen: listen}
}

func (s *Server) listen() string {
	if s.Listen == "" {
		return DefaultListen
	}
	return s.Listen
}

// Load reads the saved configuration and starts listening if a zone is set.
func (s *Server) Load(ctx context.Context) error {
	raw, ok, err := s.st.GetSetting(ctx, SettingKey)
	if err != nil {
		return err
	}
	var c Config
	if ok && json.Unmarshal([]byte(raw), &c) != nil {
		return errors.New("the saved challenge server settings are unreadable")
	}
	if _, err := s.st.PruneAcmeDNSRecords(ctx, s.now().Add(-8*24*time.Hour)); err != nil {
		s.log.Warn("prune challenge records", "err", err)
	}
	s.apply(c)
	return nil
}

// ValidConfig checks a configuration (an empty zone switches the server off).
func ValidConfig(c Config) error {
	if c.Zone == "" {
		return nil
	}
	if err := validName(c.Zone); err != nil {
		return fmt.Errorf("zone: %w", err)
	}
	if err := validName(c.NS); err != nil {
		return fmt.Errorf("ns: %w", err)
	}
	if ip := net.ParseIP(c.IP); ip == nil {
		return fmt.Errorf("ip: %q is not an IP address", c.IP)
	}
	return nil
}

func validName(n string) error {
	if n == "" || len(n) > 253 || strings.Count(n, ".") < 1 || n != strings.ToLower(n) {
		return fmt.Errorf("%q must be a lowercase name like acme.example.com", n)
	}
	for _, l := range strings.Split(n, ".") {
		if l == "" || len(l) > 63 || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return fmt.Errorf("%q has an invalid label", n)
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return fmt.Errorf("%q: only letters, digits, hyphens and underscores", n)
			}
		}
	}
	return nil
}

// Configure saves a configuration and (re)starts or stops the listeners.
// A listen failure does not fail the call: it is shown in the status.
func (s *Server) Configure(ctx context.Context, c Config) error {
	if err := ValidConfig(c); err != nil {
		return err
	}
	if c.Zone == "" {
		if err := s.st.DeleteSetting(ctx, SettingKey); err != nil {
			return err
		}
	} else {
		raw, _ := json.Marshal(c)
		if err := s.st.SetSetting(ctx, SettingKey, string(raw)); err != nil {
			return err
		}
	}
	s.apply(c)
	return nil
}

func (s *Server) apply(c Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.cfg, s.err = c, ""
	if c.Zone == "" {
		return
	}
	listen := s.listen()
	err := s.bindLocked(listen)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		// Every address (":53") cannot be had while something holds the
		// port on one of them: systemd-resolved does on 127.0.0.53 and
		// 127.0.0.54. The internet reaches the addresses that are not
		// loopback, so listen on each of those instead.
		if host, port, perr := net.SplitHostPort(listen); perr == nil && (host == "" || host == "0.0.0.0" || host == "::") {
			err = s.bindEachLocked(port)
		}
	}
	if err != nil {
		s.stopLocked()
		s.err = err.Error()
		s.log.Warn("challenge server", "err", s.err)
		return
	}
	s.listening = true
	s.log.Info("challenge server listening", "zone", c.Zone, "addr", strings.Join(s.addrs, ", "))
}

// bindEachLocked listens on every IPv4 address that is not loopback. It
// fails only when none can be had.
func (s *Server) bindEachLocked(port string) error {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return fmt.Errorf("list the addresses to listen on: %w", err)
	}
	var errs []error
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil || ip.To4() == nil || ip.IsLoopback() {
			continue
		}
		if err := s.bindLocked(net.JoinHostPort(ip.String(), port)); err != nil {
			errs = append(errs, err)
		}
	}
	if len(s.addrs) == 0 {
		if len(errs) == 0 {
			return fmt.Errorf("port %s is in use and this host has no address other than loopback to listen on", port)
		}
		return errors.Join(errs...)
	}
	return nil
}

// bindLocked listens on one address over UDP and TCP.
func (s *Server) bindLocked(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", addr, err)
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("cannot listen on tcp %s: %w", pc.LocalAddr(), err)
	}
	udp := &mdns.Server{PacketConn: pc, Handler: mdns.HandlerFunc(s.serve)}
	tcp := &mdns.Server{Listener: ln, Handler: mdns.HandlerFunc(s.serve)}
	started := make(chan struct{}, 2)
	udp.NotifyStartedFunc = func() { started <- struct{}{} }
	tcp.NotifyStartedFunc = func() { started <- struct{}{} }
	go func() { _ = udp.ActivateAndServe() }()
	go func() { _ = tcp.ActivateAndServe() }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
		}
	}
	s.servers = append(s.servers, udp, tcp)
	s.addrs = append(s.addrs, pc.LocalAddr().String())
	return nil
}

func (s *Server) stopLocked() {
	for _, srv := range s.servers {
		_ = srv.Shutdown()
	}
	s.servers, s.addrs, s.listening = nil, nil, false
}

// Close stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// Addr is the first address the server listens on ("" when it does not).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.addrs) == 0 {
		return ""
	}
	return s.addrs[0]
}

// Enabled reports whether a zone is configured (the server may still have
// failed to listen: Status says so).
func (s *Server) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Zone != ""
}

// Zone is the challenge zone ("" when off).
func (s *Server) Zone() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Zone
}

// Config is the current configuration.
func (s *Server) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Status describes the server.
func (s *Server) Status(ctx context.Context) Status {
	s.mu.Lock()
	st := Status{Config: s.cfg, Enabled: s.cfg.Zone != "", Listen: s.listen(), Listening: s.listening, Error: s.err, Addresses: append([]string(nil), s.addrs...)}
	s.mu.Unlock()
	if n, err := s.st.CountAcmeDNSRecords(ctx); err == nil {
		st.Records = n
	}
	return st
}

// ErrOff means the server has no zone.
var ErrOff = errors.New("the challenge server is off")

func (s *Server) inZone(name string) (string, error) {
	zone := s.Zone()
	if zone == "" {
		return "", ErrOff
	}
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	// The apex too: it is the challenge name of *.<ip>.sslip.io itself.
	if name != zone && !strings.HasSuffix(name, "."+zone) {
		return "", fmt.Errorf("%q is not a name inside the challenge zone %s", name, zone)
	}
	return name, nil
}

// Put stores a TXT value to answer at a name inside the zone and returns
// what to hand to Delete.
func (s *Server) Put(ctx context.Context, name, value string) (string, error) {
	n, err := s.inZone(name)
	if err != nil {
		return "", err
	}
	if value == "" || len(value) > 255 || strings.Contains(value, "|") {
		return "", errors.New("a challenge value is 1 to 255 characters")
	}
	if err := s.st.PutAcmeDNSRecord(ctx, n, value, s.now().UTC()); err != nil {
		return "", err
	}
	return n + "|" + value, nil
}

// Delete removes a value stored by Put.
func (s *Server) Delete(ctx context.Context, id string) error {
	n, v, ok := strings.Cut(id, "|")
	if !ok {
		return nil
	}
	return s.st.DeleteAcmeDNSRecord(ctx, n, v)
}

// serve answers one query.
func (s *Server) serve(w mdns.ResponseWriter, q *mdns.Msg) {
	m := new(mdns.Msg)
	m.SetReply(q)
	cfg := s.Config()
	if len(q.Question) != 1 || cfg.Zone == "" {
		m.Rcode = mdns.RcodeRefused
		_ = w.WriteMsg(m)
		return
	}
	qn := strings.ToLower(strings.TrimSuffix(q.Question[0].Name, "."))
	if qn != cfg.Zone && !strings.HasSuffix(qn, "."+cfg.Zone) {
		m.Rcode = mdns.RcodeRefused // not ours: never an open resolver
		_ = w.WriteMsg(m)
		return
	}
	m.Authoritative = true
	hdr := func(t uint16, ttl uint32) mdns.RR_Header {
		return mdns.RR_Header{Name: q.Question[0].Name, Rrtype: t, Class: mdns.ClassINET, Ttl: ttl}
	}
	soa := &mdns.SOA{Hdr: mdns.RR_Header{Name: mdns.Fqdn(cfg.Zone), Rrtype: mdns.TypeSOA, Class: mdns.ClassINET, Ttl: 60},
		Ns: mdns.Fqdn(cfg.NS), Mbox: mdns.Fqdn("hostmaster." + cfg.Zone), Serial: uint32(s.now().Unix()), Refresh: 3600, Retry: 600, Expire: 86400, Minttl: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	switch q.Question[0].Qtype {
	case mdns.TypeTXT:
		vals, err := s.st.AcmeDNSValues(ctx, qn)
		if err != nil {
			m.Rcode = mdns.RcodeServerFailure
			break
		}
		for _, v := range vals {
			m.Answer = append(m.Answer, &mdns.TXT{Hdr: hdr(mdns.TypeTXT, 1), Txt: []string{v}})
		}
		if len(vals) == 0 {
			m.Ns = append(m.Ns, soa)
			if qn != cfg.Zone {
				m.Rcode = mdns.RcodeNameError
			}
		}
	case mdns.TypeNS:
		if qn == cfg.Zone {
			m.Answer = append(m.Answer, &mdns.NS{Hdr: hdr(mdns.TypeNS, 300), Ns: mdns.Fqdn(cfg.NS)})
		} else {
			m.Ns = append(m.Ns, soa)
		}
	case mdns.TypeSOA:
		if qn == cfg.Zone {
			m.Answer = append(m.Answer, soa)
		} else {
			m.Ns = append(m.Ns, soa)
		}
	default:
		m.Ns = append(m.Ns, soa)
	}
	_ = w.WriteMsg(m)
}
