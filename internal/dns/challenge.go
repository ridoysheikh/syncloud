package dns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/certs"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// The _acme-challenge name (Phase 28): where the TXT record of a DNS-01
// challenge goes, how a domain without a provider can still renew by
// itself, and what the person has to do once.
//
//	direct     the domain has a provider: the TXT is written at the name.
//	delegated  _acme-challenge.<name> is a CNAME to a name in another domain
//	           whose provider SynCloud can write: the TXT is written there,
//	           the certificate authority follows the CNAME. One CNAME, set
//	           once, covers the name and its wildcard for ever.
//	builtin    SynCloud's own challenge server answers: the name is a CNAME
//	           to a name in its challenge zone (_acme-challenge.<ip>.sslip.io,
//	           which sslip.io delegates to the controller), and the TXT is
//	           stored there. No provider is needed, and it is how a domain
//	           without one renews.
//	manual     nothing can be written: the person adds each TXT record.

// AcmePrefix is the label every DNS-01 challenge lives under.
const AcmePrefix = "_acme-challenge."

// ChallengeName is the name the authority looks up for a host.
func ChallengeName(host string) string {
	h := strings.TrimPrefix(Fqdn(host), "*.")
	if strings.HasPrefix(h, AcmePrefix) {
		return h
	}
	return AcmePrefix + h
}

// DelegationTarget is the name a challenge name is delegated to inside the
// validation domain: stable per name, one label, so every name gets its own.
func DelegationTarget(challengeName, targetZone string) string {
	sum := sha256.Sum256([]byte(ChallengeName(challengeName)))
	return hex.EncodeToString(sum[:6]) + ".acme." + Fqdn(targetZone)
}

// isDelegationTarget reports whether a name looks like where a challenge is
// delegated to (see DelegationTarget): its TXT records are challenge leftovers.
func isDelegationTarget(name string) bool {
	label, rest, ok := strings.Cut(name, ".acme.")
	if !ok || rest == "" || len(label) != 12 {
		return false
	}
	_, err := hex.DecodeString(label)
	return err == nil
}

// BuiltinTarget is the name in the challenge server's zone a challenge name is delegated to.
func BuiltinTarget(challengeName, zone string) string {
	sum := sha256.Sum256([]byte(ChallengeName(challengeName)))
	return hex.EncodeToString(sum[:6]) + "." + Fqdn(zone)
}

// cnameBased reports whether a mode is answered through a CNAME.
func cnameBased(mode string) bool { return mode == "delegated" || mode == "builtin" }

// CNAMEState says whether the delegating CNAME is in place.
type CNAMEState struct {
	Name    string   `json:"name"`
	Target  string   `json:"target"`
	Present bool     `json:"present"`        // public resolvers answer with the target
	Seen    []string `json:"seen,omitempty"` // what they answer instead
	// Managed: SynCloud creates and keeps the CNAME (the domain has a provider).
	Managed bool `json:"managed"`
}

// ChallengeRoute is where the challenge for a host is answered.
type ChallengeRoute struct {
	Name       string      `json:"name"`           // _acme-challenge.<host>: what the authority asks for
	Host       string      `json:"host"`           // the host or domain it proves
	Zone       string      `json:"zone,omitempty"` // the registered domain, empty outside every one
	Mode       string      `json:"mode"`           // direct | delegated | builtin | manual
	Auto       bool        `json:"auto"`           // SynCloud writes the TXT record
	Target     string      `json:"target,omitempty"`
	TargetZone string      `json:"targetZone,omitempty"`
	CNAME      *CNAMEState `json:"cname,omitempty"`
	Note       string      `json:"note,omitempty"`
}

