package dns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/acmedns"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Errors a caller maps to HTTP statuses.
var (
	ErrNotFound   = errors.New("not found")
	ErrNotGranted = errors.New("this project may not use that name")
	ErrInUse      = errors.New("in use")
	ErrInvalid    = errors.New("invalid")
	ErrConflict   = errors.New("conflict")
)

type invalidError struct{ msg string }

func (e invalidError) Error() string        { return e.msg }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, a ...any) error { return invalidError{fmt.Sprintf(format, a...)} }

// ConflictError carries the record that is in the way.
type ConflictError struct {
	Msg    string
	Record *Record
}

func (e *ConflictError) Error() string        { return e.Msg }
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// ProviderView is a provider as shown: never the credentials.
type ProviderView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	LastError string    `json:"lastError,omitempty"`
	Domains   int       `json:"domains"`
	CreatedAt time.Time `json:"createdAt"`
}

// ZoneView is a registered domain as shown.
type ZoneView struct {
	Name           string   `json:"name"`
	Mode           string   `json:"mode"` // managed | manual
	ProviderID     string   `json:"providerId,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	NameServers    []string `json:"nameServers,omitempty"`
	WildcardPolicy string   `json:"wildcardPolicy"`
	AuthorityID    string   `json:"authorityId,omitempty"`
	// ChallengeZone is the domain whose provider holds this domain's
	// _acme-challenge records (a CNAME delegates them there).
	ChallengeZone string `json:"challengeZone,omitempty"`
	// ChallengeBuiltin: answered by SynCloud's own server although the domain has a provider.
	ChallengeBuiltin bool              `json:"challengeBuiltin,omitempty"`
	Grants           []store.ZoneGrant `json:"grants"`
	CreatedAt        time.Time         `json:"createdAt"`
}

// RemoteZone is a zone a provider can manage.
type RemoteZone struct {
	ZoneInfo
	Registered bool `json:"registered"`
}

type providerConfig struct {
	Token string `json:"token"`
}

// Service is the domains and DNS manager.
type Service struct {
	st  *store.Store
	box *secrets.Box
	log *slog.Logger
	now func() time.Time

	// CloudflareBase overrides Cloudflare's API address (tests, a lab).
	CloudflareBase string
	HTTPClient     *http.Client
	// Resolvers answer lookups and the challenge checks (default: public ones).
	Resolvers []string
	// Factory builds a provider; tests replace it.
	Factory func(typ, token string) (Provider, error)
	// Builtin is SynCloud's own challenge server (nil in tests that do not need it).
	Builtin *acmedns.Server
	// OnChange runs after a domain is added, changed or removed.
	OnChange func()

	mu    sync.Mutex
	cache map[string]cachedProvider
}

type cachedProvider struct {
	p     Provider
	token string
}

func NewService(st *store.Store, box *secrets.Box, log *slog.Logger) *Service {
	s := &Service{st: st, box: box, log: log, now: time.Now, cache: map[string]cachedProvider{}}
	s.Factory = s.build
	return s
}

func (s *Service) build(typ, token string) (Provider, error) {
	switch typ {
	case "cloudflare":
		return NewCloudflare(token, s.CloudflareBase, s.HTTPClient), nil
	}
	return nil, invalid("unknown DNS provider type %q (cloudflare)", typ)
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// ── providers ───────────────────────────────────────────────────────────────

func (s *Service) provider(ctx context.Context, id string) (Provider, error) {
	rec, err := s.st.DNSProvider(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	raw, err := s.box.Open(rec.ConfigEnc, []byte("dnsprov:"+rec.ID))
	if err != nil {
		return nil, fmt.Errorf("the provider's credentials cannot be read: %w", err)
	}
	var cfg providerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[id]; ok && c.token == cfg.Token {
		return c.p, nil
	}
	p, err := s.Factory(rec.Type, cfg.Token)
	if err != nil {
		return nil, err
	}
	s.cache[id] = cachedProvider{p, cfg.Token}
	return p, nil
}

func (s *Service) ListProviders(ctx context.Context) ([]ProviderView, error) {
	ps, err := s.st.ListDNSProviders(ctx)
	if err != nil {
		return nil, err
	}
	zs, err := s.st.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	n := map[string]int{}
	for _, z := range zs {
		n[z.ProviderID]++
	}
	out := make([]ProviderView, 0, len(ps))
	for _, p := range ps {
		out = append(out, ProviderView{ID: p.ID, Name: p.Name, Type: p.Type, Status: p.Status, LastError: p.LastError, Domains: n[p.ID], CreatedAt: p.CreatedAt})
	}
	return out, nil
}

// AddProvider connects a provider after checking that its token works.
func (s *Service) AddProvider(ctx context.Context, name, typ, token string) (ProviderView, error) {
	name, typ, token = strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(typ)), strings.TrimSpace(token)
	if name == "" || len(name) > 60 {
		return ProviderView{}, invalid("name: 1 to 60 characters")
	}
	if token == "" {
		return ProviderView{}, invalid("token: the provider's API token is required")
	}
	p, err := s.Factory(typ, token)
	if err != nil {
		return ProviderView{}, err
	}
	if err := p.Verify(ctx); err != nil {
		return ProviderView{}, invalid("the token was refused: %v", err)
	}
	id := newID("dnsp_")
	raw, _ := json.Marshal(providerConfig{Token: token})
	rec := store.DNSProvider{ID: id, Name: name, Type: typ, ConfigEnc: s.box.Seal(raw, []byte("dnsprov:"+id)), Status: "ok", CreatedAt: s.now().UTC()}
	if err := s.st.PutDNSProvider(ctx, rec); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ProviderView{}, &ConflictError{Msg: "a provider named " + name + " exists"}
		}
		return ProviderView{}, err
	}
	return ProviderView{ID: id, Name: name, Type: typ, Status: "ok", CreatedAt: rec.CreatedAt}, nil
}

// ReplaceToken sends a new token for a provider (the old one is never shown).
func (s *Service) ReplaceToken(ctx context.Context, id, token string) error {
	rec, err := s.st.DNSProvider(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	p, err := s.Factory(rec.Type, strings.TrimSpace(token))
	if err != nil {
		return err
	}
	if err := p.Verify(ctx); err != nil {
		return invalid("the token was refused: %v", err)
	}
	raw, _ := json.Marshal(providerConfig{Token: strings.TrimSpace(token)})
	rec.ConfigEnc, rec.Status, rec.LastError = s.box.Seal(raw, []byte("dnsprov:"+id)), "ok", ""
	return s.st.PutDNSProvider(ctx, rec)
}

// SyncProvider re-checks the token and refreshes its domains' name servers.
func (s *Service) SyncProvider(ctx context.Context, id string) (ProviderView, error) {
	p, err := s.provider(ctx, id)
	if err != nil {
		return ProviderView{}, err
	}
	status, msg := "ok", ""
	if err := p.Verify(ctx); err != nil {
		status, msg = "error", err.Error()
	}
	if err := s.st.SetDNSProviderStatus(ctx, id, status, msg); err != nil {
		return ProviderView{}, err
	}
	vs, err := s.ListProviders(ctx)
	if err != nil {
		return ProviderView{}, err
	}
	for _, v := range vs {
		if v.ID == id {
			return v, nil
		}
	}
	return ProviderView{}, ErrNotFound
}

// RemoveProvider disconnects a provider. Its domains stay, as manual ones,
// and certificates are kept.
func (s *Service) RemoveProvider(ctx context.Context, id string) error {
	if _, err := s.st.DNSProvider(ctx, id); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
	if err := s.st.DeleteDNSProvider(ctx, id); err != nil {
		return err
	}
	s.changed()
	return nil
}

// ProviderZones lists the zones a provider can manage and which are registered.
func (s *Service) ProviderZones(ctx context.Context, id string) ([]RemoteZone, error) {
	p, err := s.provider(ctx, id)
	if err != nil {
		return nil, err
	}
	zones, err := p.Zones(ctx)
	if err != nil {
		return nil, err
	}
	have, err := s.st.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	reg := map[string]bool{}
	for _, z := range have {
		reg[z.Name] = true
	}
	out := make([]RemoteZone, 0, len(zones))
	for _, z := range zones {
		out = append(out, RemoteZone{ZoneInfo: z, Registered: reg[z.Name]})
	}
	return out, nil
}

// ── domains ─────────────────────────────────────────────────────────────────

// ValidZoneName checks a registered domain's name: lowercase labels, at
// least two of them, no wildcard.
func ValidZoneName(name string) error {
	if len(name) > 253 || strings.Count(name, ".") < 1 {
		return invalid("domain: a name like example.com")
	}
	for _, l := range strings.Split(name, ".") {
		if l == "" || len(l) > 63 || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return invalid("domain: %q is not a valid name", name)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return invalid("domain: %q is not a valid name (lowercase letters, digits and hyphens)", name)
			}
		}
	}
	if net.ParseIP(name) != nil {
		return invalid("domain: an IP address is not a domain")
	}
	return nil
}

// AddZoneInput registers a domain.
type AddZoneInput struct {
	Name             string
	ProviderID       string // "" = manual
	Grants           []store.ZoneGrant
	WildcardPolicy   string
	AuthorityID      string
	ChallengeZone    string // delegate _acme-challenge to this domain (needs a provider)
	ChallengeBuiltin bool
}

// AllProjects is the grant project that means every project.
const AllProjects = "*"

func (s *Service) AddZone(ctx context.Context, in AddZoneInput) (ZoneView, error) {
	name := Fqdn(in.Name)
	if err := ValidZoneName(name); err != nil {
		return ZoneView{}, err
	}
	z := store.Zone{Name: name, Mode: "manual", WildcardPolicy: "off", AuthorityID: in.AuthorityID, CreatedAt: s.now().UTC()}
	if in.WildcardPolicy == "on" {
		z.WildcardPolicy = "on"
	}
	if in.ProviderID != "" {
		p, err := s.provider(ctx, in.ProviderID)
		if err != nil {
			return ZoneView{}, err
		}
		zones, err := p.Zones(ctx)
		if err != nil {
			return ZoneView{}, err
		}
		i := slices.IndexFunc(zones, func(x ZoneInfo) bool { return x.Name == name })
		if i < 0 {
			return ZoneView{}, invalid("the provider has no zone named %s that this token can see", name)
		}
		z.ProviderID, z.ProviderZoneID, z.Mode = in.ProviderID, zones[i].ID, "managed"
	}
	if in.ChallengeZone != "" {
		cz, err := s.validChallengeZone(ctx, name, in.ChallengeZone)
		if err != nil {
			return ZoneView{}, err
		}
		z.ChallengeZone = cz
	} else if in.ChallengeBuiltin {
		if s.Builtin == nil || !s.Builtin.Enabled() {
			return ZoneView{}, invalid("challengeBuiltin: set up SynCloud's challenge server first (Domains → Challenge server)")
		}
		z.ChallengeBuiltin = true
	}
	grants, err := s.normalizeGrants(name, in.Grants)
	if err != nil {
		return ZoneView{}, err
	}
	if len(in.Grants) == 0 {
		// A new domain is usable by all projects, wildcards included.
		grants = []store.ZoneGrant{{Zone: name, Project: AllProjects, Suffix: name, Wildcards: true}}
	}
	if err := s.st.CreateZone(ctx, z, grants); err != nil {
		if errors.Is(err, store.ErrZoneExists) {
			return ZoneView{}, &ConflictError{Msg: "the domain " + name + " is already registered"}
		}
		return ZoneView{}, err
	}
	s.changed()
	return s.Zone(ctx, name)
}

func (s *Service) normalizeGrants(zone string, in []store.ZoneGrant) ([]store.ZoneGrant, error) {
	var out []store.ZoneGrant
	for _, g := range in {
		g.Zone = zone
		g.Project = strings.TrimSpace(g.Project)
		g.Suffix = Fqdn(g.Suffix)
		if g.Suffix == "" {
			g.Suffix = zone
		}
		if g.Project == "" {
			return nil, invalid("grants: a project (or * for all projects) is required")
		}
		if !InZone(g.Suffix, zone) {
			return nil, invalid("grants: %s is not %s or below it", g.Suffix, zone)
		}
		out = append(out, g)
	}
	return out, nil
}

func (s *Service) view(ctx context.Context, z store.Zone, providers map[string]store.DNSProvider, grants []store.ZoneGrant) ZoneView {
	v := ZoneView{Name: z.Name, Mode: z.Mode, ProviderID: z.ProviderID, WildcardPolicy: z.WildcardPolicy, AuthorityID: z.AuthorityID, ChallengeZone: z.ChallengeZone, ChallengeBuiltin: z.ChallengeBuiltin, Grants: grants, CreatedAt: z.CreatedAt}
	if v.Grants == nil {
		v.Grants = []store.ZoneGrant{}
	}
	if p, ok := providers[z.ProviderID]; ok {
		v.Provider = p.Name
	}
	return v
}

func (s *Service) Zones(ctx context.Context) ([]ZoneView, error) {
	zs, err := s.st.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := s.st.ListDNSProviders(ctx)
	if err != nil {
		return nil, err
	}
	pm := map[string]store.DNSProvider{}
	for _, p := range ps {
		pm[p.ID] = p
	}
	gs, err := s.st.ListZoneGrants(ctx, "")
	if err != nil {
		return nil, err
	}
	by := map[string][]store.ZoneGrant{}
	for _, g := range gs {
		by[g.Zone] = append(by[g.Zone], g)
	}
	out := make([]ZoneView, 0, len(zs))
	for _, z := range zs {
		out = append(out, s.view(ctx, z, pm, by[z.Name]))
	}
	return out, nil
}

func (s *Service) Zone(ctx context.Context, name string) (ZoneView, error) {
	z, err := s.st.Zone(ctx, Fqdn(name))
	if errors.Is(err, store.ErrNotFound) {
		return ZoneView{}, ErrNotFound
	}
	if err != nil {
		return ZoneView{}, err
	}
	ps, _ := s.st.ListDNSProviders(ctx)
	pm := map[string]store.DNSProvider{}
	for _, p := range ps {
		pm[p.ID] = p
	}
	gs, err := s.st.ListZoneGrants(ctx, z.Name)
	if err != nil {
		return ZoneView{}, err
	}
	v := s.view(ctx, z, pm, gs)
	if z.ProviderID != "" {
		if p, err := s.provider(ctx, z.ProviderID); err == nil {
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			if zones, err := p.Zones(cctx); err == nil {
				for _, x := range zones {
					if x.ID == z.ProviderZoneID {
						v.NameServers = x.NameServers
					}
				}
			}
		}
	}
	return v, nil
}

// validChallengeZone checks the domain a domain delegates its challenges to.
func (s *Service) validChallengeZone(ctx context.Context, self, name string) (string, error) {
	cz := Fqdn(name)
	if cz == "" {
		return "", nil
	}
	if cz == self {
		return "", invalid("challengeZone: a domain cannot delegate to itself: connect a provider to it instead")
	}
	tz, err := s.st.Zone(ctx, cz)
	if errors.Is(err, store.ErrNotFound) {
		return "", invalid("challengeZone: %s is not a registered domain", cz)
	}
	if err != nil {
		return "", err
	}
	if tz.ProviderID == "" {
		return "", invalid("challengeZone: %s has no DNS provider connected, so SynCloud cannot write records there", cz)
	}
	if tz.ChallengeZone != "" {
		return "", invalid("challengeZone: %s delegates its own challenges elsewhere: pick a domain with a provider", cz)
	}
	return cz, nil
}

// UpdateZoneInput changes a domain; nil fields stay.
type UpdateZoneInput struct {
	Provider         *string // provider id, "" to unlink
	WildcardPolicy   *string
	AuthorityID      *string
	ChallengeZone    *string // a registered domain with a provider, "" to stop delegating
	ChallengeBuiltin *bool   // answer with SynCloud's own server
	Grants           *[]store.ZoneGrant
}

func (s *Service) UpdateZone(ctx context.Context, name string, in UpdateZoneInput) (ZoneView, error) {
	z, err := s.st.Zone(ctx, Fqdn(name))
	if errors.Is(err, store.ErrNotFound) {
		return ZoneView{}, ErrNotFound
	}
	if err != nil {
		return ZoneView{}, err
	}
	if in.Provider != nil {
		if *in.Provider == "" {
			z.ProviderID, z.ProviderZoneID, z.Mode = "", "", "manual"
		} else {
			p, err := s.provider(ctx, *in.Provider)
			if err != nil {
				return ZoneView{}, err
			}
			zones, err := p.Zones(ctx)
			if err != nil {
				return ZoneView{}, err
			}
			i := slices.IndexFunc(zones, func(x ZoneInfo) bool { return x.Name == z.Name })
			if i < 0 {
				return ZoneView{}, invalid("the provider has no zone named %s that this token can see", z.Name)
			}
			z.ProviderID, z.ProviderZoneID, z.Mode = *in.Provider, zones[i].ID, "managed"
		}
	}
	if in.WildcardPolicy != nil {
		if *in.WildcardPolicy != "on" && *in.WildcardPolicy != "off" {
			return ZoneView{}, invalid("wildcardPolicy: on or off")
		}
		z.WildcardPolicy = *in.WildcardPolicy
	}
	if in.AuthorityID != nil {
		z.AuthorityID = *in.AuthorityID
	}
	cleared := false
	if in.ChallengeZone != nil {
		cz, err := s.validChallengeZone(ctx, z.Name, *in.ChallengeZone)
		if err != nil {
			return ZoneView{}, err
		}
		cleared = z.ChallengeZone != "" && cz != z.ChallengeZone
		z.ChallengeZone = cz
		if cz != "" && in.ChallengeBuiltin == nil {
			z.ChallengeBuiltin = false // an external validation domain replaces the built-in server
		}
	}
	if in.ChallengeBuiltin != nil {
		if *in.ChallengeBuiltin {
			if s.Builtin == nil || !s.Builtin.Enabled() {
				return ZoneView{}, invalid("challengeBuiltin: set up SynCloud's challenge server first (Domains → Challenge server)")
			}
			if z.ChallengeZone != "" {
				cleared, z.ChallengeZone = true, ""
			}
		}
		z.ChallengeBuiltin = *in.ChallengeBuiltin
	}
	if err := s.st.UpdateZone(ctx, z); err != nil {
		return ZoneView{}, err
	}
	if cleared {
		s.removeDelegation(ctx, z)
	}
	if in.Grants != nil {
		gs, err := s.normalizeGrants(z.Name, *in.Grants)
		if err != nil {
			return ZoneView{}, err
		}
		if err := s.st.SetZoneGrants(ctx, z.Name, gs); err != nil {
			return ZoneView{}, err
		}
	}
	s.changed()
	return s.Zone(ctx, z.Name)
}

// RemoveZone unregisters a domain. Its records at the provider stay as they are.
// Hosts of services under it keep working: grants only matter when adding.
func (s *Service) RemoveZone(ctx context.Context, name string) error {
	if _, err := s.st.Zone(ctx, Fqdn(name)); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err := s.st.DeleteZone(ctx, Fqdn(name)); err != nil {
		return err
	}
	s.changed()
	return nil
}

func (s *Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// ZoneFor returns the registered domain a name is in (the longest match).
func (s *Service) ZoneFor(ctx context.Context, host string) (store.Zone, bool) {
	host = strings.TrimPrefix(Fqdn(host), "*.")
	zs, err := s.st.ListZones(ctx)
	if err != nil {
		return store.Zone{}, false
	}
	var best store.Zone
	found := false
	for _, z := range zs {
		if InZone(host, z.Name) && (!found || len(z.Name) > len(best.Name)) {
			best, found = z, true
		}
	}
	return best, found
}

// Allowed says whether a project may use a host (a wildcard host starts
// with "*."). Names outside every registered domain are free, as before.
func (s *Service) Allowed(ctx context.Context, project, host string) error {
	host = Fqdn(host)
	wildcard := strings.HasPrefix(host, "*.")
	z, ok := s.ZoneFor(ctx, host)
	if !ok {
		return nil
	}
	base := strings.TrimPrefix(host, "*.")
	grants, err := s.st.ListZoneGrants(ctx, z.Name)
	if err != nil {
		return err
	}
	for _, g := range grants {
		if g.Project != AllProjects && g.Project != project {
			continue
		}
		if !InZone(base, g.Suffix) {
			continue
		}
		if wildcard && !g.Wildcards {
			continue
		}
		return nil
	}
	if wildcard {
		return fmt.Errorf("%w: %s is not granted to project %s (wildcards need a grant that allows them)", ErrNotGranted, host, project)
	}
	return fmt.Errorf("%w: %s is in the domain %s, which is not granted to project %s", ErrNotGranted, host, z.Name, project)
}

// ── records ─────────────────────────────────────────────────────────────────

// RecordView is a record with whether the platform created it.
type RecordView struct {
	Record
	Managed bool `json:"managed"`
}

// RecordFilter narrows a listing.
type RecordFilter struct{ Type, Name, Q string }

func (s *Service) zoneProvider(ctx context.Context, zone string) (store.Zone, Provider, error) {
	z, err := s.st.Zone(ctx, Fqdn(zone))
	if errors.Is(err, store.ErrNotFound) {
		return z, nil, ErrNotFound
	}
	if err != nil {
		return z, nil, err
	}
	if z.ProviderID == "" {
		return z, nil, ErrNoProvider
	}
	p, err := s.provider(ctx, z.ProviderID)
	return z, p, err
}

func (s *Service) managedSet(ctx context.Context, z store.Zone) map[string]bool {
	ms, _ := s.st.ListManagedRecords(ctx, z.ProviderID, z.Name)
	m := make(map[string]bool, len(ms))
	for _, r := range ms {
		m[r.RecordID] = true
	}
	return m
}

func (s *Service) Records(ctx context.Context, zone string, f RecordFilter) ([]RecordView, error) {
	z, p, err := s.zoneProvider(ctx, zone)
	if err != nil {
		return nil, err
	}
	rs, err := p.Records(ctx, z.ProviderZoneID)
	if err != nil {
		return nil, err
	}
	managed := s.managedSet(ctx, z)
	out := make([]RecordView, 0, len(rs))
	for _, r := range rs {
		if f.Type != "" && !strings.EqualFold(r.Type, f.Type) {
			continue
		}
		if f.Name != "" && Fqdn(r.Name) != Fqdn(f.Name) {
			continue
		}
		if f.Q != "" && !strings.Contains(strings.ToLower(r.Name+" "+r.Content), strings.ToLower(f.Q)) {
			continue
		}
		out = append(out, RecordView{Record: r, Managed: managed[r.ID] || strings.Contains(r.Comment, "managed-by=syncloud")})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	return out, nil
}

func validateRecord(zone string, r *Record) error {
	r.Type = normType(r.Type)
	r.Name = Fqdn(r.Name)
	r.Content = strings.TrimSpace(r.Content)
	if !slices.Contains(RecordTypes, r.Type) {
		return invalid("type: one of %s", strings.Join(RecordTypes, ", "))
	}
	r.Name = Qualify(zone, r.Name)
	if !InZone(r.Name, zone) {
		return invalid("name: %s is not in %s", r.Name, zone)
	}
	if r.Content == "" {
		return invalid("content: required")
	}
	switch r.Type {
	case "A":
		if ip := net.ParseIP(r.Content); ip == nil || ip.To4() == nil {
			return invalid("content: an A record needs an IPv4 address")
		}
	case "AAAA":
		if ip := net.ParseIP(r.Content); ip == nil || ip.To4() != nil {
			return invalid("content: an AAAA record needs an IPv6 address")
		}
	case "CNAME", "NS":
		r.Content = Fqdn(r.Content)
	case "MX":
		if r.Priority == nil {
			p := 10
			r.Priority = &p
		}
	}
	if r.Proxied && r.Type != "A" && r.Type != "AAAA" && r.Type != "CNAME" {
		return invalid("proxied: only A, AAAA and CNAME records can be proxied")
	}
	return nil
}

// Qualify turns a record name as a person types it into its full name: @ is
// the domain, a name already in the domain stays, any other name is relative
// to it (as at the DNS providers themselves).
func Qualify(zone, name string) string {
	zone, name = Fqdn(zone), Fqdn(name)
	switch {
	case name == "" || name == "@":
		return zone
	case InZone(name, zone):
		return name
	}
	return name + "." + zone
}

// CreateRecord adds a record. An exact duplicate is refused, and so is a
// CNAME beside other records, with the record that is in the way.
func (s *Service) CreateRecord(ctx context.Context, zone string, r Record) (Record, error) {
	z, p, err := s.zoneProvider(ctx, zone)
	if err != nil {
		return Record{}, err
	}
	if err := validateRecord(z.Name, &r); err != nil {
		return Record{}, err
	}
	have, err := p.Records(ctx, z.ProviderZoneID)
	if err != nil {
		return Record{}, err
	}
	for i, x := range have {
		if Fqdn(x.Name) != r.Name {
			continue
		}
		if x.Type == r.Type && contentKey(r.Type, x.Content) == contentKey(r.Type, r.Content) {
			return Record{}, &ConflictError{Msg: "the same record already exists", Record: &have[i]}
		}
		if (r.Type == "CNAME") != (x.Type == "CNAME") {
			return Record{}, &ConflictError{Msg: fmt.Sprintf("a %s record cannot share a name with a CNAME", r.Type), Record: &have[i]}
		}
	}
	return p.Create(ctx, z.ProviderZoneID, r)
}

func (s *Service) UpdateRecord(ctx context.Context, zone, id string, r Record) (Record, error) {
	z, p, err := s.zoneProvider(ctx, zone)
	if err != nil {
		return Record{}, err
	}
	if err := validateRecord(z.Name, &r); err != nil {
		return Record{}, err
	}
	return p.Update(ctx, z.ProviderZoneID, id, r)
}

func (s *Service) DeleteRecord(ctx context.Context, zone, id string) error {
	z, p, err := s.zoneProvider(ctx, zone)
	if err != nil {
		return err
	}
	if err := p.Delete(ctx, z.ProviderZoneID, id); err != nil {
		return err
	}
	return s.st.DeleteManagedRecord(ctx, z.ProviderID, id)
}

type tracker struct {
	s     *Service
	ctx   context.Context
	zone  store.Zone
	owner map[string]Owner
}

func (t *tracker) Created(r Record, o Owner) error {
	return t.s.st.PutManagedRecord(t.ctx, store.ManagedRecord{ProviderID: t.zone.ProviderID, Zone: t.zone.Name, RecordID: r.ID,
		Type: r.Type, Name: r.Name, Content: r.Content, OwnerKind: o.Kind, OwnerID: o.ID, Purpose: o.Purpose, CreatedAt: t.s.now().UTC()})
}

func (t *tracker) Removed(id string) error {
	return t.s.st.DeleteManagedRecord(t.ctx, t.zone.ProviderID, id)
}

// PointInput asks for the records that make a name reach some addresses.
type PointInput struct {
	Name    string
	Targets []string // IPv4 and IPv6 addresses
	Proxied *bool
	TTL     int
	Owner   Owner
}

// PointResult is the plan, and what was done when it was applied.
type PointResult struct {
	Plan    PlanResult `json:"plan"`
	Applied *Applied   `json:"applied,omitempty"`
	// Manual lists the records to create by hand when the domain has no provider.
	Manual []Record `json:"manual,omitempty"`
}

// Point reconciles the A and AAAA records of a name to targets. plan only
// computes; replace lets it remove records the platform did not create.
func (s *Service) Point(ctx context.Context, zone string, in PointInput, plan, replace bool) (PointResult, error) {
	z, err := s.st.Zone(ctx, Fqdn(zone))
	if errors.Is(err, store.ErrNotFound) {
		return PointResult{}, ErrNotFound
	}
	if err != nil {
		return PointResult{}, err
	}
	r := Record{Type: "A", Name: in.Name, Content: "0.0.0.0"}
	if err := validateRecord(z.Name, &r); err != nil {
		return PointResult{}, err
	}
	name := r.Name
	var wants []Want
	var manual []Record
	for _, t := range in.Targets {
		ip := net.ParseIP(strings.TrimSpace(t))
		if ip == nil {
			return PointResult{}, invalid("targets: %q is not an IP address", t)
		}
		typ := "A"
		if ip.To4() == nil {
			typ = "AAAA"
		}
		wants = append(wants, Want{Type: typ, Name: name, Content: ip.String(), TTL: in.TTL, Proxied: in.Proxied, Exclusive: true, Owner: in.Owner})
		manual = append(manual, Record{Type: typ, Name: name, Content: ip.String(), TTL: 1})
	}
	if len(wants) == 0 {
		return PointResult{}, invalid("targets: at least one IP address")
	}
	if z.ProviderID == "" {
		return PointResult{Manual: manual}, nil
	}
	_, p, err := s.zoneProvider(ctx, zone)
	if err != nil {
		return PointResult{}, err
	}
	have, err := p.Records(ctx, z.ProviderZoneID)
	if err != nil {
		return PointResult{}, err
	}
	pl := Plan(have, wants, s.managedSet(ctx, z))
	res := PointResult{Plan: pl}
	if plan {
		return res, nil
	}
	got, err := Apply(ctx, p, z.ProviderZoneID, pl, replace, &tracker{s: s, ctx: ctx, zone: z})
	res.Applied = &got
	return res, err
}

// Cleanup lists duplicates and leftover challenge records of a domain's
// zone; with apply it removes the exact duplicates and the leftovers the
// platform created. Other tools' records are only listed.
type CleanupResult struct {
	Duplicates []Record `json:"duplicates"`
	Challenges []Record `json:"challenges"`
	Removed    int      `json:"removed"`
}

func (s *Service) Cleanup(ctx context.Context, zone string, apply bool) (CleanupResult, error) {
	z, p, err := s.zoneProvider(ctx, zone)
	if err != nil {
		return CleanupResult{}, err
	}
	have, err := p.Records(ctx, z.ProviderZoneID)
	if err != nil {
		return CleanupResult{}, err
	}
	managed := s.managedSet(ctx, z)
	res := CleanupResult{Duplicates: []Record{}, Challenges: []Record{}}
	seen := map[string]bool{}
	for _, r := range have {
		k := normType(r.Type) + "|" + Fqdn(r.Name) + "|" + contentKey(normType(r.Type), r.Content)
		if seen[k] {
			res.Duplicates = append(res.Duplicates, r)
			continue
		}
		seen[k] = true
		if normType(r.Type) == "TXT" && (strings.HasPrefix(Fqdn(r.Name), AcmePrefix) || isDelegationTarget(Fqdn(r.Name))) {
			res.Challenges = append(res.Challenges, r)
		}
	}
	if !apply {
		return res, nil
	}
	for _, r := range res.Duplicates {
		if err := p.Delete(ctx, z.ProviderZoneID, r.ID); err != nil {
			return res, err
		}
		_ = s.st.DeleteManagedRecord(ctx, z.ProviderID, r.ID)
		res.Removed++
	}
	for _, r := range res.Challenges {
		if !managed[r.ID] && !strings.Contains(r.Comment, "managed-by=syncloud") {
			continue // another tool's: never removed by itself
		}
		if err := p.Delete(ctx, z.ProviderZoneID, r.ID); err != nil {
			return res, err
		}
		_ = s.st.DeleteManagedRecord(ctx, z.ProviderID, r.ID)
		res.Removed++
	}
	return res, nil
}

// ── what the certificates need ──────────────────────────────────────────────

// Managed reports whether the platform can write records for a name (its
// domain has a provider).
func (s *Service) Managed(ctx context.Context, name string) bool {
	return s.Route(ctx, name).Auto
}

// txtHome finds the zone and the name a challenge TXT is written at: the
// name itself, or where its CNAME delegates it.
func (s *Service) txtHome(ctx context.Context, name string) (store.Zone, string, error) {
	r := s.Route(ctx, name)
	if !r.Auto {
		return store.Zone{}, "", ErrNoProvider
	}
	at := r.Name
	if r.Mode == "delegated" {
		at = r.Target
	}
	z, ok := s.ZoneFor(ctx, at)
	if !ok || z.ProviderID == "" {
		return store.Zone{}, "", ErrNoProvider
	}
	return z, at, nil
}

// PutTXT creates a TXT record for a challenge and returns what to hand to
// DeleteTXT. A record that is already there is reused.
func (s *Service) PutTXT(ctx context.Context, name, value string) (string, error) {
	if r := s.Route(ctx, name); r.Mode == "builtin" {
		id, err := s.Builtin.Put(ctx, r.Target, value)
		return "builtin:" + id, err
	}
	z, at, err := s.txtHome(ctx, name)
	if err != nil {
		return "", err
	}
	p, err := s.provider(ctx, z.ProviderID)
	if err != nil {
		return "", err
	}
	have, err := p.Records(ctx, z.ProviderZoneID)
	if err != nil {
		return "", err
	}
	for _, r := range have {
		if normType(r.Type) == "TXT" && Fqdn(r.Name) == at && contentKey("TXT", r.Content) == value {
			return r.ID, nil
		}
	}
	made, err := p.Create(ctx, z.ProviderZoneID, Record{Type: "TXT", Name: at, Content: value, TTL: 60, Comment: "managed-by=syncloud"})
	if err != nil {
		return "", err
	}
	_ = s.st.PutManagedRecord(ctx, store.ManagedRecord{ProviderID: z.ProviderID, Zone: z.Name, RecordID: made.ID, Type: "TXT",
		Name: at, Content: value, OwnerKind: "challenge", OwnerID: ChallengeName(name), Purpose: "acme dns-01", CreatedAt: s.now().UTC()})
	return made.ID, nil
}

// DeleteTXT removes a challenge record the platform created.
func (s *Service) DeleteTXT(ctx context.Context, name, id string) error {
	if strings.HasPrefix(id, "builtin:") { // written to SynCloud's own server
		if s.Builtin == nil {
			return nil
		}
		return s.Builtin.Delete(ctx, strings.TrimPrefix(id, "builtin:"))
	}
	z, _, err := s.txtHome(ctx, name)
	if err != nil {
		return nil // the route is gone (a provider removed): nothing to clean
	}
	p, err := s.provider(ctx, z.ProviderID)
	if err != nil {
		return err
	}
	if err := p.Delete(ctx, z.ProviderZoneID, id); err != nil {
		var pe *Error
		if errors.As(err, &pe) && (pe.Status == 404 || pe.Status == 400) {
			_ = s.st.DeleteManagedRecord(ctx, z.ProviderID, id)
			return nil
		}
		return err
	}
	return s.st.DeleteManagedRecord(ctx, z.ProviderID, id)
}

// LookupResolvers are what propagation checks ask.
func (s *Service) LookupResolvers() []string {
	if len(s.Resolvers) > 0 {
		return s.Resolvers
	}
	return DefaultResolvers
}

// Lookup answers a query from public and authoritative servers.
func (s *Service) Lookup(ctx context.Context, name, qtype, via string) ([]Answer, error) {
	name = Fqdn(name)
	if name == "" || len(name) > 253 {
		return nil, invalid("name: required")
	}
	if qtype == "" {
		qtype = "A"
	}
	if _, ok := qtypes[strings.ToUpper(qtype)]; !ok {
		return nil, invalid("type: one of %s", strings.Join(QueryTypes(), ", "))
	}
	if via != "" && via != "public" && via != "authoritative" {
		return nil, invalid("via: public or authoritative")
	}
	return Lookup(ctx, s.LookupResolvers(), name, qtype, via), nil
}

// OriginCA returns the origin-certificate API of a provider, if it has one.
func (s *Service) OriginCA(ctx context.Context, providerID string) (OriginCA, error) {
	p, err := s.provider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	o, ok := p.(OriginCA)
	if !ok {
		return nil, invalid("this provider cannot issue origin certificates")
	}
	return o, nil
}
