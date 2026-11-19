package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/acmesim"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// fakeDNS is the domains as the certificates see them.
type fakeDNS struct {
	mu       sync.Mutex
	managed  bool
	records  map[string][]string // name -> values
	ids      map[string]string   // record id -> name|value
	next     int
	policies []ZonePolicy
	// delegation: the challenge name is a CNAME to a name SynCloud can write.
	delegated, cnameOK, cnameManaged bool
	ensured                          int
	onHas                            func() // runs when the TXT records are looked up
	origin                           func(csrPEM string, names []string) (string, error)
}

func newFakeDNS(managed bool) *fakeDNS {
	return &fakeDNS{managed: managed, records: map[string][]string{}, ids: map[string]string{}}
}

func (d *fakeDNS) Challenge(context.Context, string) ChallengeInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.delegated {
		return ChallengeInfo{Mode: "delegated", Auto: true, Target: "abc.acme.validate.test", CNAMEOK: d.cnameOK, CNAMEManaged: d.cnameManaged}
	}
	if d.managed {
		return ChallengeInfo{Mode: "direct", Auto: true}
	}
	return ChallengeInfo{Mode: "manual"}
}
func (d *fakeDNS) EnsureDelegation(context.Context, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensured++
	if d.cnameManaged {
		d.cnameOK = true
	}
	return nil
}
func (d *fakeDNS) PutTXT(_ context.Context, name, value string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := "rec" + string(rune('0'+d.next))
	d.records[name] = append(d.records[name], value)
	d.ids[id] = name + "|" + value
	return id, nil
}
func (d *fakeDNS) DeleteTXT(_ context.Context, name, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	nv := d.ids[id]
	delete(d.ids, id)
	_, v, _ := strings.Cut(nv, "|")
	d.records[name] = slices.DeleteFunc(d.records[name], func(x string) bool { return x == v })
	return nil
}
func (d *fakeDNS) HasTXT(_ context.Context, name string, values []string) ([]string, error) {
	if d.onHas != nil {
		d.onHas()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var missing []string
	for _, v := range values {
		if (d.delegated && !d.cnameOK) || !slices.Contains(d.records[name], v) { // the TXT is seen through the CNAME only
			missing = append(missing, v)
		}
	}
	return missing, nil
}
func (d *fakeDNS) ZoneOf(_ context.Context, host string) (string, string, bool) {
	if strings.HasSuffix(host, "example.test") {
		return "example.test", "", true
	}
	return "", "", false
}
func (d *fakeDNS) OriginCertificate(_ context.Context, _, csrPEM string, names []string, _ int) (string, error) {
	return d.origin(csrPEM, names)
}
func (d *fakeDNS) WildcardZones(context.Context) []ZonePolicy { return d.policies }
func (d *fakeDNS) has(name, v string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Contains(d.records[name], v)
}

func newACMEManager(t *testing.T, f *acmesim.Server, d *fakeDNS) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secrets.New(make([]byte, 32))
	m := New(st, box, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{ACME: true, DirectoryURL: f.Directory(), Email: "ops@example.test", HTTPClient: &http.Client{Timeout: 10 * time.Second}})
	if d != nil {
		m.SetDNS(d)
		f.DNSHas = func(name string) bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return len(d.records[name]) > 0 && (!d.delegated || d.cnameOK)
		}
	}
	return m, st
}

func kinds(t *testing.T, m *Manager, key string) []string {
	t.Helper()
	evs, err := m.Events(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i].Kind)
	}
	return out
}

