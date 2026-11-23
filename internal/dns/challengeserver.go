package dns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/acmedns"
)

// ServerView is the challenge server as shown.
type ServerView struct {
	acmedns.Status
	// Sslip: the zone is the sslip.io one for the address (nothing to create
	// at any DNS host); false for an older zone under one of your domains.
	Sslip bool `json:"sslip"`
	// Domains are the registered domains whose challenges it answers.
	Domains []string `json:"domains"`
}

// ChallengeServer shows SynCloud's own challenge server.
func (s *Service) ChallengeServer(ctx context.Context) (ServerView, error) {
	if s.Builtin == nil {
		return ServerView{Domains: []string{}}, nil
	}
	st := s.Builtin.Status(ctx)
	v := ServerView{Status: st, Sslip: acmedns.IsSslip(st.Config), Domains: []string{}}
	if st.Enabled {
		zs, err := s.st.ListZones(ctx)
		if err != nil {
			return v, err
		}
		for _, z := range zs {
			if z.ChallengeZone == "" && (z.ChallengeBuiltin || z.ProviderID == "") {
				v.Domains = append(v.Domains, z.Name)
			}
		}
	}
	return v, nil
}

// ConfigureChallengeServer turns the server on for the controller's public
// address (the zone sslip.io delegates to it: _acme-challenge.<ip>.sslip.io)
// or off. Nothing has to be created at any DNS host.
//
// pinned: the address was given by hand, so FollowChallengeServerIP keeps it.
func (s *Service) ConfigureChallengeServer(ctx context.Context, enabled bool, ip string, pinned bool) (ServerView, error) {
	if s.Builtin == nil {
		return ServerView{}, invalid("the challenge server is not available in this build")
	}
	c := acmedns.Config{}
	if enabled {
		ip = strings.TrimSpace(ip)
		a := net.ParseIP(ip)
		if a == nil || a.To4() == nil {
			return ServerView{}, invalid("ip: this controller's public IPv4 address is needed (%q)", ip)
		}
		c = acmedns.ForIP(a.String())
		c.Pinned = pinned
	}
	if err := s.Builtin.Configure(ctx, c); err != nil {
		return ServerView{}, invalid("%v", err)
	}
	s.changed()
	return s.ChallengeServer(ctx)
}

// FollowChallengeServerIP moves the sslip.io zone to a new public address.
// The domains' CNAMEs then point at the old zone: their Challenge tabs show
// the new target. An older zone under one of your domains is left alone.
func (s *Service) FollowChallengeServerIP(ctx context.Context, ip string) (bool, error) {
	if s.Builtin == nil || !s.Builtin.Enabled() {
		return false, nil
	}
	cur := s.Builtin.Config()
	a := net.ParseIP(strings.TrimSpace(ip))
	if !acmedns.IsSslip(cur) || cur.Pinned || a == nil || a.To4() == nil || a.String() == cur.IP {
		return false, nil
	}
	if err := s.Builtin.Configure(ctx, acmedns.ForIP(a.String())); err != nil {
		return false, err
	}
	s.changed()
	return true, nil
}

// Check is one result of testing the challenge server.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// CheckChallengeServer tests the server from the outside: it listens, and a
// probe TXT value comes back through public resolvers (which proves the
// delegation reaches this address and port 53 is open).
func (s *Service) CheckChallengeServer(ctx context.Context) ([]Check, error) {
	if s.Builtin == nil || !s.Builtin.Enabled() {
		return nil, invalid("turn the challenge server on first")
	}
	st := s.Builtin.Status(ctx)
	out := []Check{}
	add := func(name string, ok bool, detail string) {
		out = append(out, Check{Name: name, OK: ok, Detail: detail})
	}

	if st.Listening {
		add("Listening", true, "UDP and TCP on "+strings.Join(firstNonEmptyList(st.Addresses, []string{st.Listen}), ", "))
	} else {
		add("Listening", false, firstNonEmpty(st.Error, "not listening on "+st.Listen))
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	resolvers := s.LookupResolvers()

	// A probe value that a resolver can only know by asking our server over the internet.
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	probe := "probe-" + hex.EncodeToString(buf) + "." + st.Zone
	val := "syncloud-probe-" + hex.EncodeToString(buf)
	id, err := s.Builtin.Put(ctx, probe, val)
	if err != nil {
		add("Reachable from the internet", false, err.Error())
		return out, nil
	}
	defer func() { _ = s.Builtin.Delete(context.Background(), id) }()
	var herr error
	missing := []string{val}
	for i := 0; i < 3 && len(missing) > 0; i++ {
		if missing, herr = HasTXT(cctx, resolvers, probe, []string{val}); herr != nil || len(missing) == 0 {
			break
		}
		select {
		case <-cctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	switch {
	case herr == nil && len(missing) == 0:
		add("Reachable from the internet", true, "a probe record came back through public resolvers: the delegation reaches "+st.IP+" and port 53 is open")
	case !st.Listening:
		add("Reachable from the internet", false, "not tested: the server is not listening")
	default:
		add("Reachable from the internet", false, "resolvers did not get an answer from "+net.JoinHostPort(st.IP, "53")+" through "+st.NS+": check that UDP and TCP port 53 reach the controller (cloud firewall, and nothing else using the port) and that "+st.IP+" is still its public address")
	}
	return out, nil
}

func firstNonEmptyList(a, b []string) []string {
	if len(a) > 0 {
		return a
	}
	return b
}
