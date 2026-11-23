package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/dns"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Domains, DNS providers and records (Phase 28).

func (s *Server) requireDNS(w http.ResponseWriter) bool {
	if s.dns == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "domains are not enabled")
		return false
	}
	return true
}

// dnsError maps the DNS service's errors to the API's.
func (s *Server) dnsError(w http.ResponseWriter, what string, err error) {
	var conflict *dns.ConflictError
	var perr *dns.Error
	switch {
	case errors.Is(err, dns.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "not found")
	case errors.Is(err, dns.ErrInvalid):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, dns.ErrNotGranted):
		writeError(w, http.StatusForbidden, CodeForbidden, err.Error())
	case errors.Is(err, dns.ErrNoProvider):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.As(err, &conflict):
		body := map[string]any{"error": apiError{Code: CodeConflict, Message: conflict.Msg}}
		if conflict.Record != nil {
			body["record"] = conflict.Record
		}
		writeJSON(w, http.StatusConflict, body)
	case errors.As(err, &perr):
		writeError(w, http.StatusBadGateway, "provider_error", err.Error())
	default:
		s.internalError(w, what, err)
	}
}

// ── providers ───────────────────────────────────────────────────────────────

func (s *Server) handleListDnsProviders(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	ps, err := s.dns.ListProviders(r.Context())
	if err != nil {
		s.dnsError(w, "list DNS providers", err)
		return
	}
	out := ps[:0]
	for _, p := range ps {
		if s.can(r, "dns:ListDnsProviders", "srn:syncloud:dns-provider/"+p.ID) {
			out = append(out, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleGetDnsProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	ps, err := s.dns.ListProviders(r.Context())
	if err != nil {
		s.dnsError(w, "get DNS provider", err)
		return
	}
	for _, p := range ps {
		if p.ID == r.PathValue("id") {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	writeError(w, http.StatusNotFound, CodeNotFound, "no such DNS provider")
}

func (s *Server) handleCreateDnsProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Type == "" {
		req.Type = "cloudflare"
	}
	p, err := s.dns.AddProvider(r.Context(), req.Name, req.Type, req.Token)
	if err != nil {
		s.dnsError(w, "create DNS provider", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "dns:CreateDnsProvider", "srn:syncloud:dns-provider/"+p.ID, map[string]any{"name": p.Name, "type": p.Type})
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateDnsProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := s.dns.ReplaceToken(r.Context(), id, req.Token); err != nil {
		s.dnsError(w, "replace DNS provider token", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "dns:UpdateDnsProvider", "srn:syncloud:dns-provider/"+id, map[string]any{"token": "replaced"})
	s.handleGetDnsProvider(w, r)
}

func (s *Server) handleDeleteDnsProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.dns.RemoveProvider(r.Context(), id); err != nil {
		s.dnsError(w, "remove DNS provider", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "dns:DeleteDnsProvider", "srn:syncloud:dns-provider/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSyncDnsProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	p, err := s.dns.SyncProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		s.dnsError(w, "sync DNS provider", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleListDnsProviderZones(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zs, err := s.dns.ProviderZones(r.Context(), r.PathValue("id"))
	if err != nil {
		s.dnsError(w, "list provider zones", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": zs})
}

func (s *Server) handleQueryDns(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	q := r.URL.Query()
	answers, err := s.dns.Lookup(r.Context(), q.Get("name"), q.Get("type"), q.Get("via"))
	if err != nil {
		s.dnsError(w, "lookup", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": dns.Fqdn(q.Get("name")), "type": strings.ToUpper(q.Get("type")), "answers": answers})
}

// ── domains ─────────────────────────────────────────────────────────────────

// domainUse says what depends on a domain.
type domainUse struct {
	Services     int `json:"services"`
	Certificates int `json:"certificates"`
}

func (s *Server) domainUsage(ctx context.Context) map[string]domainUse {
	out := map[string]domainUse{}
	zs, err := s.store.ListZones(ctx)
	if err != nil {
		return out
	}
	hosts, _ := s.store.ListServiceHosts(ctx)
	for _, z := range zs {
		var u domainUse
		for _, h := range hosts {
			if dns.InZone(strings.TrimPrefix(h, "*."), z.Name) {
				u.Services++
			}
		}
		if s.certs != nil {
			u.Certificates = s.certs.CountForZone(z.Name)
		}
		out[z.Name] = u
	}
	return out
}

type domainItem struct {
	dns.ZoneView
	Use domainUse `json:"use"`
}

func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zs, err := s.dns.Zones(r.Context())
	if err != nil {
		s.dnsError(w, "list domains", err)
		return
	}
	if q := strings.ToLower(r.URL.Query().Get("q")); q != "" {
		zs = filterZones(zs, func(z dns.ZoneView) bool { return strings.Contains(z.Name, q) })
	}
	if p := r.URL.Query().Get("provider"); p != "" {
		zs = filterZones(zs, func(z dns.ZoneView) bool { return z.ProviderID == p })
	}
	use := s.domainUsage(r.Context())
	items := make([]domainItem, 0, len(zs))
	for _, z := range zs {
		if s.can(r, "domain:ListDomains", domainSRN(z.Name)) {
			items = append(items, domainItem{ZoneView: z, Use: use[z.Name]})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func filterZones(zs []dns.ZoneView, keep func(dns.ZoneView) bool) []dns.ZoneView {
	out := zs[:0]
	for _, z := range zs {
		if keep(z) {
			out = append(out, z)
		}
	}
	return out
}

type zoneGrantBody struct {
	Project   string `json:"project"`
	Suffix    string `json:"suffix,omitempty"`
	Wildcards bool   `json:"wildcards"`
}

func grantsOf(in []zoneGrantBody) []store.ZoneGrant {
	out := make([]store.ZoneGrant, 0, len(in))
	for _, g := range in {
		out = append(out, store.ZoneGrant{Project: g.Project, Suffix: g.Suffix, Wildcards: g.Wildcards})
	}
	return out
}

func (s *Server) handleAddDomain(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	var req struct {
		Name           string          `json:"name"`
		Provider       string          `json:"provider"`
		Grants         []zoneGrantBody `json:"grants"`
		WildcardPolicy string          `json:"wildcardPolicy"`
		Authority      string          `json:"authority"`
		ChallengeZone  string          `json:"challengeZone"`
		// ChallengeBuiltin answers _acme-challenge with SynCloud's own server (a domain without a provider does so anyway while it is on).
		ChallengeBuiltin bool `json:"challengeBuiltin"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	z, err := s.dns.AddZone(r.Context(), dns.AddZoneInput{Name: req.Name, ProviderID: req.Provider, Grants: grantsOf(req.Grants), WildcardPolicy: req.WildcardPolicy, AuthorityID: req.Authority, ChallengeZone: req.ChallengeZone, ChallengeBuiltin: req.ChallengeBuiltin})
	if err != nil {
		s.dnsError(w, "add domain", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "domain:AddDomain", domainSRN(z.Name), map[string]any{"mode": z.Mode, "provider": z.Provider})
	writeJSON(w, http.StatusCreated, domainItem{ZoneView: z})
}

func (s *Server) handleGetDomain2(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	z, err := s.dns.Zone(r.Context(), r.PathValue("zone"))
	if err != nil {
		s.dnsError(w, "get domain", err)
		return
	}
	writeJSON(w, http.StatusOK, domainItem{ZoneView: z, Use: s.domainUsage(r.Context())[z.Name]})
}

func (s *Server) handleUpdateDomain(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	var req struct {
		Provider         *string          `json:"provider"`
		WildcardPolicy   *string          `json:"wildcardPolicy"`
		Authority        *string          `json:"authority"`
		ChallengeZone    *string          `json:"challengeZone"`
		ChallengeBuiltin *bool            `json:"challengeBuiltin"`
		Grants           *[]zoneGrantBody `json:"grants"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	in := dns.UpdateZoneInput{Provider: req.Provider, WildcardPolicy: req.WildcardPolicy, AuthorityID: req.Authority, ChallengeZone: req.ChallengeZone, ChallengeBuiltin: req.ChallengeBuiltin}
	if req.Grants != nil {
		g := grantsOf(*req.Grants)
		in.Grants = &g
	}
	z, err := s.dns.UpdateZone(r.Context(), r.PathValue("zone"), in)
	if err != nil {
		s.dnsError(w, "update domain", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "domain:UpdateDomain", domainSRN(z.Name), nil)
	writeJSON(w, http.StatusOK, domainItem{ZoneView: z, Use: s.domainUsage(r.Context())[z.Name]})
}

func (s *Server) handleRemoveDomain(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zone := dns.Fqdn(r.PathValue("zone"))
	if _, err := s.dns.Zone(r.Context(), zone); err != nil {
		s.dnsError(w, "remove domain", err)
		return
	}
	if u := s.domainUsage(r.Context())[zone]; (u.Services > 0 || u.Certificates > 0) && r.URL.Query().Get("force") != "true" {
		writeError(w, http.StatusConflict, CodeConflict, "the domain is in use by "+plural(u.Services, "service domain")+" and "+plural(u.Certificates, "certificate")+
			": removing it only stops managing it (nothing is deleted at the provider), but pass force=true to confirm")
		return
	}
	if err := s.dns.RemoveZone(r.Context(), zone); err != nil {
		s.dnsError(w, "remove domain", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "domain:RemoveDomain", domainSRN(zone), nil)
	w.WriteHeader(http.StatusNoContent)
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

// ── records ─────────────────────────────────────────────────────────────────

func (s *Server) handleListDomainRecords(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zone := dns.Fqdn(r.PathValue("zone"))
	q := r.URL.Query()
	rs, err := s.dns.Records(r.Context(), zone, dns.RecordFilter{Type: q.Get("type"), Name: q.Get("name"), Q: q.Get("q")})
	if err != nil {
		s.dnsError(w, "list records", err)
		return
	}
	out := rs[:0]
	for _, rec := range rs {
		if s.decideRecord(r, "domain:ListDomainRecords", recordSRN(zone, rec.Name), rec.Type).Allowed {
			out = append(out, rec)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type recordBody struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`
}

func (b recordBody) record() dns.Record {
	return dns.Record{Type: b.Type, Name: b.Name, Content: b.Content, TTL: b.TTL, Proxied: b.Proxied, Priority: b.Priority}
}

// authorizeRecord decides the exact record, after the name is known.
func (s *Server) authorizeRecord(w http.ResponseWriter, r *http.Request, action, zone, name, typ string) bool {
	res := recordSRN(zone, dns.Fqdn(name))
	if d := s.decideRecord(r, action, res, typ); !d.Allowed {
		s.denied(w, r, action, res, d.Reason)
		return false
	}
	return true
}

func qualify(zone, name string) string { return dns.Qualify(zone, name) }

func (s *Server) handleCreateDomainRecord(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zone := dns.Fqdn(r.PathValue("zone"))
	var req recordBody
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.authorizeRecord(w, r, "domain:CreateDomainRecord", zone, qualify(zone, req.Name), req.Type) {
		return
	}
	rec, err := s.dns.CreateRecord(r.Context(), zone, req.record())
	if err != nil {
		s.dnsError(w, "create record", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "domain:CreateDomainRecord", recordSRN(zone, rec.Name), map[string]any{"type": rec.Type, "content": rec.Content})
	writeJSON(w, http.StatusCreated, rec)
}

// recordByID finds a record of a zone (to decide on its name and type).
func (s *Server) recordByID(r *http.Request, zone, id string) (dns.RecordView, error) {
	rs, err := s.dns.Records(r.Context(), zone, dns.RecordFilter{})
	if err != nil {
		return dns.RecordView{}, err
	}
	for _, rec := range rs {
		if rec.ID == id {
			return rec, nil
		}
	}
	return dns.RecordView{}, dns.ErrNotFound
}

func (s *Server) handleUpdateDomainRecord(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zone, id := dns.Fqdn(r.PathValue("zone")), r.PathValue("id")
	var req recordBody
	if !decodeJSON(w, r, &req) {
		return
	}
	cur, err := s.recordByID(r, zone, id)
	if err != nil {
		s.dnsError(w, "update record", err)
		return
	}
	// Both what it is and what it becomes must be allowed.
	if !s.authorizeRecord(w, r, "domain:UpdateDomainRecord", zone, cur.Name, cur.Type) ||
		!s.authorizeRecord(w, r, "domain:UpdateDomainRecord", zone, qualify(zone, req.Name), req.Type) {
		return
	}
	rec, err := s.dns.UpdateRecord(r.Context(), zone, id, req.record())
	if err != nil {
		s.dnsError(w, "update record", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "domain:UpdateDomainRecord", recordSRN(zone, rec.Name), map[string]any{"type": rec.Type, "content": rec.Content})
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleDeleteDomainRecord(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zone, id := dns.Fqdn(r.PathValue("zone")), r.PathValue("id")
	cur, err := s.recordByID(r, zone, id)
	if err != nil {
		s.dnsError(w, "delete record", err)
		return
	}
	if !s.authorizeRecord(w, r, "domain:DeleteDomainRecord", zone, cur.Name, cur.Type) {
		return
	}
	// A record a service's address depends on is not removed by accident.
	if r.URL.Query().Get("force") != "true" {
		if hosts, _ := s.store.ListServiceHosts(r.Context()); (cur.Type == "A" || cur.Type == "AAAA" || cur.Type == "CNAME") && containsHost(hosts, cur.Name) {
			writeError(w, http.StatusConflict, CodeConflict, cur.Name+" is the address of a service domain: pass force=true to remove it anyway")
			return
		}
	}
	if err := s.dns.DeleteRecord(r.Context(), zone, id); err != nil {
		s.dnsError(w, "delete record", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "domain:DeleteDomainRecord", recordSRN(zone, cur.Name), map[string]any{"type": cur.Type, "content": cur.Content})
	w.WriteHeader(http.StatusNoContent)
}

func containsHost(hosts []string, name string) bool {
	for _, h := range hosts {
		if h == name {
			return true
		}
	}
	return false
}

func (s *Server) handlePointDomain(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zone := dns.Fqdn(r.PathValue("zone"))
	var req struct {
		Name    string   `json:"name"`
		Targets []string `json:"targets"`
		Project string   `json:"project"`
		Proxied *bool    `json:"proxied"`
		TTL     int      `json:"ttl"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	plan := r.URL.Query().Get("plan") == "true"
	replace := r.URL.Query().Get("replace") == "conflicts"
	name := qualify(zone, req.Name)
	if !s.authorizeRecord(w, r, "domain:PointDomain", zone, name, "A") || !s.authorizeRecord(w, r, "domain:PointDomain", zone, name, "AAAA") {
		return
	}
	targets := req.Targets
	if len(targets) == 0 && req.Project != "" && s.ingress != nil {
		targets = s.ingress.Expected(r.Context(), req.Project)
	}
	if len(targets) == 0 && s.detector != nil {
		if ip, err := s.detector.PublicIP(r.Context()); err == nil {
			targets = []string{ip}
		}
	}
	res, err := s.dns.Point(r.Context(), zone, dns.PointInput{Name: name, Targets: targets, Proxied: req.Proxied, TTL: req.TTL, Owner: dns.Owner{Kind: "point", ID: name}}, plan, replace)
	if err != nil {
		s.dnsError(w, "point domain", err)
		return
	}
	if !plan {
		u, _ := currentUser(r.Context())
		s.audit(r, u.ID, "domain:PointDomain", recordSRN(zone, name), map[string]any{"targets": targets})
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCleanupDomainRecords(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zone := dns.Fqdn(r.PathValue("zone"))
	apply := r.URL.Query().Get("apply") == "true"
	res, err := s.dns.Cleanup(r.Context(), zone, apply)
	if err != nil {
		s.dnsError(w, "clean up records", err)
		return
	}
	if apply {
		u, _ := currentUser(r.Context())
		s.audit(r, u.ID, "domain:CleanupDomainRecords", domainSRN(zone), map[string]any{"removed": res.Removed})
	}
	writeJSON(w, http.StatusOK, res)
}

// ── the _acme-challenge name ────────────────────────────────────────────────

// challengeHost reads the host a challenge request is about: the domain
// itself unless ?name= says another name under it (a wildcard may be written *.x).
func (s *Server) challengeHost(w http.ResponseWriter, r *http.Request) (zone, host string, ok bool) {
	zone = dns.Fqdn(r.PathValue("zone"))
	if _, err := s.dns.Zone(r.Context(), zone); err != nil {
		s.dnsError(w, "challenge", err)
		return "", "", false
	}
	host = zone
	if n := strings.TrimSpace(r.URL.Query().Get("name")); n != "" {
		host = dns.Fqdn(n)
	}
	if !dns.InZone(strings.TrimPrefix(host, "*."), zone) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, host+" is not in the domain "+zone)
		return "", "", false
	}
	return zone, host, true
}

// handleGetDomainChallenge says where the _acme-challenge TXT record of a name
// goes, and whether the CNAME of a delegated name is in place.
func (s *Server) handleGetDomainChallenge(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	_, host, ok := s.challengeHost(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.dns.Challenge(r.Context(), host))
}

// handleDelegateDomainChallenge creates the delegating CNAME of a name when
// the domain has a provider; otherwise it returns the record to create by hand.
func (s *Server) handleDelegateDomainChallenge(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	zone, host, ok := s.challengeHost(w, r)
	if !ok {
		return
	}
	replace := r.URL.Query().Get("replace") == "conflicts"
	route, err := s.dns.EnsureDelegation(r.Context(), host, replace)
	if err != nil {
		s.dnsError(w, "delegate the challenge", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "domain:DelegateDomainChallenge", recordSRN(zone, route.Name), map[string]any{"target": route.Target})
	writeJSON(w, http.StatusOK, route)
}

// ── SynCloud's own challenge server ─────────────────────────────────────────

func (s *Server) handleGetChallengeServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	v, err := s.dns.ChallengeServer(r.Context())
	if err != nil {
		s.dnsError(w, "challenge server", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleUpdateChallengeServer turns the server on or off: one click. The
// zone is _acme-challenge.<public ip>.sslip.io, which sslip.io already
// delegates to this controller; ip overrides the detected address.
func (s *Server) handleUpdateChallengeServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	var req struct {
		Enabled bool   `json:"enabled"`
		IP      string `json:"ip"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	pinned := strings.TrimSpace(req.IP) != ""
	if req.Enabled && strings.TrimSpace(req.IP) == "" && s.detector != nil {
		if ip, err := s.detector.PublicIP(r.Context()); err == nil {
			req.IP = ip
		}
	}
	if req.Enabled && strings.TrimSpace(req.IP) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "ip: this controller's public address could not be detected: give it")
		return
	}
	v, err := s.dns.ConfigureChallengeServer(r.Context(), req.Enabled, req.IP, pinned)
	if err != nil {
		s.dnsError(w, "set up the challenge server", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "domain:UpdateChallengeServer", "srn:syncloud:domains", map[string]any{"enabled": v.Enabled, "zone": v.Zone})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleCheckChallengeServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireDNS(w) {
		return
	}
	checks, err := s.dns.CheckChallengeServer(r.Context())
	if err != nil {
		s.dnsError(w, "check the challenge server", err)
		return
	}
	ok := true
	for _, c := range checks {
		ok = ok && c.OK
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "checks": checks})
}
