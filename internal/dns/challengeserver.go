package dns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/acmedns"
)

// NeededRecord is a record to create at the DNS of the domain the challenge
// zone sits under, so that the internet reaches SynCloud's challenge server.
type NeededRecord struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	// Zone is the registered domain the record belongs in ("" when none of yours).
	Zone string `json:"zone,omitempty"`
	// Managed: that domain has a provider, so SynCloud can create the record.
	Managed bool `json:"managed"`
}

// ServerView is the challenge server as shown.
type ServerView struct {
	acmedns.Status
	Needed []NeededRecord `json:"needed"`
	// Domains are the registered domains whose challenges it answers.
	Domains []string `json:"domains"`
}

// DefaultNS names the name server host of a challenge zone: acme.example.com → acme-ns.example.com.
func DefaultNS(zone string) string {
	first, rest, ok := strings.Cut(Fqdn(zone), ".")
	if !ok || rest == "" {
		return ""
	}
	return first + "-ns." + rest
}

func (s *Service) needed(ctx context.Context, c acmedns.Config) []NeededRecord {
	if c.Zone == "" {
		return []NeededRecord{}
	}
	mk := func(typ, name, content string) NeededRecord {
		r := NeededRecord{Type: typ, Name: name, Content: content}
		if z, ok := s.ZoneFor(ctx, name); ok {
			r.Zone, r.Managed = z.Name, z.ProviderID != ""
		}
		return r
	}
	return []NeededRecord{mk("A", c.NS, c.IP), mk("NS", c.Zone, c.NS)}
}

// ChallengeServer shows SynCloud's own challenge server.
func (s *Service) ChallengeServer(ctx context.Context) (ServerView, error) {
	if s.Builtin == nil {
		return ServerView{Needed: []NeededRecord{}, Domains: []string{}}, nil
	}
	st := s.Builtin.Status(ctx)
	v := ServerView{Status: st, Needed: s.needed(ctx, st.Config), Domains: []string{}}
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

// ConfigureChallengeServer sets the challenge zone (empty switches the server
// off). ns defaults from the zone; ip must be given (the API fills it with the
// controller's address).
func (s *Service) ConfigureChallengeServer(ctx context.Context, zone, ns, ip string) (ServerView, error) {
	if s.Builtin == nil {
		return ServerView{}, invalid("the challenge server is not available in this build")
	}
	zone, ns, ip = Fqdn(zone), Fqdn(ns), strings.TrimSpace(ip)
	if zone != "" && ns == "" {
		ns = DefaultNS(zone)
	}
	if zone != "" {
		// It must not be a registered domain's own name or above one: that zone's DNS is somebody else's.
		zs, _ := s.st.ListZones(ctx)
		for _, z := range zs {
			if z.Name == zone || InZone(z.Name, zone) {
				return ServerView{}, invalid("zone: %s is a registered domain; use a name below one, like acme.%s", zone, z.Name)
			}
		}
	}
	if err := s.Builtin.Configure(ctx, acmedns.Config{Zone: zone, NS: ns, IP: ip}); err != nil {
		return ServerView{}, invalid("%v", err)
	}
	s.changed()
	return s.ChallengeServer(ctx)
}

// CreateChallengeServerRecords makes the records that point the internet at
// the server, in the domains that have a provider. Others are left to you.
func (s *Service) CreateChallengeServerRecords(ctx context.Context, replace bool) (int, error) {
	if s.Builtin == nil || !s.Builtin.Enabled() {
		return 0, invalid("set up the challenge server first")
	}
	made := 0
	for _, n := range s.needed(ctx, s.Builtin.Config()) {
		if !n.Managed {
			continue
		}
		z, p, err := s.zoneProvider(ctx, n.Zone)
		if err != nil {
			return made, err
		}
		have, err := p.Records(ctx, z.ProviderZoneID)
		if err != nil {
			return made, err
		}
		w := Want{Type: n.Type, Name: n.Name, Content: n.Content, Exclusive: n.Type == "A", Owner: Owner{Kind: "challenge-server", ID: n.Name, Purpose: "acme dns"}}
		pl := Plan(have, []Want{w}, s.managedSet(ctx, z))
		got, err := Apply(ctx, p, z.ProviderZoneID, pl, replace, &tracker{s: s, ctx: ctx, zone: z})
		if err != nil {
			return made, err
		}
		made += got.Created
		if c := pl.Conflicts(); len(c) > 0 && !replace {
			return made, &ConflictError{Msg: fmt.Sprintf("%s already has other records: replacing them needs confirmation", n.Name), Record: &c[0].Record}
		}
	}
	return made, nil
}

// Check is one result of testing the challenge server.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// CheckChallengeServer tests the server from the outside, as far as it can:
// it listens, the zone's NS record and its glue are visible, and a probe TXT
// value comes back through public resolvers (which proves port 53 is open).
func (s *Service) CheckChallengeServer(ctx context.Context) ([]Check, error) {
	if s.Builtin == nil || !s.Builtin.Enabled() {
		return nil, invalid("set up the challenge server first")
	}
	st := s.Builtin.Status(ctx)
	out := []Check{}
	add := func(name string, ok bool, detail string) {
		out = append(out, Check{Name: name, OK: ok, Detail: detail})
	}

	if st.Listening {
		add("Listening", true, "UDP and TCP on "+st.Listen)
	} else {
		add("Listening", false, firstNonEmpty(st.Error, "not listening on "+st.Listen))
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	resolvers := s.LookupResolvers()

	seenNS := map[string]bool{}
	for _, r := range resolvers {
		for _, v := range Query(cctx, r, st.Zone, "NS").Values {
			seenNS[v] = true
		}
	}
	nsList := make([]string, 0, len(seenNS))
	for v := range seenNS {
		nsList = append(nsList, v)
	}
	slices.Sort(nsList)
	switch {
	case seenNS[st.NS]:
		add("NS record", true, st.Zone+" is delegated to "+st.NS)
	case len(nsList) > 0:
		add("NS record", false, st.Zone+" is delegated to "+strings.Join(nsList, ", ")+", not "+st.NS)
	default:
		add("NS record", false, "resolvers do not know "+st.Zone+" yet: create the NS record (it can take a few minutes to appear)")
	}

	var ips []string
	for _, r := range resolvers {
		for _, v := range Query(cctx, r, st.NS, "A").Values {
			if !slices.Contains(ips, v) {
				ips = append(ips, v)
			}
		}
	}
	switch {
	case slices.Contains(ips, st.IP):
		add("Name server address", true, st.NS+" → "+st.IP)
	case len(ips) > 0:
		add("Name server address", false, st.NS+" points at "+strings.Join(ips, ", ")+", not "+st.IP)
	default:
		add("Name server address", false, "no A record for "+st.NS+" yet")
	}

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
		add("Reachable from the internet", true, "a probe record came back through public resolvers, so port 53 is open and the delegation works")
	case !seenNS[st.NS] || !slices.Contains(ips, st.IP):
		add("Reachable from the internet", false, "not tested through resolvers yet: fix the records above first")
	default:
		add("Reachable from the internet", false, "resolvers did not get an answer from "+net.JoinHostPort(st.IP, "53")+": check that UDP and TCP port 53 reach the controller (firewall, and nothing else using the port)")
	}
	return out, nil
}
