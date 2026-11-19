package certs

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// save stores an entry and tells the dashboard.
func (m *Manager) save(ctx context.Context, e *entry) {
	e.rec.UpdatedAt = m.now().UTC().Truncate(time.Second)
	if e.rec.CreatedAt.IsZero() || e.rec.CreatedAt.Unix() <= 0 {
		e.rec.CreatedAt = e.rec.UpdatedAt
	}
	e.rec.KeyEnc = m.box.Seal([]byte(e.keyPEM), []byte("cert:"+e.rec.Host))
	e.covers = coversOf(e.rec)
	if err := m.st.PutCertificate(ctx, e.rec); err != nil {
		m.log.Error("store certificate", "host", e.rec.Host, "err", err)
	}
	m.mu.Lock()
	m.cache[e.rec.Host] = e
	v := m.viewLocked(ctx, e)
	m.mu.Unlock()
	if m.bus != nil {
		m.bus.Publish(TopicUpdated, v)
	}
}

// event adds an entry to a certificate's history.
func (m *Manager) event(ctx context.Context, key, kind, detail, actor string) {
	if err := m.st.AddCertEvent(ctx, store.CertEvent{CertKey: key, At: m.now().UTC(), Kind: kind, Detail: detail, Actor: actor}); err != nil {
		m.log.Warn("certificate history", "cert", key, "err", err)
	}
}

func (m *Manager) authorityName(ctx context.Context, id string) string {
	if id == "" {
		id = AuthorityLetsEncrypt
	}
	if a, err := m.Authority(ctx, id); err == nil {
		return a.Name
	}
	return id
}

// coversOf lists the names a stored certificate really holds: those in its
// PEM, else the names it was asked for.
func coversOf(r store.Certificate) []string {
	if r.CertPEM != "" {
		if b, _ := pem.Decode([]byte(r.CertPEM)); b != nil {
			if c, err := x509.ParseCertificate(b.Bytes); err == nil {
				if n := certNames(c); len(n) > 0 {
					return n
				}
			}
		}
	}
	return namesOf(r)
}

// nameCovers reports whether a certificate name covers a host: exactly, or
// as a one-label wildcard (*.example.com covers a.example.com, not
// example.com and not a.b.example.com).
func nameCovers(name, host string) bool {
	name, host = strings.ToLower(name), strings.ToLower(host)
	if name == host {
		return true
	}
	if rest, ok := strings.CutPrefix(name, "*."); ok && !strings.HasPrefix(host, "*.") {
		if h, ok := strings.CutSuffix(host, "."+rest); ok && h != "" && !strings.Contains(h, ".") {
			return true
		}
	}
	return false
}

func covers(names []string, host string) bool {
	for _, n := range names {
		if nameCovers(n, host) {
			return true
		}
	}
	return false
}

// usable: it has a certificate, was not revoked and has not expired.
func (m *Manager) usable(e *entry) bool {
	return e.rec.CertPEM != "" && e.rec.Status != StatusRevoked && e.rec.NotAfter.After(m.now())
}

// coveredByManaged reports whether a certificate a person asked for covers host.
func (m *Manager) coveredByManaged(host string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.cache {
		if e.rec.Managed && m.usable(e) && covers(e.covers, host) {
			return true
		}
	}
	return false
}

// rank orders the certificates that could serve a host: pinned, then an
// exact name, then the narrowest wildcard; a real certificate over a
// placeholder; one asked for over an automatic one.
func (m *Manager) rank(e *entry, host string) int {
	r := 0
	switch {
	case slices.Contains(pinnedOf(e.rec), host):
		r = 10000
	case slices.Contains(e.covers, host):
		r = 5000
	default:
		for _, n := range e.covers {
			if nameCovers(n, host) {
				r = max(r, 1000+len(n))
			}
		}
	}
	if e.rec.Issuer != IssuerSelfSigned {
		r += 500
	}
	if e.rec.Managed {
		r += 100
	}
	return r
}

// selectFor returns the certificate that serves host.
func (m *Manager) selectFor(host string) *entry {
	var best *entry
	bestRank, bestUsable := -1, false
	keys := make([]string, 0, len(m.cache))
	for k := range m.cache {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := m.cache[k]
		if e.rec.CertPEM == "" || e.rec.Status == StatusRevoked || !(covers(e.covers, host) || slices.Contains(pinnedOf(e.rec), host)) {
			continue
		}
		u := m.usable(e)
		rk := m.rank(e, host)
		// An expired certificate is never chosen while a valid one covers the name.
		if best == nil || (u && !bestUsable) || (u == bestUsable && (rk > bestRank || rk == bestRank && e.rec.NotAfter.After(best.rec.NotAfter))) {
			best, bestRank, bestUsable = e, rk, u
		}
	}
	return best
}