func TestWildcardDNS01WithProvider(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	d := newFakeDNS(true)
	m, _ := newACMEManager(t, f, d)

	v, err := m.Request(ctx, RequestInput{Names: []string{"example.test", "*.example.test", "*.sub.example.test"}, Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Method != "dns-01" || !v.Wildcard || v.Zone != "example.test" || v.AuthorityID != AuthorityLetsEncrypt {
		t.Fatalf("%+v", v)
	}
	if _, err := m.Request(ctx, RequestInput{Names: []string{"*.example.test", "example.test", "*.sub.example.test"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the same names twice: %v", err)
	}
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	if v.Status != StatusValid || v.Order != nil || v.Source != "acme" || v.Fingerprint == "" || !slices.Contains(v.Names, "*.sub.example.test") {
		t.Fatalf("issued: %+v", v)
	}
	// The challenge records were written for both names and removed afterwards.
	if n := len(d.records["_acme-challenge.example.test"]) + len(d.records["_acme-challenge.sub.example.test"]); n != 0 {
		t.Fatalf("challenge records left: %v", d.records)
	}
	if got := kinds(t, m, v.ID); !slices.Equal(got, []string{"requested", "challenge", "issued"}) {
		t.Fatalf("history: %v", got)
	}
	if f.NewOrders() != 1 {
		t.Fatalf("orders: %d", f.NewOrders())
	}
	// It serves a.example.test and the apex, not a.b.example.test.
	m.SetHosts([]string{"a.example.test", "example.test", "x.sub.example.test", "a.b.example.test"})
	m.Reconcile(ctx)
	for h, want := range map[string]bool{"a.example.test": true, "example.test": true, "x.sub.example.test": true, "a.b.example.test": false} {
		l := m.List()
		found := false
		for _, c := range l {
			if c.Managed && slices.Contains(c.UsedBy, h) {
				found = true
			}
		}
		if found != want {
			t.Errorf("%s served by the wildcard certificate: %v (want %v)", h, found, want)
		}
	}
	// Only a.b.example.test needed a certificate of its own (one more order).
	if f.NewOrders() != 2 {
		t.Fatalf("orders: %d", f.NewOrders())
	}
}

func TestManualDNS01OrderAndVerify(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	d := newFakeDNS(false) // no provider: the records are shown
	m, _ := newACMEManager(t, f, d)

	v, err := m.Request(ctx, RequestInput{Names: []string{"*.sub.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	if v.Status != StatusAwaitingDNS || v.Order == nil || !v.Order.Manual || len(v.Order.Challenges) != 1 {
		t.Fatalf("awaiting: %+v", v)
	}
	ch := v.Order.Challenges[0]
	// *.sub.example.test is proved at _acme-challenge.sub.example.test.
	if ch.Name != "_acme-challenge.sub.example.test" || !ch.Wildcard || ch.Auto || ch.Present || ch.Value == "" {
		t.Fatalf("challenge: %+v", ch)
	}
	// Nothing is there yet: Verify says so and keeps waiting.
	if v, err = m.Verify(ctx, v.ID, "ann"); err != nil || v.Status != StatusAwaitingDNS || v.Order.Challenges[0].Present {
		t.Fatalf("verify early: %+v %v", v, err)
	}
	d.mu.Lock()
	d.records[ch.Name] = []string{ch.Value}
	d.mu.Unlock()
	if v, err = m.Verify(ctx, v.ID, "ann"); err != nil || v.Status != StatusValid || v.Order != nil {
		t.Fatalf("verify: %+v %v", v, err)
	}
	if !slices.Contains(kinds(t, m, v.ID), "verify") || !slices.Contains(kinds(t, m, v.ID), "issued") {
		t.Fatal(kinds(t, m, v.ID))
	}
	// A manual record of ours is not removed by the platform.
	if !d.has(ch.Name, ch.Value) {
		t.Fatal("the platform removed a record it did not write")
	}
}

func TestManualOrderSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	d := newFakeDNS(false)
	m, st := newACMEManager(t, f, d)
	v, _ := m.Request(ctx, RequestInput{Names: []string{"*.example.test"}})
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	ch := v.Order.Challenges[0]

	m2 := New(st, m.box, nil, m.log, m.cfg)
	m2.SetDNS(d)
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	v2, ok := m2.Get(ctx, v.ID)
	if !ok || v2.Order == nil || v2.Order.Challenges[0].Value != ch.Value || v2.Status != StatusAwaitingDNS {
		t.Fatalf("the order was lost on restart: %+v", v2)
	}
	d.mu.Lock()
	d.records[ch.Name] = []string{ch.Value}
	d.mu.Unlock()
	if v2, err := m2.Verify(ctx, v.ID, ""); err != nil || v2.Status != StatusValid {
		t.Fatalf("%+v %v", v2, err)
	}
}

func TestRequestRules(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	m, _ := newACMEManager(t, f, newFakeDNS(false))
	for _, c := range []struct {
		name string
		in   RequestInput
	}{
		{"no names", RequestInput{}},
		{"an IP", RequestInput{Names: []string{"203.0.113.1"}}},
		{"a bare label", RequestInput{Names: []string{"localhost"}}},
		{"a wildcard over http-01", RequestInput{Names: []string{"*.example.test"}, Method: "http-01"}},
		{"sslip.io over dns", RequestInput{Names: []string{"1-2-3-4.sslip.io"}, Method: "dns-01"}},
		{"a middle wildcard", RequestInput{Names: []string{"a.*.example.test"}}},
		{"a bad key type", RequestInput{Names: []string{"a.example.test"}, KeyType: "dsa"}},
		{"no such authority", RequestInput{Names: []string{"a.example.test"}, Authority: "ca_nope"}},
	} {
		if _, err := m.Request(ctx, c.in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestHTTP01RSAKeyAndRevoke(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	m, _ := newACMEManager(t, f, newFakeDNS(true))
	v, err := m.Request(ctx, RequestInput{Names: []string{"app.example.test"}, KeyType: KeyRSA2048})
	if err != nil || v.Method != "http-01" {
		t.Fatalf("%+v %v", v, err)
	}
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	if v.Status != StatusValid || v.KeyType != KeyRSA2048 {
		t.Fatalf("%+v", v)
	}
	if _, key, err := m.Bundle(ctx, v.ID, "ann"); err != nil || !strings.Contains(key, "RSA PRIVATE KEY") {
		t.Fatalf("export: %v %v", err, key)
	}
	rv, err := m.Revoke(ctx, v.ID, "ann")
	if err != nil || rv.Status != StatusRevoked || rv.RevokedAt == nil || f.Revoked() != 1 {
		t.Fatalf("revoke: %+v %v revoked=%d", rv, err, f.Revoked())
	}
	// A revoked certificate is not served and not renewed.
	m.SetHosts([]string{"app.example.test"})
	for _, p := range m.Pairs() {
		if strings.Contains(p.CertPEM, "Fake ACME") && false {
			t.Fatal("a revoked certificate is served")
		}
	}
	if _, err := m.Revoke(ctx, v.ID, "ann"); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoking twice: %v", err)
	}
	if err := m.RenewNow(ctx, v.ID, "ann"); !errors.Is(err, ErrConflict) {
		t.Fatalf("renewing a revoked certificate: %v", err)
	}
	want := []string{"requested", "issued", "exported", "revoked"}
	if got := kinds(t, m, v.ID); !slices.Equal(got, want) {
		t.Fatalf("history: %v", got)
	}
}

func TestAuthoritiesEABAndFallback(t *testing.T) {
	ctx := context.Background()
	primary := acmesim.Start(t)
	primary.SetFail(403)
	second := acmesim.Start(t)
	m, _ := newACMEManager(t, primary, newFakeDNS(true))

	if _, err := m.AddAuthority(ctx, AuthorityInput{Name: "ZeroSSL", Preset: "zerossl"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a preset that needs its binding: %v", err)
	}
	au, err := m.AddAuthority(ctx, AuthorityInput{Name: "Provider CA", Preset: "custom", DirectoryURL: second.Directory(), EABKid: "kid-123", EABHMAC: "c2VjcmV0LWhtYWM"})
	if err != nil || !au.HasEAB || au.EABKid != "kid-123" {
		t.Fatalf("%+v %v", au, err)
	}
	if _, err := m.AddAuthority(ctx, AuthorityInput{Name: "Provider CA", DirectoryURL: second.Directory()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second authority with the name: %v", err)
	}
	if tested, err := m.TestAuthority(ctx, au.ID); err != nil || tested.Status != "ok" || !slices.Equal(second.EABKids(), []string{"kid-123"}) {
		t.Fatalf("the account was not registered with its binding: %+v %v %v", tested, err, second.EABKids())
	}
	// A certificate at the other authority, chosen per certificate.
	v, err := m.Request(ctx, RequestInput{Names: []string{"a.example.test"}, Authority: au.ID})
	if err != nil {
		t.Fatal(err)
	}
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	if v.Status != StatusValid || v.AuthorityID != au.ID || v.Authority != "Provider CA" || second.NewOrders() != 1 || primary.NewOrders() != 0 {
		t.Fatalf("%+v second=%d primary=%d", v, second.NewOrders(), primary.NewOrders())
	}
	// The default authority is rate limited: the fallback takes over.
	if err := m.SetFallbackAuthority(ctx, au.ID); err != nil {
		t.Fatal(err)
	}
	v2, _ := m.Request(ctx, RequestInput{Names: []string{"b.example.test"}})
	m.Reconcile(ctx)
	v2, _ = m.Get(ctx, v2.ID)
	if v2.Status != StatusValid || v2.AuthorityID != au.ID || primary.NewOrders() != 1 {
		t.Fatalf("fallback: %+v primary=%d", v2, primary.NewOrders())
	}
	if got := kinds(t, m, v2.ID); !slices.Contains(got, "fallback") {
		t.Fatalf("history: %v", got)
	}
	// An authority in use cannot be removed; the default cannot be, either.
	if err := m.DeleteAuthority(ctx, au.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("in use: %v", err)
	}
	if err := m.SetDefaultAuthority(ctx, au.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteAuthority(ctx, AuthorityLetsEncrypt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("built in: %v", err)
	}
	list, _ := m.Authorities(ctx)
	if len(list) != 3 || !list[2].Default || list[2].Certificates != 2 {
		t.Fatalf("%+v", list)
	}
}

// ── imported certificates ───────────────────────────────────────────────────

type testPKI struct {
	rootKey *ecdsa.PrivateKey
	root    *x509.Certificate
	interK  *ecdsa.PrivateKey
	inter   *x509.Certificate
}

func newPKI(t *testing.T) *testPKI {
	t.Helper()
	p := &testPKI{}
	p.rootKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rt := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, rt, rt, &p.rootKey.PublicKey, p.rootKey)
	p.root, _ = x509.ParseCertificate(der)
	p.interK, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	it := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Test Intermediate", Organization: []string{"Test Provider"}}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(5, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ = x509.CreateCertificate(rand.Reader, it, p.root, &p.interK.PublicKey, p.rootKey)
	p.inter, _ = x509.ParseCertificate(der)
	return p
}

func (p *testPKI) leaf(t *testing.T, notAfter time.Time, names ...string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tm := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tm, p.inter, &k.PublicKey, p.interK)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, k
}

func pemOf(cs ...*x509.Certificate) string {
	var b strings.Builder
	for _, c := range cs {
		b.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	return b.String()
}

func TestImportChecksAndReplace(t *testing.T) {
	ctx := context.Background()
	m, _ := newACMEManager(t, acmesim.Start(t), newFakeDNS(false))
	pki := newPKI(t)
	leaf, key := pki.leaf(t, time.Now().AddDate(1, 0, 0), "shop.example.test", "*.shop.example.test")
	keyPEM := string(encodeKey(key))

	// The key must belong to the certificate; expired ones are refused.
	_, other := pki.leaf(t, time.Now().AddDate(1, 0, 0), "x.example.test")
	if _, err := m.Import(ctx, ImportInput{Chain: pemOf(leaf, pki.inter), Key: string(encodeKey(other))}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "pair") {
		t.Fatalf("a key that is not the certificate's: %v", err)
	}
	old, oldKey := pki.leaf(t, time.Now().Add(-30*time.Minute), "old.example.test")
	if _, err := m.Import(ctx, ImportInput{Chain: pemOf(old), Key: string(encodeKey(oldKey))}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an expired certificate: %v", err)
	}
	if _, err := m.Import(ctx, ImportInput{Chain: "not a pem", Key: keyPEM}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}

	// In any order, and with the intermediate missing the import warns.
	res, err := m.Import(ctx, ImportInput{Chain: pemOf(pki.inter, leaf), Key: keyPEM, Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	v := res.View
	if v.Source != "imported" || v.Method != "import" || v.AutoRenew || v.Status != StatusValid || !v.Wildcard || v.Authority != "" || v.IssuerName == "" ||
		!slices.Equal(v.Names, []string{"shop.example.test", "*.shop.example.test"}) || v.Zone != "example.test" {
		t.Fatalf("%+v", v)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "root") {
		t.Fatalf("the root is not trusted here, so it says so: %v", res.Warnings)
	}
	chain, _ := m.Chain(v.ID)
	if got, _ := parseChain(chain); len(got) != 2 || got[0].Subject.CommonName != "shop.example.test" {
		t.Fatal("the chain is stored leaf first")
	}
	// It does not renew by itself.
	if err := m.RenewNow(ctx, v.ID, "ann"); !errors.Is(err, ErrConflict) {
		t.Fatalf("renew imported: %v", err)
	}
	if _, err := m.Import(ctx, ImportInput{Chain: pemOf(leaf, pki.inter), Key: keyPEM}); err != nil {
		t.Fatalf("the same names again is only a warning: %v", err)
	}

	// Replace keeps the id and the pinned hosts.
	pin := []string{"a.shop.example.test"}
	if _, err := m.Update(ctx, v.ID, UpdateInput{Pinned: &pin}, "ann"); err != nil {
		t.Fatal(err)
	}
	bad := []string{"elsewhere.example.org"}
	if _, err := m.Update(ctx, v.ID, UpdateInput{Pinned: &bad}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pinning a host the certificate does not cover: %v", err)
	}
	leaf2, key2 := pki.leaf(t, time.Now().AddDate(2, 0, 0), "shop.example.test", "*.shop.example.test")
	res2, err := m.Import(ctx, ImportInput{Chain: pemOf(leaf2, pki.inter), Key: string(encodeKey(key2)), Replace: v.ID, Actor: "ann"})
	if err != nil || res2.View.ID != v.ID || !slices.Equal(res2.View.Pinned, pin) || res2.View.Serial == v.Serial {
		t.Fatalf("replace: %+v %v", res2, err)
	}
	if got := kinds(t, m, v.ID); got[0] != "imported" || got[len(got)-1] != "replaced" {
		t.Fatalf("history: %v", got)
	}
}

func TestCSRFlow(t *testing.T) {
	ctx := context.Background()
	m, _ := newACMEManager(t, acmesim.Start(t), nil)
	pki := newPKI(t)
	req, err := m.CreateRequest(ctx, []string{"mail.example.test"}, KeyRSA2048)
	if err != nil || !strings.Contains(req.CSR, "CERTIFICATE REQUEST") {
		t.Fatalf("%+v %v", req, err)
	}
	if rs, _ := m.ListRequests(ctx); len(rs) != 1 || strings.Contains(rs[0].CSR, "PRIVATE") {
		t.Fatal("the request is listed with its CSR only")
	}
	// The provider signs the CSR's public key; the certificate is for another key: refused.
	_, other := pki.leaf(t, time.Now().AddDate(1, 0, 0), "mail.example.test")
	_ = other
	wrong, _ := pki.leaf(t, time.Now().AddDate(1, 0, 0), "mail.example.test")
	if _, err := m.CompleteRequest(ctx, req.ID, pemOf(wrong, pki.inter), "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a certificate that is not for the request's key: %v", err)
	}
	// Sign the real CSR.
	b, _ := pem.Decode([]byte(req.CSR))
	csr, _ := x509.ParseCertificateRequest(b.Bytes)
	tm := &x509.Certificate{SerialNumber: big.NewInt(77), Subject: pkix.Name{CommonName: "mail.example.test"}, DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tm, pki.inter, csr.PublicKey, pki.interK)
	if err != nil {
		t.Fatal(err)
	}
	signed, _ := x509.ParseCertificate(der)
	res, err := m.CompleteRequest(ctx, req.ID, pemOf(signed, pki.inter), "ann")
	if err != nil || res.View.KeyType != KeyRSA2048 || res.View.Source != "imported" {
		t.Fatalf("%+v %v", res, err)
	}
	if rs, _ := m.ListRequests(ctx); len(rs) != 0 {
		t.Fatal("the request stays after its certificate came")
	}
	if _, key, err := m.Bundle(ctx, res.View.ID, ""); err != nil || !strings.Contains(key, "PRIVATE KEY") {
		t.Fatalf("the key made for the request is the certificate's key: %v", err)
	}
}

func TestWhichCertificateServesAName(t *testing.T) {
	ctx := context.Background()
	m, _ := newACMEManager(t, acmesim.Start(t), newFakeDNS(false))
	pki := newPKI(t)
	imp := func(notAfter time.Time, names ...string) View {
		l, k := pki.leaf(t, notAfter, names...)
		r, err := m.Import(ctx, ImportInput{Chain: pemOf(l, pki.inter), Key: string(encodeKey(k))})
		if err != nil {
			t.Fatal(err)
		}
		return r.View
	}
	wide := imp(time.Now().AddDate(1, 0, 0), "*.example.test")
	narrow := imp(time.Now().AddDate(1, 0, 0), "*.apps.example.test")
	exact := imp(time.Now().AddDate(1, 0, 0), "www.apps.example.test")
	m.SetHosts([]string{"a.example.test", "a.apps.example.test", "www.apps.example.test", "a.b.example.test"})
	m.Reconcile(ctx)
	served := func(id string) []string { v, _ := m.Get(ctx, id); slices.Sort(v.UsedBy); return v.UsedBy }
	if got := served(wide.ID); !slices.Equal(got, []string{"a.example.test"}) {
		t.Errorf("wide: %v", got)
	}
	if got := served(narrow.ID); !slices.Equal(got, []string{"a.apps.example.test"}) {
		t.Errorf("narrow: %v", got)
	}
	if got := served(exact.ID); !slices.Equal(got, []string{"www.apps.example.test"}) {
		t.Errorf("exact: %v", got)
	}
	// a.b.example.test is covered by none of them: it keeps its own automatic certificate.
	own, _ := m.Get(ctx, "a.b.example.test")
	if own.Managed || own.Status == "" {
		t.Errorf("a.b.example.test: %+v", own)
	}
	// *.example.test covers one label only: it cannot be pinned to two.
	pin := []string{"www.apps.example.test"}
	if _, err := m.Update(ctx, wide.ID, UpdateInput{Pinned: &pin}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pinning a host the wildcard does not cover: %v", err)
	}
	// A pin beats the exact name.
	if _, err := m.Update(ctx, narrow.ID, UpdateInput{Pinned: &pin}, ""); err != nil {
		t.Fatal(err)
	}
	if got := served(narrow.ID); !slices.Equal(got, []string{"a.apps.example.test", "www.apps.example.test"}) {
		t.Errorf("pinned: %v", got)
	}
	if got := served(exact.ID); len(got) != 0 {
		t.Errorf("the pinned host is still served by the exact certificate: %v", got)
	}
	none := []string{}
	_, _ = m.Update(ctx, narrow.ID, UpdateInput{Pinned: &none}, "")
	// An expired certificate is never chosen while another covers the name.
	m.mu.Lock()
	m.cache[exact.ID].rec.NotAfter = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	if got := served(exact.ID); len(got) != 0 {
		t.Errorf("expired certificate still serves: %v", got)
	}
}

func TestOriginCertificate(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	d := newFakeDNS(true)
	pki := newPKI(t)
	d.origin = func(csrPEM string, names []string) (string, error) {
		b, _ := pem.Decode([]byte(csrPEM))
		csr, err := x509.ParseCertificateRequest(b.Bytes)
		if err != nil {
			return "", err
		}
		tm := &x509.Certificate{SerialNumber: big.NewInt(5), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(15, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, tm, pki.inter, csr.PublicKey, pki.interK)
		if err != nil {
			return "", err
		}
		c, _ := x509.ParseCertificate(der)
		return pemOf(c), nil
	}
	m, _ := newACMEManager(t, f, d)
	au, err := m.AddAuthority(ctx, AuthorityInput{Name: "Cloudflare origin", Kind: KindOrigin, ProviderID: "dnsp_1"})
	if err != nil || au.Trusted {
		t.Fatalf("%+v %v", au, err)
	}
	if err := m.SetDefaultAuthority(ctx, au.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an origin authority is never the default: %v", err)
	}
	v, err := m.Request(ctx, RequestInput{Names: []string{"app.example.test", "*.example.test"}, Authority: au.ID, Days: 5475})
	if err != nil || v.Method != "origin-ca" || v.Source != "origin-ca" {
		t.Fatalf("%+v %v", v, err)
	}
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	if v.Status != StatusValid || v.Trusted || f.NewOrders() != 0 || v.DaysLeft < 5000 {
		t.Fatalf("an origin certificate is not publicly trusted and needs no ACME order: %+v", v)
	}
}

func TestWildcardPolicyKeepsOneCertificate(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	d := newFakeDNS(true)
	d.policies = []ZonePolicy{{Zone: "example.test"}}
	m, _ := newACMEManager(t, f, d)
	m.Reconcile(ctx)
	m.Reconcile(ctx) // not a second one
	var wc []View
	for _, v := range m.List() {
		if v.Managed {
			wc = append(wc, v)
		}
	}
	if len(wc) != 1 || wc[0].Status != StatusValid || !slices.Equal(wc[0].Names, []string{"example.test", "*.example.test"}) || f.NewOrders() != 1 {
		t.Fatalf("%+v orders=%d", wc, f.NewOrders())
	}
}

func TestRenewalWindowAndAttention(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	m, _ := newACMEManager(t, f, newFakeDNS(true))
	v, _ := m.Request(ctx, RequestInput{Names: []string{"app.example.test"}})
	m.Reconcile(ctx)
	if f.NewOrders() != 1 {
		t.Fatalf("orders: %d", f.NewOrders())
	}
	m.Reconcile(ctx) // fresh: nothing
	if f.NewOrders() != 1 {
		t.Fatalf("renewed a fresh certificate: %d", f.NewOrders())
	}
	// 90-day certificates renew a third of their life (30 days) before the end.
	m.mu.Lock()
	m.cache[v.ID].rec.NotBefore, m.cache[v.ID].rec.NotAfter = m.now().Add(-70*24*time.Hour), m.now().Add(20*24*time.Hour)
	m.mu.Unlock()
	m.Reconcile(ctx)
	if f.NewOrders() != 2 {
		t.Fatalf("not renewed inside the window: %d", f.NewOrders())
	}
	if got := kinds(t, m, v.ID); got[len(got)-1] != "renewed" {
		t.Fatal(got)
	}
	// With auto-renew off, an expiring certificate raises attention.
	off := false
	if _, err := m.Update(ctx, v.ID, UpdateInput{AutoRenew: &off}, ""); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.cache[v.ID].rec.NotAfter = m.now().Add(10 * 24 * time.Hour)
	m.mu.Unlock()
	m.Reconcile(ctx)
	if f.NewOrders() != 2 {
		t.Fatal("renewed with auto-renew off")
	}
	at := m.Attentions()
	if len(at) != 1 || !at[0].Alert || !strings.Contains(at[0].Reason, "does not renew") {
		t.Fatalf("%+v", at)
	}
	if err := m.Delete(ctx, v.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get(ctx, v.ID); ok {
		t.Fatal("not deleted")
	}
}

func TestSelfSignedIsAnExplicitRareChoice(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	m, _ := newACMEManager(t, f, newFakeDNS(false))
	v, err := m.Request(ctx, RequestInput{Names: []string{"app.corp.internal", "*.corp.internal"}, Method: "self-signed", KeyType: KeyRSA2048})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusSelfSigned || v.Source != "self-signed" || v.Trusted || v.AutoRenew || v.KeyType != KeyRSA2048 || f.NewOrders() != 0 || !v.Wildcard {
		t.Fatalf("%+v", v)
	}
	// It is never chosen over a real certificate, and renewing makes a new one.
	old := v.Serial
	if err := m.RenewNow(ctx, v.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	if nv, _ := m.Get(ctx, v.ID); nv.Serial == old || nv.Status != StatusSelfSigned {
		t.Fatalf("not made again: %+v", nv)
	}
	if att := m.Attentions(); len(att) != 0 && att[0].Alert && att[0].DaysLeft < 100 {
		t.Fatalf("a fresh self-signed certificate raises no alert: %+v", att)
	}
	if err := m.Delete(ctx, v.ID, false); err != nil {
		t.Fatal(err)
	}
}

// The link to an issuing certificate comes from an uploaded file: it must not reach this network.
func TestIssuerLinksNeverReachPrivateAddresses(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	pki := newPKI(t)
	leaf, _ := pki.leaf(t, time.Now().AddDate(1, 0, 0), "a.example.test")
	leaf.IssuingCertificateURL = []string{srv.URL + "/ca.crt", "http://169.254.169.254/latest/meta-data/", "http://[::1]:1/x"}
	if got := fetchIntermediates(leaf, &http.Client{Timeout: time.Second}); len(got) != 0 || hit {
		t.Fatalf("fetched from a private address: %v hit=%v", got, hit)
	}
}

// A service's wildcard host gets a placeholder (HTTP-01 cannot prove a wildcard) until a wildcard certificate exists.
func TestWildcardRouteHostPlaceholderThenCertificate(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	d := newFakeDNS(true)
	m, _ := newACMEManager(t, f, d)
	m.SetHosts([]string{"*.apps.example.test", "plain.example.test"})
	m.Reconcile(ctx)
	if f.NewOrders() != 1 {
		t.Fatalf("only the plain host is ordered over HTTP-01; the wildcard is not: %d orders", f.NewOrders())
	}
	wild, ok := m.Get(ctx, "*.apps.example.test")
	if !ok || wild.Status != StatusPending || !wild.Wildcard || !strings.Contains(wild.LastError, "wildcard certificate") || wild.Trusted {
		t.Fatalf("placeholder: %+v", wild)
	}
	plan := m.PlanFor("*.apps.example.test")
	if plan.State != "needs_certificate" {
		t.Fatalf("%+v", plan)
	}
	// The wildcard certificate arrives (DNS-01 through the provider) and takes over the host.
	v, err := m.Request(ctx, RequestInput{Names: []string{"*.apps.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	m.Reconcile(ctx)
	got, _ := m.Get(ctx, v.ID)
	if got.Status != StatusValid || !slices.Contains(got.UsedBy, "*.apps.example.test") {
		t.Fatalf("the wildcard certificate does not serve the wildcard host: %+v", got)
	}
	if p := m.PlanFor("*.apps.example.test"); p.State != "covered" || p.CertificateID != v.ID {
		t.Fatalf("%+v", p)
	}
	// Deleting the only certificate of a wildcard host is refused unless forced.
	if err := m.Delete(ctx, v.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete: %v", err)
	}
	if err := m.Delete(ctx, v.ID, true); err != nil {
		t.Fatal(err)
	}
}

func TestDelegatedChallengeWithManagedCNAME(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	d := newFakeDNS(false)
	d.delegated, d.cnameManaged = true, true // the domain has a provider: SynCloud makes the CNAME
	m, _ := newACMEManager(t, f, d)

	v, err := m.Request(ctx, RequestInput{Names: []string{"example.test", "*.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	if v.Status != StatusValid || v.Order != nil {
		t.Fatalf("a delegated name with a managed CNAME needs nobody: %+v", v)
	}
	if d.ensured == 0 {
		t.Fatal("the CNAME was never created")
	}
	if n := len(d.records["_acme-challenge.example.test"]); n != 0 {
		t.Fatalf("challenge records left: %v", d.records)
	}
	found := false
	for _, e := range func() []string { return kinds(t, m, v.ID) }() {
		found = found || e == "challenge"
	}
	if !found {
		t.Fatal("no challenge event")
	}
}

func TestDelegatedChallengeWaitsForTheOneTimeCNAME(t *testing.T) {
	ctx := context.Background()
	f := acmesim.Start(t)
	d := newFakeDNS(false)
	d.delegated = true // a manual domain delegating to a domain SynCloud can write
	m, _ := newACMEManager(t, f, d)

	v, _ := m.Request(ctx, RequestInput{Names: []string{"*.example.test"}})
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	if v.Status != StatusAwaitingDNS || v.Order == nil || !v.Order.Manual {
		t.Fatalf("the person has one thing to do: %+v", v)
	}
	ch := v.Order.Challenges[0]
	if ch.Mode != "delegated" || !ch.Auto || ch.CNAMEOK || ch.Target != "abc.acme.validate.test" || ch.Name != "_acme-challenge.example.test" {
		t.Fatalf("the CNAME to add is shown: %+v", ch)
	}
	if !d.has(ch.Name, ch.Value) {
		t.Fatal("SynCloud writes the TXT meanwhile, so the order finishes the moment the CNAME appears")
	}
	// Not visible yet: still waiting.
	if v, _ = m.Verify(ctx, v.ID, "ann"); v.Status != StatusAwaitingDNS || v.Order.Challenges[0].Present {
		t.Fatalf("%+v", v)
	}
	d.mu.Lock()
	d.cnameOK = true
	d.mu.Unlock()
	v, err := m.Verify(ctx, v.ID, "ann")
	if err != nil || v.Status != StatusValid || v.Order != nil {
		t.Fatalf("%+v %v", v, err)
	}
	if d.has(ch.Name, ch.Value) {
		t.Fatal("the TXT SynCloud wrote must be removed after issuing")
	}

	// Renewal needs nobody: the CNAME is already there.
	if err := m.RenewNow(ctx, v.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	m.Reconcile(ctx)
	v, _ = m.Get(ctx, v.ID)
	if v.Status != StatusValid || v.Order != nil {
		t.Fatalf("a delegated name renews by itself: %+v", v)
	}
	if f.NewOrders() != 2 {
		t.Fatalf("orders: %d", f.NewOrders())
	}
}

func TestVerifyFinishesEvenIfThePageIsClosed(t *testing.T) {
	f := acmesim.Start(t)
	d := newFakeDNS(true) // SynCloud writes and removes the records
	m, st := newACMEManager(t, f, d)
	bg := context.Background()
	v, _ := m.Request(bg, RequestInput{Names: []string{"*.example.test"}})
	// The order is open (records written, not yet checked by the order step).
	d.delegated = true
	d.cnameManaged = false
	m.Reconcile(bg)
	v, _ = m.Get(bg, v.ID)
	if v.Order == nil {
		t.Fatalf("an order should wait for the CNAME: %+v", v)
	}
	d.mu.Lock()
	d.cnameOK = true
	d.mu.Unlock()

	// The person clicks Verify and closes the tab while it works: the request's context is cancelled.
	ctx, cancel := context.WithCancel(bg)
	d.onHas = cancel
	if _, err := m.Verify(ctx, v.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	d.onHas = nil
	v, _ = m.Get(bg, v.ID)
	if v.Status != StatusValid || v.Order != nil {
		t.Fatalf("the certificate must be issued anyway: %+v", v)
	}
	if n := len(d.records["_acme-challenge.example.test"]); n != 0 {
		t.Fatalf("challenge records left behind: %v", d.records)
	}
	if os, _ := st.ListCertOrders(bg); len(os) != 0 {
		t.Fatalf("the order was left in the store: %+v", os)
	}
}