// Route decides where the TXT record of a host's challenge goes. It asks the
// provider only to follow a CNAME the person already made.
func (s *Service) Route(ctx context.Context, host string) ChallengeRoute {
	name := ChallengeName(host)
	r := ChallengeRoute{Name: name, Host: strings.TrimPrefix(name, AcmePrefix), Mode: "manual"}
	// The controller's own *.<ip>.sslip.io: sslip.io hands its challenge name
	// to this address, so the server answers it directly, with no CNAME.
	if b := s.Builtin; b != nil && b.Enabled() && name == b.Zone() {
		r.Mode, r.Auto, r.TargetZone, r.Target = "builtin", true, b.Zone(), name
		r.Note = "sslip.io hands this name to SynCloud's challenge server: nothing to create"
		return r
	}
	z, ok := s.ZoneFor(ctx, name)
	if !ok {
		if b := s.Builtin; b != nil && b.Enabled() {
			r.Mode, r.Auto, r.TargetZone, r.Target = "builtin", true, b.Zone(), BuiltinTarget(name, b.Zone())
			return r
		}
		r.Note = "the name is outside every registered domain: add the TXT record at your DNS host"
		return r
	}
	r.Zone = z.Name
	if z.ChallengeZone != "" {
		tz, err := s.st.Zone(ctx, z.ChallengeZone)
		switch {
		case err != nil:
			r.Note = "the validation domain " + z.ChallengeZone + " is no longer registered"
		case tz.ProviderID == "":
			r.Note = "the validation domain " + z.ChallengeZone + " has no DNS provider connected"
		default:
			r.Mode, r.Auto, r.TargetZone = "delegated", true, tz.Name
			r.Target = DelegationTarget(name, tz.Name)
			return r
		}
	}
	// SynCloud's own server: chosen for the domain, or used because there is no provider.
	if b := s.Builtin; b != nil && b.Enabled() && (z.ChallengeBuiltin || z.ProviderID == "") {
		r.Mode, r.Auto, r.TargetZone, r.Target = "builtin", true, b.Zone(), BuiltinTarget(name, b.Zone())
		return r
	}
	if z.ProviderID == "" {
		if r.Note == "" {
			r.Note = "this domain has no DNS provider: add the TXT record by hand, delegate " + name + " to a domain that has one, or turn on SynCloud's own challenge server (one click)"
		}
		return r
	}
	// A provider: write the TXT at the name, unless somebody made it a CNAME
	// (a TXT cannot sit beside one): then the challenge lives where it points.
	if to, found := s.cnameAt(ctx, z, name); found {
		if b := s.Builtin; b != nil && b.Enabled() && InZone(to, b.Zone()) && to != b.Zone() {
			r.Mode, r.Auto, r.Target, r.TargetZone = "builtin", true, to, b.Zone()
			r.Note = "the name is already a CNAME to SynCloud's challenge server"
			return r
		}
		if tz, ok := s.ZoneFor(ctx, to); ok && tz.ProviderID != "" {
			r.Mode, r.Auto, r.Target, r.TargetZone = "delegated", true, to, tz.Name
			r.Note = "the name is already a CNAME to " + to + ": the record is written there"
			return r
		}
		r.Note = name + " is a CNAME to " + to + ", outside the domains SynCloud can write: add the TXT record at " + to
		return r
	}
	r.Mode, r.Auto = "direct", true
	return r
}

// cnameAt returns where a name's CNAME points in the zone's provider, if it is one.
func (s *Service) cnameAt(ctx context.Context, z store.Zone, name string) (string, bool) {
	p, err := s.provider(ctx, z.ProviderID)
	if err != nil {
		return "", false
	}
	rs, err := p.Records(ctx, z.ProviderZoneID)
	if err != nil {
		return "", false
	}
	for _, r := range rs {
		if normType(r.Type) == "CNAME" && Fqdn(r.Name) == name {
			return Fqdn(r.Content), true
		}
	}
	return "", false
}

// Challenge is Route plus what public resolvers show of the delegating CNAME.
func (s *Service) Challenge(ctx context.Context, host string) ChallengeRoute {
	r := s.Route(ctx, host)
	if !cnameBased(r.Mode) || r.Target == r.Name { // answered at the name itself: no CNAME
		return r
	}
	z, _ := s.ZoneFor(ctx, r.Name)
	r.CNAME = &CNAMEState{Name: r.Name, Target: r.Target, Managed: z.ProviderID != ""}
	r.CNAME.Present, r.CNAME.Seen = s.cnameVisible(ctx, r.Name, r.Target)
	return r
}

