package certs

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// Certificates a person asks for (Phase 28): several names, wildcards,
// another authority, a provider's certificate imported, a request made for
// a provider to sign. They are stored beside the automatic per-host ones.

// DNSHost is what the certificates need from the domains: whether the
// platform can write a name's records, the challenge records, and a check
// that they are visible.
type DNSHost interface {
	// Challenge says where the TXT record of a challenge name goes: written
	// at the name (direct), written where a CNAME delegates it, or by hand.
	Challenge(ctx context.Context, name string) ChallengeInfo
	// EnsureDelegation creates the delegating CNAME when the domain has a provider.
	EnsureDelegation(ctx context.Context, name string) error
	PutTXT(ctx context.Context, name, value string) (id string, err error)
	DeleteTXT(ctx context.Context, name, id string) error
	// HasTXT returns the values that public resolvers do not all show yet.
	HasTXT(ctx context.Context, name string, values []string) (missing []string, err error)
	// ZoneOf names the registered domain a host belongs to, and its default authority.
	ZoneOf(ctx context.Context, host string) (zone, authorityID string, ok bool)
	// OriginCertificate asks a provider to sign an origin certificate.
	OriginCertificate(ctx context.Context, providerID, csrPEM string, names []string, days int) (string, error)
	// WildcardZones lists the domains that keep one wildcard certificate.
	WildcardZones(ctx context.Context) []ZonePolicy
}

// ChallengeInfo is how a challenge name is answered (see dns.ChallengeRoute).
type ChallengeInfo struct {
	Mode         string // direct | delegated | manual
	Auto         bool   // SynCloud writes the TXT record
	Target       string // delegated: the name the CNAME points to
	CNAMEOK      bool   // delegated: public resolvers show the CNAME
	CNAMEManaged bool   // delegated: SynCloud can create the CNAME itself
}

// ZonePolicy is a domain that keeps a wildcard certificate (the domain and *.domain).
type ZonePolicy struct{ Zone, AuthorityID string }

// SetDNS connects the domains. Without it wildcard orders are manual.
func (m *Manager) SetDNS(h DNSHost) { m.mu.Lock(); m.dnsHost = h; m.mu.Unlock() }

func (m *Manager) dns() DNSHost { m.mu.Lock(); defer m.mu.Unlock(); return m.dnsHost }

// ownSslipName reports whether a host is the controller's own <ip>.sslip.io
// with the challenge server on: sslip.io hands its _acme-challenge name to
// the controller, so the server answers it at the name itself.
func (m *Manager) ownSslipName(ctx context.Context, host string) bool {
	h := m.dns()
	if h == nil {
		return false
	}
	name := "_acme-challenge." + strings.ToLower(strings.TrimSuffix(host, "."))
	info := h.Challenge(ctx, name)
	return info.Mode == "builtin" && info.Target == name
}

// ── what an order shows ─────────────────────────────────────────────────────

// Challenge is a TXT record an order needs.
type Challenge struct {
	Name     string   `json:"name"`
	Value    string   `json:"value"`
	Names    []string `json:"names"`    // the certificate names it proves
	Wildcard bool     `json:"wildcard"` // proves a *. name
	Auto     bool     `json:"auto"`     // SynCloud writes and removes it (the domain has a provider)
	Present  bool     `json:"present"`  // public resolvers show it
	// Delegation: the challenge name is a CNAME to Target, where the TXT is written.
	Mode         string `json:"mode"`                   // direct | delegated | builtin | manual
	Target       string `json:"target,omitempty"`       // delegated: the CNAME's target
	CNAMEOK      bool   `json:"cnameOk,omitempty"`      // delegated: the CNAME is in place
	CNAMEManaged bool   `json:"cnameManaged,omitempty"` // delegated: SynCloud creates the CNAME
}

// OrderView is an order in progress.
type OrderView struct {
	State         string      `json:"state"` // awaiting_dns | verifying | issuing
	Manual        bool        `json:"manual"`
	Challenges    []Challenge `json:"challenges"`
	CreatedAt     time.Time   `json:"createdAt"`
	ExpiresAt     *time.Time  `json:"expiresAt,omitempty"`
	LastCheckedAt *time.Time  `json:"lastCheckedAt,omitempty"`
	LastError     string      `json:"lastError,omitempty"`
}