func pinnedOf(r store.Certificate) []string {
	var out []string
	if r.PinnedHosts != "" {
		_ = json.Unmarshal([]byte(r.PinnedHosts), &out)
	}
	return out
}

func alertDaysOf(r store.Certificate) []int {
	out := []int{30, 14, 7, 1}
	if r.AlertDays != "" {
		var d []int
		if json.Unmarshal([]byte(r.AlertDays), &d) == nil && len(d) > 0 {
			out = d
		}
	}
	return out
}

// Pairs returns the certificates Traefik should serve: for every wanted
// host the one that serves it, and every other usable certificate a person
// asked for (an imported one for names no route lists yet).
func (m *Manager) Pairs() []Pair {
	m.mu.Lock()
	defer m.mu.Unlock()
	chosen := map[string]*entry{}
	for _, h := range m.hosts {
		if e := m.selectFor(h); e != nil {
			chosen[e.rec.Host] = e
		}
	}
	for k, e := range m.cache {
		if !e.rec.Managed || !m.usable(e) {
			continue
		}
		served := false
		for _, h := range m.hosts {
			if covers(e.covers, h) {
				served = true
			}
		}
		if !served {
			chosen[k] = e
		}
	}
	keys := make([]string, 0, len(chosen))
	for k := range chosen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Pair, 0, len(keys))
	for _, k := range keys {
		out = append(out, Pair{CertPEM: chosen[k].rec.CertPEM, KeyPEM: chosen[k].keyPEM})
	}
	return out
}

// PairFor returns the certificate and key that serve host, if there is one.
func (m *Manager) PairFor(host string) (Pair, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.Contains(m.hosts, host) {
		return Pair{}, false
	}
	e := m.selectFor(host)
	if e == nil {
		return Pair{}, false
	}
	return Pair{CertPEM: e.rec.CertPEM, KeyPEM: e.keyPEM}, true
}

// List returns the certificates for the current hosts and every certificate
// a person asked for.
func (m *Manager) List() []View {
	ctx := context.Background()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []View{}
	seen := map[string]bool{}
	for _, h := range m.hosts {
		if e, ok := m.cache[h]; ok {
			out = append(out, m.viewLocked(ctx, e))
			seen[h] = true
		} else if e := m.selectFor(h); e == nil {
			out = append(out, View{Host: h, ID: h, Names: []string{h}, Status: StatusPending, Source: "acme", Method: "http-01", AutoRenew: true, Pinned: []string{}, UsedBy: []string{}, AlertDays: alertDaysOf(store.Certificate{})})
		}
	}
	keys := make([]string, 0, len(m.cache))
	for k := range m.cache {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if e := m.cache[k]; e.rec.Managed && !seen[k] {
			out = append(out, m.viewLocked(ctx, e))
		}
	}
	return out
}

// Get returns one certificate by key (or by host for an automatic one).
func (m *Manager) Get(ctx context.Context, key string) (View, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cache[key]
	if !ok {
		return View{}, false
	}
	return m.viewLocked(ctx, e), true
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() || t.Unix() <= 0 {
		return nil
	}
	u := t.UTC()
	return &u
}