// cnameVisible reports whether every public resolver that answers shows the CNAME.
func (s *Service) cnameVisible(ctx context.Context, name, target string) (bool, []string) {
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	asked, ok := 0, 0
	var seen []string
	for _, rv := range s.LookupResolvers() {
		a := Query(cctx, rv, name, "CNAME")
		if a.Error != "" {
			continue
		}
		asked++
		if slices.Contains(a.Values, Fqdn(target)) {
			ok++
			continue
		}
		for _, v := range a.Values {
			if !slices.Contains(seen, v) {
				seen = append(seen, v)
			}
		}
	}
	return asked > 0 && ok == asked, seen
}

// EnsureDelegation creates the CNAME of a delegated name when the domain has
// a provider (a manual domain's CNAME is made by hand). It keeps the CNAME:
// it is set once. Somebody else's records on the name are left alone unless
// replace is true.
func (s *Service) EnsureDelegation(ctx context.Context, host string, replace bool) (ChallengeRoute, error) {
	r := s.Route(ctx, host)
	if !cnameBased(r.Mode) {
		return r, invalid("%s is not delegated: set the validation domain of %s, or turn on the challenge server, first", r.Name, firstNonEmpty(r.Zone, r.Host))
	}
	z, ok := s.ZoneFor(ctx, r.Name)
	if !ok || z.ProviderID == "" {
		return s.Challenge(ctx, host), nil // a manual domain: the CNAME is made by hand
	}
	_, p, err := s.zoneProvider(ctx, z.Name)
	if err != nil {
		return r, err
	}
	have, err := p.Records(ctx, z.ProviderZoneID)
	if err != nil {
		return r, err
	}
	want := []Want{{Type: "CNAME", Name: r.Name, Content: r.Target, TTL: 0, Exclusive: true,
		Owner: Owner{Kind: "challenge-delegation", ID: r.Name, Purpose: "acme delegation"}}}
	pl := Plan(have, want, s.managedSet(ctx, z))
	if _, err := Apply(ctx, p, z.ProviderZoneID, pl, replace, &tracker{s: s, ctx: ctx, zone: z}); err != nil {
		return r, err
	}
	if c := pl.Conflicts(); len(c) > 0 && !replace {
		return s.Challenge(ctx, host), &ConflictError{Msg: fmt.Sprintf("%s already has other records: replacing them needs confirmation", r.Name), Record: &c[0].Record}
	}
	return s.Challenge(ctx, host), nil
}

// RemoveDelegation removes the CNAME of a delegated name that the platform
// created (when the validation domain is cleared, or the name no longer
// needs it).
func (s *Service) removeDelegation(ctx context.Context, z store.Zone) {
	if z.ProviderID == "" {
		return
	}
	ms, err := s.st.ListManagedRecords(ctx, z.ProviderID, z.Name)
	if err != nil {
		return
	}
	p, err := s.provider(ctx, z.ProviderID)
	if err != nil {
		return
	}
	for _, m := range ms {
		if m.OwnerKind != "challenge-delegation" {
			continue
		}
		if err := p.Delete(ctx, z.ProviderZoneID, m.RecordID); err != nil {
			var pe *Error
			if !errors.As(err, &pe) || (pe.Status != 404 && pe.Status != 400) {
				s.log.Warn("remove challenge delegation", "name", m.Name, "err", err)
				continue
			}
		}
		_ = s.st.DeleteManagedRecord(ctx, z.ProviderID, m.RecordID)
	}
}

// ── what the certificates ask ───────────────────────────────────────────────

// Challenge adapts the route for the certificates.
func (d CertHost) Challenge(ctx context.Context, name string) certs.ChallengeInfo {
	r := d.Service.Challenge(ctx, name)
	info := certs.ChallengeInfo{Mode: r.Mode, Auto: r.Auto, Target: r.Target}
	if r.CNAME != nil {
		info.CNAMEOK, info.CNAMEManaged = r.CNAME.Present, r.CNAME.Managed
	} else if cnameBased(r.Mode) {
		info.CNAMEOK = true // answered at the name itself (the controller's own sslip.io name)
	}
	return info
}

// EnsureDelegation makes the CNAME when SynCloud can, without replacing records of others.
func (d CertHost) EnsureDelegation(ctx context.Context, name string) error {
	_, err := d.Service.EnsureDelegation(ctx, name, false)
	return err
}