type chalRec struct {
	Name         string   `json:"name"`
	Value        string   `json:"value"`
	Names        []string `json:"names"`
	Wildcard     bool     `json:"wildcard"`
	Auto         bool     `json:"auto"`
	Present      bool     `json:"present"`
	AuthzURI     string   `json:"authz"`
	ChallengeURI string   `json:"challenge"`
	RecordID     string   `json:"record,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Target       string   `json:"target,omitempty"`
	CNAMEOK      bool     `json:"cnameOk,omitempty"`
	CNAMEManaged bool     `json:"cnameManaged,omitempty"`
}

// cnameMode reports whether a challenge is answered through a CNAME: to a
// validation domain at a provider (delegated) or to SynCloud's own server (builtin).
func cnameMode(mode string) bool { return mode == "delegated" || mode == "builtin" }

// waiting is true while a person has something to do: a TXT record to add,
// or the one-time CNAME of a delegated name.
func (c chalRec) waiting() bool {
	return !c.Auto || (cnameMode(c.Mode) && !c.CNAMEOK && !c.CNAMEManaged)
}

type orderState struct {
	key       string
	authority string
	uri       string
	state     string
	chals     []chalRec
	created   time.Time
	expires   time.Time
	checked   time.Time
	lastErr   string
}

func (o *orderState) view() *OrderView {
	v := &OrderView{State: o.state, CreatedAt: o.created.UTC(), LastError: o.lastErr, ExpiresAt: timePtr(o.expires), LastCheckedAt: timePtr(o.checked), Challenges: []Challenge{}}
	for _, c := range o.chals {
		v.Challenges = append(v.Challenges, Challenge{Name: c.Name, Value: c.Value, Names: c.Names, Wildcard: c.Wildcard, Auto: c.Auto, Present: c.Present,
			Mode: firstNonEmpty(c.Mode, map[bool]string{true: "direct", false: "manual"}[c.Auto]), Target: c.Target, CNAMEOK: c.CNAMEOK, CNAMEManaged: c.CNAMEManaged})
		if c.waiting() {
			v.Manual = true
		}
	}
	return v
}

func (m *Manager) persistOrder(ctx context.Context, o *orderState) {
	raw, _ := json.Marshal(o.chals)
	if err := m.st.PutCertOrder(ctx, store.CertOrder{CertKey: o.key, AuthorityID: o.authority, OrderURI: o.uri, State: o.state, Challenges: string(raw),
		CreatedAt: o.created, ExpiresAt: o.expires, LastCheckedAt: o.checked, LastError: o.lastErr}); err != nil {
		m.log.Warn("store certificate order", "cert", o.key, "err", err)
	}
}

func (m *Manager) loadOrders(ctx context.Context) error {
	os, err := m.st.ListCertOrders(ctx)
	if err != nil {
		return err
	}
	for _, r := range os {
		o := &orderState{key: r.CertKey, authority: r.AuthorityID, uri: r.OrderURI, state: StatusAwaitingDNS, created: r.CreatedAt, expires: r.ExpiresAt, checked: r.LastCheckedAt, lastErr: r.LastError}
		if json.Unmarshal([]byte(r.Challenges), &o.chals) != nil {
			continue
		}
		m.orders[r.CertKey] = o // a step that was running is taken up again from the TXT check
	}
	return nil
}

// ── names and requests ──────────────────────────────────────────────────────

// ValidName checks a certificate name: a host, or *.host.
func ValidName(n string) error {
	n = strings.ToLower(n)
	if len(n) > 253 || n == "" {
		return invalid("name: %q is not a valid host name", n)
	}
	h := strings.TrimPrefix(n, "*.")
	if net.ParseIP(h) != nil {
		return invalid("name: %q is an IP address: certificates are for host names", n)
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return invalid("name: %q needs a domain, like app.example.com", n)
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return invalid("name: %q is not a valid host name", n)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return invalid("name: %q is not a valid host name (letters, digits and hyphens)", n)
			}
		}
	}
	return nil
}

func normNames(in []string) ([]string, error) {
	var out []string
	for _, n := range in {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if err := ValidName(n); err != nil {
			return nil, err
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, invalid("names: at least one host name")
	}
	if len(out) > 100 {
		return nil, invalid("names: at most 100 names in one certificate")
	}
	return out, nil
}

func hasWildcard(names []string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, "*.") {
			return true
		}
	}
	return false
}

func newKeyID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "cert_" + hex.EncodeToString(b)
}

// RequestInput asks for a certificate.
type RequestInput struct {
	Names     []string
	Authority string // "" = the domain's, else the cluster's
	Method    string // "" (decided from the names) | http-01 | dns-01
	KeyType   string
	Fallback  string
	AutoRenew *bool
	Days      int // origin certificates: validity in days
	Actor     string
	Key       string // internal: a fixed key (wildcard policy)
}

func (m *Manager) findByNames(names []string) (string, bool) {
	set := slices.Clone(names)
	sort.Strings(set)
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, e := range m.cache {
		if !e.rec.Managed || e.rec.Status == StatusRevoked {
			continue
		}
		have := namesOf(e.rec)
		sort.Strings(have)
		if slices.Equal(have, set) {
			return k, true
		}
	}
	return "", false
}

// Request starts a certificate: a person asked for it. It is issued by the
// loop (or by Verify, once the TXT records are in place).
func (m *Manager) Request(ctx context.Context, in RequestInput) (View, error) {
	names, err := normNames(in.Names)
	if err != nil {
		return View{}, err
	}
	if !ValidKeyType(in.KeyType) {
		return View{}, invalid("keyType: one of %s", strings.Join(KeyTypes, ", "))
	}
	if k, ok := m.findByNames(names); ok && in.Key == "" {
		return View{}, conflict("a certificate for these names exists (%s)", k)
	}
	zone, zoneAuth := "", ""
	if h := m.dns(); h != nil {
		if z, a, ok := h.ZoneOf(ctx, strings.TrimPrefix(names[0], "*.")); ok {
			zone, zoneAuth = z, a
		}
	}
	authID := in.Authority
	if authID == "" {
		authID = zoneAuth
	}
	if authID == "" {
		authID = m.DefaultAuthorityID(ctx)
	}
	au, err := m.Authority(ctx, authID)
	if err != nil {
		return View{}, invalid("authority: no such certificate authority")
	}
	method, source := in.Method, "acme"
	switch {
	case au.Kind == KindOrigin:
		method, source = "origin-ca", "origin-ca"
	case method == "":
		method = "http-01"
		if hasWildcard(names) {
			method = "dns-01"
		}
	case method == "self-signed":
		source = "self-signed" // for private or internal names: browsers do not trust it
	case method != "http-01" && method != "dns-01":
		return View{}, invalid("method: http-01, dns-01 or self-signed")
	}
	if method == "http-01" && hasWildcard(names) {
		return View{}, invalid("a wildcard name can only be validated with DNS (dns-01)")
	}
	if method == "dns-01" {
		for _, n := range names {
			if h := strings.TrimPrefix(n, "*."); (strings.HasSuffix(h, ".sslip.io") || strings.HasSuffix(h, ".nip.io")) && !m.ownSslipName(ctx, h) {
				return View{}, invalid("%s cannot use DNS validation: sslip.io and nip.io do not let you add records (only the controller's own <ip>.sslip.io can, with SynCloud's challenge server on)", n)
			}
		}
	}
	if in.Fallback != "" {
		if _, err := m.Authority(ctx, in.Fallback); err != nil {
			return View{}, invalid("fallback: no such certificate authority")
		}
	}
	auto := true
	if in.AutoRenew != nil {
		auto = *in.AutoRenew
	}
	key := in.Key
	if key == "" {
		key = newKeyID()
	}
	rawNames, _ := json.Marshal(names)
	rec := store.Certificate{Host: key, Issuer: IssuerACME, Status: StatusPending, Names: string(rawNames), Method: method, Zone: zone, Source: source,
		Managed: true, AuthorityID: authID, FallbackID: in.Fallback, KeyType: in.KeyType, AutoRenew: auto, CreatedAt: m.now().UTC()}
	if source == "origin-ca" && in.Days > 0 {
		m.setOriginDays(key, in.Days)
	}
	e := &entry{rec: rec}
	if source != "self-signed" {
		m.save(ctx, e)
	}
	if source == "self-signed" {
		rec.AuthorityID, rec.AutoRenew = "", false
		e.rec = rec
		if err := m.makeSelfSigned(ctx, e); err != nil {
			return View{}, err
		}
		m.event(ctx, key, "requested", strings.Join(names, ", ")+", self-signed (not trusted by browsers)", in.Actor)
		v, _ := m.Get(ctx, key)
		return v, nil
	}
	m.event(ctx, key, "requested", fmt.Sprintf("%s, %s, from %s", strings.Join(names, ", "), method, au.Name), in.Actor)
	m.Kick()
	v, _ := m.Get(ctx, key)
	return v, nil
}

func (m *Manager) setOriginDays(key string, d int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.originDays == nil {
		m.originDays = map[string]int{}
	}
	m.originDays[key] = d
}

// ── the loop's work ─────────────────────────────────────────────────────────

func (m *Manager) renewWindow(r store.Certificate) time.Duration {
	w := m.cfg.RenewBefore
	if !r.NotBefore.IsZero() && r.NotBefore.Unix() > 0 && r.NotAfter.After(r.NotBefore) {
		if third := r.NotAfter.Sub(r.NotBefore) / 3; third < w {
			w = third // short certificates renew a third of their life before the end
		}
	}
	return w
}

func (m *Manager) needsIssue(key string, r store.Certificate, now time.Time) bool {
	if r.Status == StatusRevoked || r.Source == "imported" || r.Source == "self-signed" {
		return false
	}
	if m.renewNow[key] || r.CertPEM == "" {
		return true
	}
	return r.AutoRenew && r.NotAfter.Sub(now) <= m.renewWindow(r)
}

// reconcileManaged advances every certificate a person asked for.
func (m *Manager) reconcileManaged(ctx context.Context) {
	m.ensurePolicyCerts(ctx)
	m.mu.Lock()
	var keys []string
	for k, e := range m.cache {
		if e.rec.Managed {
			keys = append(keys, k)
		}
	}
	m.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		e := m.cache[k]
		o := m.orders[k]
		var rec store.Certificate
		if e != nil {
			rec = e.rec
		}
		force := m.renewNow[k]
		m.mu.Unlock()
		if e == nil || rec.Status == StatusRevoked {
			continue
		}
		now := m.now()
		switch {
		case o != nil:
			m.stepOrder(ctx, k, false)
		case m.needsIssue(k, rec, now) && (force || !now.Before(rec.NextAttemptAt)):
			m.startOrder(ctx, k)
		}
	}
}

// ensurePolicyCerts keeps one wildcard certificate for each domain that asks for it.
func (m *Manager) ensurePolicyCerts(ctx context.Context) {
	h := m.dns()
	if h == nil {
		return
	}
	for _, z := range h.WildcardZones(ctx) {
		names := []string{z.Zone, "*." + z.Zone}
		covered := false
		m.mu.Lock()
		for _, e := range m.cache {
			if e.rec.Managed && e.rec.Status != StatusRevoked && covers(namesOf(e.rec), z.Zone) && covers(namesOf(e.rec), "x."+z.Zone) {
				covered = true
			}
		}
		m.mu.Unlock()
		if covered {
			continue
		}
		key := "cert_wc_" + strings.ReplaceAll(z.Zone, ".", "_")
		if _, err := m.Request(ctx, RequestInput{Names: names, Authority: z.AuthorityID, Method: "dns-01", Actor: "wildcard policy", Key: key}); err != nil {
			m.log.Warn("wildcard certificate for a domain", "zone", z.Zone, "err", err)
		}
	}
}

func (m *Manager) failManaged(ctx context.Context, key string, err error, manual bool) {
	m.mu.Lock()
	e := m.cache[key]
	if e == nil {
		m.mu.Unlock()
		return
	}
	cp := *e
	m.mu.Unlock()
	now := m.now()
	cp.rec.Failures++
	cp.rec.LastError = err.Error()
	cp.rec.Status = StatusFailed
	wait := backoff(cp.rec.Failures)
	if d, ok := rateLimited(err); ok {
		cp.rec.Status = StatusRateLimited
		wait = max(d, time.Hour)
	}
	if manual {
		wait = 7 * 24 * time.Hour // a new order means new TXT values: not by itself
	}
	if cp.rec.CertPEM != "" && cp.rec.NotAfter.After(now) {
		cp.rec.Status = StatusValid // still serving a valid certificate; the renewal is retried
	}
	cp.rec.NextAttemptAt = now.Add(wait)
	m.mu.Lock()
	delete(m.renewNow, key)
	m.mu.Unlock()
	m.save(ctx, &cp)
	m.event(ctx, key, "failed", err.Error(), "")
	m.log.Warn("certificate request failed", "cert", key, "err", err, "retry_in", wait)
}

// startOrder asks the authority for the certificate: at once for HTTP-01
// and for DNS-01 with a provider, or as an order that waits for TXT records.
func (m *Manager) startOrder(ctx context.Context, key string) {
	m.mu.Lock()
	e := m.cache[key]
	if e == nil {
		m.mu.Unlock()
		return
	}
	rec := e.rec
	m.mu.Unlock()
	ictx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	authID := rec.AuthorityID
	if authID == "" {
		authID = m.DefaultAuthorityID(ctx)
	}
	err := m.issueManaged(ictx, key, rec, authID)
	if err != nil {
		if fb := m.fallbackFor(ctx, key, rec.FallbackID, err); fb != nil && fb.ID != authID {
			m.event(ctx, key, "fallback", "the first authority failed ("+err.Error()+"): trying "+fb.Name, "")
			if err2 := m.issueManaged(ictx, key, rec, fb.ID); err2 == nil {
				return
			} else {
				err = err2
			}
		}
		m.failManaged(ctx, key, err, false)
	}
}

var errAwaiting = errors.New("awaiting DNS")

// issueManaged runs one issuance at one authority.
func (m *Manager) issueManaged(ctx context.Context, key string, rec store.Certificate, authID string) error {
	au, err := m.Authority(ctx, authID)
	if err != nil {
		return err
	}
	names := namesOf(rec)
	switch {
	case au.Kind == KindOrigin || rec.Method == "origin-ca":
		return m.issueOrigin(ctx, key, rec, au, names)
	case rec.Method == "http-01":
		res, err := m.acme.issueHTTP(ctx, au, names, firstNonEmpty(rec.KeyType, au.KeyType))
		if err != nil {
			return err
		}
		m.finishIssue(ctx, key, res, au.ID, "http-01")
		return nil
	}
	err = m.startDNSOrder(ctx, key, rec, au, names)
	if errors.Is(err, errAwaiting) {
		return nil
	}
	return err
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (m *Manager) startDNSOrder(ctx context.Context, key string, rec store.Certificate, au Authority, names []string) error {
	c, err := m.acme.get(ctx, au)
	if err != nil {
		return err
	}
	order, err := c.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return fmt.Errorf("new order: %w", err)
	}
	h := m.dns()
	var chals []chalRec
	var delegErr string
	for _, u := range order.AuthzURLs {
		z, err := c.GetAuthorization(ctx, u)
		if err != nil {
			return err
		}
		if z.Status == acme.StatusValid {
			continue // the CA remembers an earlier validation
		}
		var ch *acme.Challenge
		for _, x := range z.Challenges {
			if x.Type == "dns-01" {
				ch = x
			}
		}
		if ch == nil {
			return errors.New("the authority offered no dns-01 challenge")
		}
		val, err := c.DNS01ChallengeRecord(ch.Token)
		if err != nil {
			return err
		}
		base := strings.TrimPrefix(strings.ToLower(z.Identifier.Value), "*.")
		cn := "_acme-challenge." + base
		shown := base
		if z.Wildcard {
			shown = "*." + base
		}
		rec := chalRec{Name: cn, Value: val, Names: []string{shown}, Wildcard: z.Wildcard, Mode: "manual", AuthzURI: z.URI, ChallengeURI: ch.URI}
		if h != nil {
			info := h.Challenge(ctx, cn)
			rec.Mode, rec.Auto, rec.Target, rec.CNAMEOK, rec.CNAMEManaged = info.Mode, info.Auto, info.Target, info.CNAMEOK, info.CNAMEManaged
			if cnameMode(info.Mode) && !info.CNAMEOK && info.CNAMEManaged {
				// The domain has a provider: make the delegating CNAME now.
				if err := h.EnsureDelegation(ctx, cn); err != nil {
					m.log.Warn("create the challenge delegation", "name", cn, "err", err)
					rec.CNAMEManaged = false // a person has to make it
					delegErr = fmt.Sprintf("SynCloud could not create the CNAME %s: %v", cn, err)
				} else {
					rec.CNAMEOK = h.Challenge(ctx, cn).CNAMEOK
				}
			}
		}
		chals = append(chals, rec)
	}
	o := &orderState{key: key, authority: au.ID, uri: order.URI, state: StatusAwaitingDNS, chals: chals, created: m.now().UTC(), expires: order.Expires, lastErr: delegErr}
	if o.expires.IsZero() {
		o.expires = o.created.Add(7 * 24 * time.Hour)
	}
	m.mu.Lock()
	m.orders[key] = o
	delete(m.renewNow, key)
	m.mu.Unlock()
	// Publish the records the platform can write.
	for i := range o.chals {
		if !o.chals[i].Auto {
			continue
		}
		id, err := h.PutTXT(ctx, o.chals[i].Name, o.chals[i].Value)
		if err != nil {
			m.dropOrder(ctx, key, true)
			return fmt.Errorf("write the challenge record %s: %w", o.chals[i].Name, err)
		}
		o.chals[i].RecordID = id
	}
	m.persistOrder(ctx, o)
	m.setStatus(ctx, key, StatusAwaitingDNS, "")
	manual, delegated := 0, 0
	for _, c := range o.chals {
		if c.waiting() {
			manual++
		}
		if cnameMode(c.Mode) {
			delegated++
		}
	}
	detail := fmt.Sprintf("%d TXT record(s) needed, %d written by SynCloud", len(o.chals), len(o.chals)-manual)
	if delegated > 0 {
		detail += fmt.Sprintf(", %d through a delegated _acme-challenge CNAME", delegated)
	}
	m.event(ctx, key, "challenge", detail, "")
	if manual == 0 {
		// Give the provider's DNS a moment to answer, then go on.
		m.waitPropagation(ctx, o, 75*time.Second)
	}
	if err := m.stepOrderErr(ctx, key, true); err != nil {
		return err
	}
	if m.hasOrder(key) {
		return errAwaiting
	}
	return nil
}

func (m *Manager) hasOrder(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orders[key] != nil
}

// waitPropagation polls until every record is visible or the time is up.
func (m *Manager) waitPropagation(ctx context.Context, o *orderState, d time.Duration) {
	h := m.dns()
	if h == nil {
		return
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if m.checkPresence(ctx, o) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// checkPresence updates which records public resolvers show; true when all.
func (m *Manager) checkPresence(ctx context.Context, o *orderState) bool {
	h := m.dns()
	all := true
	byName := map[string][]int{}
	for i, c := range o.chals {
		byName[c.Name] = append(byName[c.Name], i)
	}
	for i, c := range o.chals {
		if cnameMode(c.Mode) && h != nil {
			info := h.Challenge(ctx, c.Name)
			o.chals[i].CNAMEOK, o.chals[i].Target = info.CNAMEOK, info.Target
			if !info.CNAMEOK && info.CNAMEManaged {
				_ = h.EnsureDelegation(ctx, c.Name) // made now if it was removed
			}
		}
	}
	for name, idx := range byName {
		var vals []string
		for _, i := range idx {
			vals = append(vals, o.chals[i].Value)
		}
		var missing []string
		if h != nil {
			var err error
			if missing, err = h.HasTXT(ctx, name, vals); err != nil {
				missing = vals
			}
		} else {
			missing = vals
		}
		for _, i := range idx {
			o.chals[i].Present = !slices.Contains(missing, o.chals[i].Value)
			if o.chals[i].Present && cnameMode(o.chals[i].Mode) {
				o.chals[i].CNAMEOK = true // the TXT answers through the name: the CNAME works
			}
			all = all && o.chals[i].Present
		}
	}
	o.checked = m.now().UTC()
	return all
}

func (m *Manager) setStatus(ctx context.Context, key, status, lastErr string) {
	m.mu.Lock()
	e := m.cache[key]
	if e == nil {
		m.mu.Unlock()
		return
	}
	cp := *e
	m.mu.Unlock()
	if cp.rec.CertPEM != "" && cp.rec.NotAfter.After(m.now()) && status != StatusRevoked {
		cp.rec.LastError = lastErr // a certificate still serves: its status stays
		m.save(ctx, &cp)
		return
	}
	cp.rec.Status, cp.rec.LastError = status, lastErr
	m.save(ctx, &cp)
}

// stepOrder advances an order that waits for DNS: it checks the records
// every minute, and as soon as they are all visible finishes the order.
func (m *Manager) stepOrder(ctx context.Context, key string, force bool) {
	if err := m.stepOrderErr(ctx, key, force); err != nil && !errors.Is(err, errAwaiting) {
		m.failManaged(ctx, key, err, m.isManualOrder(key))
	}
}

func (m *Manager) isManualOrder(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o := m.orders[key]; o != nil {
		for _, c := range o.chals {
			if c.waiting() {
				return true
			}
		}
	}
	return false
}

func (m *Manager) stepOrderErr(ctx context.Context, key string, force bool) error {
	m.mu.Lock()
	o := m.orders[key]
	last := m.stepAt[key]
	m.mu.Unlock()
	if o == nil {
		return nil
	}
	now := m.now()
	if !force && now.Sub(last) < 50*time.Second {
		return nil
	}
	if now.After(o.expires) {
		m.dropOrder(ctx, key, true)
		return errors.New("the order expired before the TXT records were seen: request it again (new records are needed)")
	}
	all := m.checkPresence(ctx, o)
	m.mu.Lock()
	m.stepAt[key] = now
	m.mu.Unlock()
	m.persistOrder(ctx, o)
	m.publishOrder(ctx, key)
	if !all {
		// With every record written by the platform, the CA may still see it
		// before public resolvers do: go on after a few minutes.
		if m.isManualOrder(key) || now.Sub(o.created) < 4*time.Minute {
			return errAwaiting
		}
	}
	return m.completeOrder(ctx, key, o)
}

func (m *Manager) publishOrder(ctx context.Context, key string) {
	m.mu.Lock()
	e := m.cache[key]
	var v View
	if e != nil {
		v = m.viewLocked(ctx, e)
	}
	m.mu.Unlock()
	if e != nil && m.bus != nil {
		m.bus.Publish(TopicUpdated, v)
	}
}

// completeOrder tells the authority the records are there, waits for the
// validation, finalizes and stores the certificate.
func (m *Manager) completeOrder(ctx context.Context, key string, o *orderState) error {
	au, err := m.Authority(ctx, o.authority)
	if err != nil {
		return err
	}
	c, err := m.acme.get(ctx, au)
	if err != nil {
		return err
	}
	m.mu.Lock()
	e := m.cache[key]
	rec := e.rec
	m.mu.Unlock()
	o.state = StatusVerifying
	m.persistOrder(ctx, o)
	m.setStatus(ctx, key, StatusVerifying, "")
	vctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for _, ch := range o.chals {
		chal, err := c.GetChallenge(vctx, ch.ChallengeURI)
		if err != nil {
			m.dropOrder(ctx, key, true)
			return fmt.Errorf("challenge: %w", err)
		}
		if chal.Status == acme.StatusPending {
			if _, err := c.Accept(vctx, chal); err != nil {
				m.dropOrder(ctx, key, true)
				return fmt.Errorf("accept challenge: %w", err)
			}
		}
	}
	for _, ch := range o.chals {
		if _, err := c.WaitAuthorization(vctx, ch.AuthzURI); err != nil {
			m.dropOrder(ctx, key, true)
			return fmt.Errorf("DNS validation of %s: %w", strings.Join(ch.Names, ", "), err)
		}
	}
	res, err := finalize(vctx, c, o.uri, namesOf(rec), firstNonEmpty(rec.KeyType, au.KeyType))
	if err != nil {
		m.dropOrder(ctx, key, true)
		return err
	}
	m.dropOrder(ctx, key, true)
	m.finishIssue(ctx, key, res, au.ID, rec.Method)
	return nil
}

// dropOrder forgets an order and removes the challenge records SynCloud wrote.
func (m *Manager) dropOrder(ctx context.Context, key string, cleanup bool) {
	m.mu.Lock()
	o := m.orders[key]
	delete(m.orders, key)
	delete(m.stepAt, key)
	m.mu.Unlock()
	if o == nil {
		return
	}
	if h := m.dns(); h != nil && cleanup {
		for _, c := range o.chals {
			if c.Auto && c.RecordID != "" {
				if err := h.DeleteTXT(ctx, c.Name, c.RecordID); err != nil {
					m.log.Warn("remove challenge record", "name", c.Name, "err", err)
				}
			}
		}
	}
	if err := m.st.DeleteCertOrder(ctx, key); err != nil {
		m.log.Warn("remove certificate order", "cert", key, "err", err)
	}
}

// finishIssue stores a freshly issued certificate.
func (m *Manager) finishIssue(ctx context.Context, key string, res *issued, authID, method string) {
	m.mu.Lock()
	e := m.cache[key]
	if e == nil {
		m.mu.Unlock()
		return
	}
	cp := *e
	m.mu.Unlock()
	renewed := cp.rec.CertPEM != ""
	cp.rec.CertPEM, cp.keyPEM = string(res.chainPEM), string(res.keyPEM)
	cp.rec.Issuer = IssuerACME
	cp.rec.NotAfter, cp.rec.NotBefore = res.leaf.NotAfter.UTC(), res.leaf.NotBefore.UTC()
	cp.rec.Serial, cp.rec.Fingerprint, cp.rec.IssuerName = fmt.Sprintf("%X", res.leaf.SerialNumber), fingerprint(res.leaf), issuerName(res.leaf)
	cp.rec.KeyType = normKeyType(keyTypeOf(res.leaf.PublicKey))
	cp.rec.Status, cp.rec.LastError, cp.rec.Failures, cp.rec.NextAttemptAt = StatusValid, "", 0, time.Time{}
	if authID != "" {
		cp.rec.AuthorityID = authID
	}
	m.mu.Lock()
	delete(m.renewNow, key)
	m.mu.Unlock()
	m.save(ctx, &cp)
	kind := "issued"
	if renewed {
		kind = "renewed"
	}
	m.event(ctx, key, kind, fmt.Sprintf("by %s, valid until %s", cp.rec.IssuerName, cp.rec.NotAfter.Format("2006-01-02")), "")
	m.log.Info("certificate issued", "cert", key, "names", namesOf(cp.rec), "not_after", cp.rec.NotAfter)
}

// issueOrigin asks a provider (Cloudflare) for an origin certificate.
func (m *Manager) issueOrigin(ctx context.Context, key string, rec store.Certificate, au Authority, names []string) error {
	h := m.dns()
	if h == nil {
		return errors.New("no DNS provider is connected")
	}
	k, keyPEM, err := newKey(KeyECDSA)
	if err != nil {
		return err
	}
	_, csrPEM, err := csrFor(names, k)
	if err != nil {
		return err
	}
	m.mu.Lock()
	days := m.originDays[key]
	m.mu.Unlock()
	certPEM, err := h.OriginCertificate(ctx, au.ProviderID, csrPEM, names, days)
	if err != nil {
		return err
	}
	chain, err := parseChain(certPEM)
	if err != nil {
		return err
	}
	leaf, ordered := orderChain(chain)
	m.finishIssue(ctx, key, &issued{chainPEM: []byte(chainPEM(ordered)), keyPEM: keyPEM, leaf: leaf}, au.ID, "origin-ca")
	return nil
}

// ── what a person does ──────────────────────────────────────────────────────

// Verify checks the order's TXT records now and goes on if they are all there.
func (m *Manager) Verify(ctx context.Context, key, actor string) (View, error) {
	m.mu.Lock()
	e := m.cache[key]
	o := m.orders[key]
	m.mu.Unlock()
	if e == nil {
		return View{}, ErrNotFound
	}
	if o == nil {
		return View{}, conflict("this certificate has no order waiting for DNS")
	}
	m.event(ctx, key, "verify", "checked on request", actor)
	// The order finishes even if the person closes the page meanwhile: a half
	// finished order would leave challenge records and a stale order behind.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	m.stepOrder(dctx, key, true)
	v, _ := m.Get(ctx, key)
	return v, nil
}

// renewManaged starts a new order now (Renew, Retry), dropping an open one.
func (m *Manager) renewManaged(ctx context.Context, key, actor string) error {
	m.mu.Lock()
	e := m.cache[key]
	if e == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	if e.rec.Status == StatusRevoked {
		m.mu.Unlock()
		return conflict("a revoked certificate cannot be renewed: request a new one")
	}
	if e.rec.Source == "imported" {
		m.mu.Unlock()
		return conflict("an imported certificate cannot be renewed here: replace it with a new one from your provider")
	}
	if e.rec.Source == "self-signed" {
		cp := *e
		m.mu.Unlock()
		if err := m.makeSelfSigned(ctx, &cp); err != nil {
			return err
		}
		m.event(ctx, key, "renew", "made again", actor)
		return nil
	}
	m.mu.Unlock()
	m.dropOrder(ctx, key, true)
	m.mu.Lock()
	m.renewNow[key] = true
	if e := m.cache[key]; e != nil {
		e.rec.NextAttemptAt, e.rec.Failures = time.Time{}, 0
	}
	m.mu.Unlock()
	m.event(ctx, key, "renew", "requested", actor)
	m.Kick()
	return nil
}

// RenewNow starts a new order now.
func (m *Manager) RenewNow(ctx context.Context, key, actor string) error {
	return m.renewManaged(ctx, key, actor)
}

// Reissue changes the names and orders again; the old certificate serves until the new one is valid.
func (m *Manager) Reissue(ctx context.Context, key string, names []string, actor string) (View, error) {
	ns, err := normNames(names)
	if err != nil {
		return View{}, err
	}
	m.mu.Lock()
	e := m.cache[key]
	if e == nil || !e.rec.Managed {
		m.mu.Unlock()
		return View{}, ErrNotFound
	}
	cp := *e
	m.mu.Unlock()
	if cp.rec.Source == "imported" || cp.rec.Source == "self-signed" {
		return View{}, conflict("an imported certificate keeps its names: replace it with a new one")
	}
	if cp.rec.Method == "http-01" && hasWildcard(ns) {
		return View{}, invalid("a wildcard name needs DNS validation: request a new certificate with the dns-01 method")
	}
	raw, _ := json.Marshal(ns)
	cp.rec.Names = string(raw)
	m.save(ctx, &cp)
	m.event(ctx, key, "reissue", "names: "+strings.Join(ns, ", "), actor)
	if err := m.renewManaged(ctx, key, actor); err != nil {
		return View{}, err
	}
	v, _ := m.Get(ctx, key)
	return v, nil
}

// UpdateInput changes a certificate's settings; nil fields stay.
type UpdateInput struct {
	AutoRenew *bool
	Authority *string
	KeyType   *string
	Fallback  *string
	AlertDays *[]int
	Pinned    *[]string
}

func (m *Manager) Update(ctx context.Context, key string, in UpdateInput, actor string) (View, error) {
	m.mu.Lock()
	e := m.cache[key]
	if e == nil {
		m.mu.Unlock()
		return View{}, ErrNotFound
	}
	cp := *e
	m.mu.Unlock()
	var changed []string
	if in.AutoRenew != nil {
		cp.rec.AutoRenew = *in.AutoRenew
		changed = append(changed, fmt.Sprintf("auto-renew %v", *in.AutoRenew))
	}
	if in.Authority != nil {
		if cp.rec.Source != "acme" && cp.rec.Source != "origin-ca" {
			return View{}, conflict("only an authority-issued certificate has an authority")
		}
		if _, err := m.Authority(ctx, *in.Authority); err != nil {
			return View{}, invalid("authority: no such certificate authority")
		}
		cp.rec.AuthorityID = *in.Authority
		changed = append(changed, "authority "+*in.Authority+" (from the next issuance)")
	}
	if in.KeyType != nil {
		if !ValidKeyType(*in.KeyType) {
			return View{}, invalid("keyType: one of %s", strings.Join(KeyTypes, ", "))
		}
		cp.rec.KeyType = *in.KeyType
		changed = append(changed, "key type "+*in.KeyType+" (from the next issuance)")
	}
	if in.Fallback != nil {
		if *in.Fallback != "" {
			if _, err := m.Authority(ctx, *in.Fallback); err != nil {
				return View{}, invalid("fallback: no such certificate authority")
			}
		}
		cp.rec.FallbackID = *in.Fallback
		changed = append(changed, "fallback authority")
	}
	if in.AlertDays != nil {
		for _, d := range *in.AlertDays {
			if d < 1 || d > 365 {
				return View{}, invalid("alertDays: whole days from 1 to 365")
			}
		}
		raw, _ := json.Marshal(*in.AlertDays)
		cp.rec.AlertDays = string(raw)
		if len(*in.AlertDays) == 0 {
			cp.rec.AlertDays = ""
		}
		changed = append(changed, "alert days")
	}
	if in.Pinned != nil {
		for _, h := range *in.Pinned {
			if !covers(cp.covers, h) && !covers(namesOf(cp.rec), h) {
				return View{}, invalid("pinnedHosts: %s is not covered by this certificate's names", h)
			}
		}
		raw, _ := json.Marshal(*in.Pinned)
		cp.rec.PinnedHosts = string(raw)
		if len(*in.Pinned) == 0 {
			cp.rec.PinnedHosts = ""
		}
		changed = append(changed, "pinned hosts")
	}
	m.save(ctx, &cp)
	m.event(ctx, key, "settings", strings.Join(changed, ", "), actor)
	v, _ := m.Get(ctx, key)
	return v, nil
}

// Revoke tells the authority the certificate must not be trusted (ACME), or
// for an imported one stops serving it (you tell the provider).
func (m *Manager) Revoke(ctx context.Context, key, actor string) (View, error) {
	m.mu.Lock()
	e := m.cache[key]
	if e == nil {
		m.mu.Unlock()
		return View{}, ErrNotFound
	}
	cp := *e
	m.mu.Unlock()
	if cp.rec.Status == StatusRevoked {
		return View{}, conflict("it is already revoked")
	}
	detail := "stopped serving; revoke it at the provider too"
	if cp.rec.Source == "acme" && cp.rec.CertPEM != "" && cp.rec.Issuer == IssuerACME {
		au, err := m.Authority(ctx, firstNonEmpty(cp.rec.AuthorityID, AuthorityLetsEncrypt))
		if err != nil {
			return View{}, err
		}
		c, err := m.acme.get(ctx, au)
		if err != nil {
			return View{}, err
		}
		b, _ := pem.Decode([]byte(cp.rec.CertPEM))
		if b == nil {
			return View{}, errors.New("the stored certificate cannot be read")
		}
		if err := c.RevokeCert(ctx, nil, b.Bytes, acme.CRLReasonUnspecified); err != nil {
			return View{}, fmt.Errorf("the authority refused the revocation: %w", err)
		}
		detail = "revoked at " + au.Name
	}
	m.dropOrder(ctx, key, true)
	cp.rec.Status, cp.rec.RevokedAt, cp.rec.LastError = StatusRevoked, m.now().UTC(), ""
	cp.rec.AutoRenew = false
	m.save(ctx, &cp)
	m.event(ctx, key, "revoked", detail, actor)
	v, _ := m.Get(ctx, key)
	return v, nil
}

// Delete removes a certificate. A wildcard host that only this certificate
// covers keeps no valid certificate: refused unless force.
func (m *Manager) Delete(ctx context.Context, key string, force bool) error {
	m.mu.Lock()
	e := m.cache[key]
	if e == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	if !e.rec.Managed {
		m.mu.Unlock()
		return conflict("an automatic certificate follows its route: remove the route's domain instead")
	}
	if !force {
		var stranded []string
		for _, h := range m.hosts {
			if sel := m.selectFor(h); sel == e && strings.HasPrefix(h, "*.") {
				stranded = append(stranded, h)
			}
		}
		if len(stranded) > 0 {
			m.mu.Unlock()
			return conflict("%s is served only by this certificate and would be left with a placeholder: pass force to delete it anyway", strings.Join(stranded, ", "))
		}
	}
	m.mu.Unlock()
	m.dropOrder(ctx, key, true)
	if err := m.st.DeleteCertificate(ctx, key); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.cache, key)
	delete(m.renewNow, key)
	m.mu.Unlock()
	m.Kick()
	return nil
}

// Bundle returns the chain and the private key of a certificate.
func (m *Manager) Bundle(ctx context.Context, key, actor string) (chain, privKey string, err error) {
	m.mu.Lock()
	e := m.cache[key]
	m.mu.Unlock()
	if e == nil || e.rec.CertPEM == "" {
		return "", "", ErrNotFound
	}
	m.event(ctx, key, "exported", "the private key was downloaded", actor)
	return e.rec.CertPEM, e.keyPEM, nil
}

// Chain returns the public chain of a certificate (no key).
func (m *Manager) Chain(key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.cache[key]
	if e == nil || e.rec.CertPEM == "" {
		return "", ErrNotFound
	}
	return e.rec.CertPEM, nil
}

// Events returns a certificate's history, newest first.
func (m *Manager) Events(ctx context.Context, key string) ([]store.CertEvent, error) {
	m.mu.Lock()
	_, ok := m.cache[key]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	return m.st.ListCertEvents(ctx, key, 200)
}

// AddEvent lets the API record what a person did.
func (m *Manager) AddEvent(ctx context.Context, key, kind, detail, actor string) {
	m.event(ctx, key, kind, detail, actor)
}

// ── what needs a person's attention (alerts) ────────────────────────────────

// Attention describes a certificate that is about to expire or cannot renew.
type Attention struct {
	Key      string
	Name     string
	DaysLeft float64
	Reason   string
	Alert    bool // a threshold the certificate's own alert days says to alert at
}

// Attentions lists managed certificates that need a person: expiring and
// not renewing by themselves, or failing. Thresholds are each certificate's own alert days.
func (m *Manager) Attentions() []Attention {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Attention
	for k, e := range m.cache {
		r := e.rec
		if !r.Managed || r.Status == StatusRevoked || r.CertPEM == "" {
			continue
		}
		days := r.NotAfter.Sub(now).Hours() / 24
		limit := 0
		for _, d := range alertDaysOf(r) {
			limit = max(limit, d)
		}
		name := namesOf(r)[0]
		selfRenews := r.AutoRenew && r.Source != "imported" && r.Source != "self-signed" && m.orders[k] == nil && r.Status != StatusFailed && r.Status != StatusRateLimited
		switch {
		case r.Status == StatusFailed || r.Status == StatusRateLimited:
			out = append(out, Attention{Key: k, Name: name, DaysLeft: days, Reason: "renewal " + r.Status + ": " + r.LastError, Alert: days <= float64(limit) || days < 30})
		case m.orders[k] != nil && days <= float64(limit):
			out = append(out, Attention{Key: k, Name: name, DaysLeft: days, Reason: "waiting for the TXT records of its renewal", Alert: true})
		case !selfRenews && days <= float64(limit):
			out = append(out, Attention{Key: k, Name: name, DaysLeft: days, Reason: "it does not renew by itself: replace or renew it", Alert: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ── import and requests for a provider to sign ──────────────────────────────

// ImportInput brings a certificate in from a provider.
type ImportInput struct {
	Chain      string // PEM, leaf first or in any order
	Key        string // PEM private key
	Passphrase string
	PKCS12     []byte
	Password   string
	Replace    string // the key of the certificate this replaces
	Actor      string
}

// ImportResult is the imported certificate and what to know about it.
type ImportResult struct {
	View     View     `json:"certificate"`
	Warnings []string `json:"warnings"`
}

// parseBundle checks a chain and its key.
func (m *Manager) parseBundle(chainText, keyText, pass string) (leaf *x509.Certificate, ordered []*x509.Certificate, key string, warnings []string, err error) {
	chain, err := parseChain(chainText)
	if err != nil {
		return nil, nil, "", nil, invalid("%v", err)
	}
	leaf, ordered = orderChain(chain)
	k, err := parseKey(keyText, pass)
	if err != nil {
		return nil, nil, "", nil, invalid("%v", err)
	}
	if !sameKey(k, leaf) {
		return nil, nil, "", nil, invalid("the private key does not belong to the certificate (they must be a pair)")
	}
	now := m.now()
	if now.After(leaf.NotAfter) {
		return nil, nil, "", nil, invalid("the certificate expired on %s", leaf.NotAfter.Format("2006-01-02"))
	}
	if now.Before(leaf.NotBefore.Add(-time.Hour)) {
		warnings = append(warnings, "the certificate is not valid yet: from "+leaf.NotBefore.Format("2006-01-02"))
	}
	if len(certNames(leaf)) == 0 {
		return nil, nil, "", nil, invalid("the certificate names no host")
	}
	// Complete the chain: an intermediate that is missing is fetched from the link inside the certificate.
	inter := x509.NewCertPool()
	for _, c := range ordered[1:] {
		inter.AddCert(c)
	}
	if _, verr := leaf.Verify(x509.VerifyOptions{Intermediates: inter, CurrentTime: now}); verr != nil {
		var unk x509.UnknownAuthorityError
		if errors.As(verr, &unk) && len(ordered) == 1 {
			if more := fetchIntermediates(leaf, m.cfg.HTTPClient); len(more) > 0 {
				ordered = append(ordered, more...)
				inter = x509.NewCertPool()
				for _, c := range ordered[1:] {
					inter.AddCert(c)
				}
				if _, verr = leaf.Verify(x509.VerifyOptions{Intermediates: inter, CurrentTime: now}); verr == nil {
					warnings = append(warnings, "the chain was missing its intermediate certificate: it was fetched from the link inside the certificate")
				}
			}
		}
		if verr != nil {
			if errors.As(verr, &unk) {
				warnings = append(warnings, "the chain is incomplete or its root is not one this server trusts: browsers may refuse the certificate. Add the provider's intermediate certificate to the chain")
			} else {
				warnings = append(warnings, "the chain does not verify: "+verr.Error())
			}
		}
	}
	return leaf, ordered, string(encodeKey(k)), warnings, nil
}

// Import stores a certificate (and its key) from a provider.
func (m *Manager) Import(ctx context.Context, in ImportInput) (ImportResult, error) {
	chainText, keyText := in.Chain, in.Key
	if len(in.PKCS12) > 0 {
		var err error
		if chainText, keyText, err = fromPKCS12(in.PKCS12, in.Password); err != nil {
			return ImportResult{}, invalid("%v", err)
		}
	}
	leaf, ordered, key, warns, err := m.parseBundle(chainText, keyText, in.Passphrase)
	if err != nil {
		return ImportResult{}, err
	}
	names := certNames(leaf)
	for i := range names {
		names[i] = strings.ToLower(names[i])
	}
	var rec store.Certificate
	id := in.Replace
	if id != "" {
		m.mu.Lock()
		e := m.cache[id]
		m.mu.Unlock()
		if e == nil || !e.rec.Managed {
			return ImportResult{}, ErrNotFound
		}
		rec = e.rec
		if !slices.Equal(namesOf(rec), names) {
			warns = append(warns, "the names differ from the certificate it replaces: "+strings.Join(namesOf(rec), ", ")+" → "+strings.Join(names, ", "))
		}
	} else {
		id = newKeyID()
		rec = store.Certificate{Host: id, Managed: true, AutoRenew: false, CreatedAt: m.now().UTC()}
		for _, n := range names {
			if k, ok := m.findByNames([]string{n}); ok {
				warns = append(warns, n+" is also covered by "+k)
			}
		}
	}
	raw, _ := json.Marshal(names)
	zone := ""
	if h := m.dns(); h != nil {
		if z, _, ok := h.ZoneOf(ctx, strings.TrimPrefix(names[0], "*.")); ok {
			zone = z
		}
	}
	rec.Names, rec.Method, rec.Source, rec.Issuer, rec.Zone = string(raw), "import", "imported", IssuerACME, zone
	rec.CertPEM, rec.NotAfter, rec.NotBefore = chainPEM(ordered), leaf.NotAfter.UTC(), leaf.NotBefore.UTC()
	rec.Serial, rec.Fingerprint, rec.IssuerName, rec.KeyType = fmt.Sprintf("%X", leaf.SerialNumber), fingerprint(leaf), issuerName(leaf), normKeyType(keyTypeOf(leaf.PublicKey))
	rec.Status, rec.LastError, rec.Failures, rec.AuthorityID, rec.AutoRenew, rec.RevokedAt = StatusValid, "", 0, "", false, time.Time{}
	m.save(ctx, &entry{rec: rec, keyPEM: key})
	kind := "imported"
	if in.Replace != "" {
		kind = "replaced"
	}
	m.event(ctx, id, kind, fmt.Sprintf("%s, issued by %s, valid until %s", strings.Join(names, ", "), rec.IssuerName, rec.NotAfter.Format("2006-01-02")), in.Actor)
	m.Kick()
	v, _ := m.Get(ctx, id)
	return ImportResult{View: v, Warnings: append([]string{}, warns...)}, nil
}

// RequestView is a certificate request waiting for the provider's answer.
type RequestView struct {
	ID        string    `json:"id"`
	Names     []string  `json:"names"`
	KeyType   string    `json:"keyType"`
	CSR       string    `json:"csr"`
	CreatedAt time.Time `json:"createdAt"`
}

// CreateRequest makes a key and a CSR for names: the CSR goes to the
// provider; the key never leaves.
func (m *Manager) CreateRequest(ctx context.Context, names []string, keyType string) (RequestView, error) {
	ns, err := normNames(names)
	if err != nil {
		return RequestView{}, err
	}
	if !ValidKeyType(keyType) {
		return RequestView{}, invalid("keyType: one of %s", strings.Join(KeyTypes, ", "))
	}
	k, keyPEM, err := newKey(keyType)
	if err != nil {
		return RequestView{}, err
	}
	_, csrPEM, err := csrFor(ns, k)
	if err != nil {
		return RequestView{}, err
	}
	id := "csr_" + strings.TrimPrefix(newKeyID(), "cert_")
	raw, _ := json.Marshal(ns)
	now := m.now().UTC()
	if err := m.st.PutCertRequest(ctx, store.CertRequest{ID: id, Names: string(raw), KeyType: keyType, KeyEnc: m.box.Seal(keyPEM, []byte("certreq:"+id)), CSRPEM: csrPEM, CreatedAt: now}); err != nil {
		return RequestView{}, err
	}
	return RequestView{ID: id, Names: ns, KeyType: normKeyType(keyType), CSR: csrPEM, CreatedAt: now}, nil
}

// ListRequests returns the requests waiting for a certificate.
func (m *Manager) ListRequests(ctx context.Context) ([]RequestView, error) {
	rs, err := m.st.ListCertRequests(ctx)
	if err != nil {
		return nil, err
	}
	out := []RequestView{}
	for _, r := range rs {
		var ns []string
		_ = json.Unmarshal([]byte(r.Names), &ns)
		out = append(out, RequestView{ID: r.ID, Names: ns, KeyType: normKeyType(r.KeyType), CSR: r.CSRPEM, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// CompleteRequest takes the certificate the provider signed for a request.
func (m *Manager) CompleteRequest(ctx context.Context, id, chainText, actor string) (ImportResult, error) {
	r, err := m.st.CertRequest(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ImportResult{}, ErrNotFound
	}
	if err != nil {
		return ImportResult{}, err
	}
	keyPEM, err := m.box.Open(r.KeyEnc, []byte("certreq:"+id))
	if err != nil {
		return ImportResult{}, err
	}
	res, err := m.Import(ctx, ImportInput{Chain: chainText, Key: string(keyPEM), Actor: actor})
	if err != nil {
		return ImportResult{}, err
	}
	_ = m.st.DeleteCertRequest(ctx, id)
	return res, nil
}

// DeleteRequest discards a request (and its key).
func (m *Manager) DeleteRequest(ctx context.Context, id string) error {
	if _, err := m.st.CertRequest(ctx, id); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return m.st.DeleteCertRequest(ctx, id)
}

// makeSelfSigned makes (or remakes) a self-signed certificate for the names
// of e: for private or internal names, never chosen by itself.
func (m *Manager) makeSelfSigned(ctx context.Context, e *entry) error {
	names := namesOf(e.rec)
	k, keyPEM, err := newKey(e.rec.KeyType)
	if err != nil {
		return err
	}
	now := m.now()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: names[0], Organization: []string{"SynCloud (self-signed)"}}, DNSNames: names,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour).UTC().Truncate(time.Second), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, k.Public(), k)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	cp := *e
	cp.rec.CertPEM, cp.keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(keyPEM)
	cp.rec.Issuer, cp.rec.Source, cp.rec.Method = IssuerSelfSigned, "self-signed", "self-signed"
	cp.rec.NotAfter, cp.rec.NotBefore = leaf.NotAfter.UTC(), leaf.NotBefore.UTC()
	cp.rec.Serial, cp.rec.Fingerprint, cp.rec.IssuerName = fmt.Sprintf("%X", serial), fingerprint(leaf), "SynCloud (self-signed)"
	cp.rec.KeyType = normKeyType(keyTypeOf(leaf.PublicKey))
	cp.rec.Status, cp.rec.LastError, cp.rec.Failures = StatusSelfSigned, "", 0
	m.save(ctx, &cp)
	return nil
}