// viewLocked builds the view of an entry. m.mu is held.
func (m *Manager) viewLocked(ctx context.Context, e *entry) View {
	r := e.rec
	names := e.covers
	if r.Managed && len(namesOf(r)) > 0 && (r.CertPEM == "" || r.Source == "acme" || r.Source == "origin-ca") {
		names = namesOf(r) // what was asked for, while an order is open
	}
	if len(names) == 0 {
		names = namesOf(r)
	}
	v := View{Host: r.Host, Issuer: r.Issuer, Status: r.Status, NotAfter: r.NotAfter.UTC(), LastError: r.LastError, Failures: r.Failures, UpdatedAt: r.UpdatedAt,
		ID: r.Host, Names: slices.Clone(names), Source: r.Source, Method: r.Method, Managed: r.Managed, Zone: r.Zone, AuthorityID: r.AuthorityID, FallbackID: r.FallbackID,
		IssuerName: r.IssuerName, KeyType: r.KeyType, AutoRenew: r.AutoRenew, Serial: r.Serial, Fingerprint: r.Fingerprint, AlertDays: alertDaysOf(r),
		Pinned: pinnedOf(r), UsedBy: []string{}}
	if v.Source == "" {
		v.Source = "acme"
	}
	if v.Method == "" {
		v.Method = "http-01"
	}
	if r.Managed {
		v.Host = names[0]
	}
	if v.Pinned == nil {
		v.Pinned = []string{}
	}
	v.Trusted = r.Issuer != IssuerSelfSigned && r.Source != "origin-ca" && r.Status != StatusRevoked
	if a, err := m.authorityLocked(ctx, r); err == nil {
		v.Authority, v.AuthorityID, v.Trusted = a.Name, a.ID, v.Trusted && a.Trusted || r.Source == "imported"
	}
	if r.Source == "imported" {
		v.Authority = ""
	}
	for _, n := range v.Names {
		if strings.HasPrefix(n, "*.") {
			v.Wildcard = true
		}
	}
	v.NextAttemptAt = timePtr(r.NextAttemptAt)
	v.NotBefore, v.RevokedAt, v.CreatedAt = timePtr(r.NotBefore), timePtr(r.RevokedAt), timePtr(r.CreatedAt)
	if r.CertPEM != "" {
		v.DaysLeft = int(r.NotAfter.Sub(m.now()).Hours() / 24)
	}
	for _, h := range m.hosts {
		if sel := m.selectFor(h); sel != nil && sel == e {
			v.UsedBy = append(v.UsedBy, h)
		}
	}
	if o := m.orders[r.Host]; o != nil {
		v.Order = o.view()
	}
	return v
}

// authorityLocked finds a certificate's authority without taking m.mu.
func (m *Manager) authorityLocked(ctx context.Context, r store.Certificate) (Authority, error) {
	id := r.AuthorityID
	if id == "" && r.Source == "acme" {
		id = AuthorityLetsEncrypt
	}
	if id == "" {
		return Authority{}, ErrNotFound
	}
	if a, ok := m.builtin(id); ok {
		return a, nil
	}
	rec, err := m.st.CertAuthority(ctx, id)
	if err != nil {
		return Authority{}, ErrNotFound
	}
	return m.fromRecord(rec), nil
}

// fallbackFor says which authority to try after a failure at the first one
// ("" fallback id means the cluster's): when it was rate limited, or has
// failed repeatedly.
func (m *Manager) fallbackFor(ctx context.Context, key, certFallback string, err error) *Authority {
	id := certFallback
	if id == "" {
		id = m.FallbackAuthorityID(ctx)
	}
	if id == "" {
		return nil
	}
	_, limited := rateLimited(err)
	m.mu.Lock()
	failures := 0
	if e := m.cache[key]; e != nil {
		failures = e.rec.Failures
	}
	m.mu.Unlock()
	if !limited && failures < 2 {
		return nil
	}
	a, aerr := m.Authority(ctx, id)
	if aerr != nil || a.Kind != KindACME {
		return nil
	}
	return &a
}

// noteAuthority remembers which authority issued a certificate.
func (m *Manager) noteAuthority(key, id string) { m.issuedBy.Store(key, id) }

// Plan says how a host gets its certificate.
type Plan struct {
	State         string     `json:"state"` // covered | issuing | automatic | needs_certificate
	CertificateID string     `json:"certificateId,omitempty"`
	Names         []string   `json:"names,omitempty"`
	ValidUntil    *time.Time `json:"validUntil,omitempty"`
	Note          string     `json:"note"`
}

// PlanFor describes the certificate a host has or will get.
func (m *Manager) PlanFor(host string) Plan {
	host = strings.ToLower(host)
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.selectFor(host); e != nil && m.usable(e) && (e.rec.Issuer != IssuerSelfSigned) {
		p := Plan{State: "covered", CertificateID: e.rec.Host, Names: slices.Clone(e.covers), ValidUntil: timePtr(e.rec.NotAfter)}
		if slices.Contains(e.covers, host) {
			p.Note = "its own certificate, valid until " + e.rec.NotAfter.Format("2006-01-02")
		} else {
			p.Note = "covered by " + strings.Join(e.covers, ", ") + ", valid until " + e.rec.NotAfter.Format("2006-01-02")
		}
		return p
	}
	if strings.HasPrefix(host, "*.") {
		return Plan{State: "needs_certificate", Note: "a wildcard host needs a wildcard certificate (DNS validation): request " + host + " under Domains › Certificates. Until then HTTPS uses a placeholder"}
	}
	if e := m.cache[host]; e != nil {
		return Plan{State: "issuing", CertificateID: host, Note: "a certificate is being issued; HTTPS uses a placeholder until DNS points here"}
	}
	return Plan{State: "automatic", Note: "a certificate is issued once DNS points here"}
}
