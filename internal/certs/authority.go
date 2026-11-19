package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// Certificate authorities (Phase 28). Let's Encrypt is built in and the
// default, and uses the controller's ACME settings (--acme-directory,
// --acme-email) and the account key it has always used. Others are added by
// the user: any ACME directory (most providers want external account
// binding), or Cloudflare's origin CA.

const (
	AuthorityLetsEncrypt = "letsencrypt"
	AuthorityStaging     = "letsencrypt-staging"

	KindACME   = "acme"
	KindOrigin = "cloudflare-origin"

	settingDefaultAuthority  = "certs.default_authority"
	settingFallbackAuthority = "certs.fallback_authority"

	// StagingDirectory is Let's Encrypt's test CA: its certificates are not trusted.
	StagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// Authority is a certificate authority as shown (never its binding key).
type Authority struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Preset       string    `json:"preset,omitempty"`
	DirectoryURL string    `json:"directoryUrl,omitempty"`
	Email        string    `json:"email,omitempty"`
	EABKid       string    `json:"eabKid,omitempty"`
	HasEAB       bool      `json:"hasEab"`
	KeyType      string    `json:"keyType,omitempty"`
	ProviderID   string    `json:"providerId,omitempty"`
	Status       string    `json:"status"` // ok | new | error
	LastError    string    `json:"lastError,omitempty"`
	Builtin      bool      `json:"builtin"`
	Trusted      bool      `json:"trusted"` // browsers trust what it issues
	Default      bool      `json:"default"`
	Fallback     bool      `json:"fallback"`
	Certificates int       `json:"certificates"`
	CreatedAt    time.Time `json:"createdAt,omitempty"`
}

// Preset is a provider whose directory is known; the user adds only the keys.
type Preset struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DirectoryURL string `json:"directoryUrl"`
	NeedsEAB     bool   `json:"needsEab"`
	Hint         string `json:"hint"`
}

// Presets are the providers the panel offers. Their directory addresses and
// how to get the keys are as the providers publish them; check the
// provider's own page if one stops working.
var Presets = []Preset{
	{"zerossl", "ZeroSSL", "https://acme.zerossl.com/v2/DV90", true, "In the ZeroSSL dashboard, open Developer and create EAB credentials: a key id and an HMAC key."},
	{"google", "Google Trust Services", "https://dv.acme-v02.api.pki.goog/directory", true, "Create an external account key with: gcloud publicca external-account-keys create"},
	{"sslcom", "SSL.com", "https://acme.ssl.com/sslcom-dv-ecc", true, "In your SSL.com account, create an ACME key: it gives a key id (HMAC key id) and an HMAC key."},
	{"buypass", "Buypass Go SSL", "https://api.buypass.com/acme/directory", false, "Needs no keys; certificates last 180 days."},
	{"custom", "Other ACME authority", "", false, "Any CA that speaks ACME: give its directory URL, and the key id and HMAC key if it asks for them."},
}

// AuthorityInput creates or changes an authority. Empty secrets keep the stored ones.
type AuthorityInput struct {
	Name         string
	Preset       string
	DirectoryURL string
	Email        string
	EABKid       string
	EABHMAC      string // base64 or base64url, as providers show it
	KeyType      string
	Kind         string
	ProviderID   string
}

// Errors the API maps.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
)

type wrapErr struct {
	kind error
	msg  string
}

func (e wrapErr) Error() string        { return e.msg }
func (e wrapErr) Is(target error) bool { return target == e.kind }

func invalid(format string, a ...any) error  { return wrapErr{ErrInvalid, fmt.Sprintf(format, a...)} }
func conflict(format string, a ...any) error { return wrapErr{ErrConflict, fmt.Sprintf(format, a...)} }

func (m *Manager) setting(ctx context.Context, key, def string) string {
	if v, ok, err := m.st.GetSetting(ctx, key); err == nil && ok && v != "" {
		return v
	}
	return def
}

// DefaultAuthorityID is the authority new certificates use.
func (m *Manager) DefaultAuthorityID(ctx context.Context) string {
	return m.setting(ctx, settingDefaultAuthority, AuthorityLetsEncrypt)
}

// FallbackAuthorityID is the authority tried when the first one keeps failing ("" = none).
func (m *Manager) FallbackAuthorityID(ctx context.Context) string {
	return m.setting(ctx, settingFallbackAuthority, "")
}

func (m *Manager) builtin(id string) (Authority, bool) {
	switch id {
	case AuthorityLetsEncrypt:
		return Authority{ID: id, Name: "Let's Encrypt", Kind: KindACME, DirectoryURL: m.cfg.DirectoryURL, Email: m.cfg.Email, Builtin: true, Status: "ok", Trusted: true}, true
	case AuthorityStaging:
		return Authority{ID: id, Name: "Let's Encrypt (staging, untrusted)", Kind: KindACME, DirectoryURL: StagingDirectory, Email: m.cfg.Email, Builtin: true, Status: "ok"}, true
	}
	return Authority{}, false
}

func (m *Manager) fromRecord(a store.CertAuthority) Authority {
	return Authority{ID: a.ID, Name: a.Name, Kind: a.Kind, Preset: a.Preset, DirectoryURL: a.DirectoryURL, Email: a.Email, EABKid: a.EABKid,
		HasEAB: len(a.EABHMACEnc) > 0, KeyType: a.KeyType, ProviderID: a.ProviderID, Status: a.Status, LastError: a.LastError,
		Trusted: a.Kind == KindACME, CreatedAt: a.CreatedAt}
}

// Authority returns one authority by id.
func (m *Manager) Authority(ctx context.Context, id string) (Authority, error) {
	if id == "" {
		id = m.DefaultAuthorityID(ctx)
	}
	if a, ok := m.builtin(id); ok {
		a.Default, a.Fallback = id == m.DefaultAuthorityID(ctx), id == m.FallbackAuthorityID(ctx)
		return a, nil
	}
	rec, err := m.st.CertAuthority(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Authority{}, ErrNotFound
	}
	if err != nil {
		return Authority{}, err
	}
	a := m.fromRecord(rec)
	a.Default, a.Fallback = id == m.DefaultAuthorityID(ctx), id == m.FallbackAuthorityID(ctx)
	return a, nil
}

// Authorities lists every authority, built-in ones first.
func (m *Manager) Authorities(ctx context.Context) ([]Authority, error) {
	recs, err := m.st.ListCertAuthorities(ctx)
	if err != nil {
		return nil, err
	}
	def, fb := m.DefaultAuthorityID(ctx), m.FallbackAuthorityID(ctx)
	count := map[string]int{}
	m.mu.Lock()
	for _, e := range m.cache {
		id := e.rec.AuthorityID
		if id == "" && e.rec.Source == "acme" {
			id = AuthorityLetsEncrypt
		}
		count[id]++
	}
	m.mu.Unlock()
	var out []Authority
	for _, id := range []string{AuthorityLetsEncrypt, AuthorityStaging} {
		a, _ := m.builtin(id)
		out = append(out, a)
	}
	for _, r := range recs {
		out = append(out, m.fromRecord(r))
	}
	for i := range out {
		out[i].Default, out[i].Fallback, out[i].Certificates = out[i].ID == def, out[i].ID == fb, count[out[i].ID]
	}
	return out, nil
}

func validDirectory(u string) error {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
		return invalid("directoryUrl: an https:// address, as the authority publishes it")
	}
	return nil
}

func decodeHMAC(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	return nil, invalid("eabHmac: the HMAC key is base64 text, as the provider shows it")
}

func newAuthorityID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "ca_" + hex.EncodeToString(b)
}

// AddAuthority registers an authority. Its account is created at the first use,
// or with TestAuthority.
func (m *Manager) AddAuthority(ctx context.Context, in AuthorityInput) (Authority, error) {
	rec := store.CertAuthority{ID: newAuthorityID(), Kind: KindACME, Status: "new", CreatedAt: m.now().UTC()}
	if err := m.applyAuthority(&rec, in, true); err != nil {
		return Authority{}, err
	}
	if err := m.st.PutCertAuthority(ctx, rec); err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			return Authority{}, conflict("an authority named %s exists", rec.Name)
		}
		return Authority{}, err
	}
	return m.Authority(ctx, rec.ID)
}

func (m *Manager) applyAuthority(rec *store.CertAuthority, in AuthorityInput, create bool) error {
	if in.Name != "" {
		rec.Name = strings.TrimSpace(in.Name)
	}
	if rec.Name == "" || len(rec.Name) > 60 {
		return invalid("name: 1 to 60 characters")
	}
	if !ValidKeyType(in.KeyType) {
		return invalid("keyType: one of %s", strings.Join(KeyTypes, ", "))
	}
	rec.KeyType = in.KeyType
	if create && in.Kind == KindOrigin {
		rec.Kind, rec.ProviderID = KindOrigin, in.ProviderID
		if in.ProviderID == "" {
			return invalid("provider: the DNS provider (Cloudflare) that issues origin certificates")
		}
		rec.Status = "ok"
		return nil
	}
	if rec.Kind == KindOrigin {
		return nil
	}
	if create {
		rec.Preset = in.Preset
		dir := in.DirectoryURL
		if p := presetByID(in.Preset); p != nil && dir == "" {
			dir = p.DirectoryURL
		}
		rec.DirectoryURL = dir
	} else if in.DirectoryURL != "" && in.DirectoryURL != rec.DirectoryURL {
		rec.DirectoryURL = in.DirectoryURL
		rec.AccountKeyEnc, rec.Status = nil, "new" // another authority: another account
	}
	if err := validDirectory(rec.DirectoryURL); err != nil {
		return err
	}
	if in.Email != "" || create {
		rec.Email = strings.TrimSpace(in.Email)
	}
	if in.EABKid != "" || in.EABHMAC != "" {
		if in.EABKid == "" || in.EABHMAC == "" {
			return invalid("the external account binding needs both the key id and the HMAC key")
		}
		key, err := decodeHMAC(in.EABHMAC)
		if err != nil {
			return err
		}
		rec.EABKid, rec.EABHMACEnc = strings.TrimSpace(in.EABKid), m.box.Seal(key, []byte("certauth:"+rec.ID+":eab"))
		rec.AccountKeyEnc, rec.Status = nil, "new" // a new binding makes a new account
	}
	if p := presetByID(rec.Preset); p != nil && p.NeedsEAB && len(rec.EABHMACEnc) == 0 {
		return invalid("%s needs its external account binding: the key id and the HMAC key", p.Name)
	}
	return nil
}

func presetByID(id string) *Preset {
	for i := range Presets {
		if Presets[i].ID == id {
			return &Presets[i]
		}
	}
	return nil
}

// UpdateAuthority changes a user-added authority.
func (m *Manager) UpdateAuthority(ctx context.Context, id string, in AuthorityInput) (Authority, error) {
	if _, ok := m.builtin(id); ok {
		return Authority{}, invalid("a built-in authority cannot be changed (add another one, or set its email with --acme-email)")
	}
	rec, err := m.st.CertAuthority(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Authority{}, ErrNotFound
	}
	if err != nil {
		return Authority{}, err
	}
	if err := m.applyAuthority(&rec, in, false); err != nil {
		return Authority{}, err
	}
	m.acme.forget(id)
	if err := m.st.PutCertAuthority(ctx, rec); err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			return Authority{}, conflict("an authority named %s exists", rec.Name)
		}
		return Authority{}, err
	}
	return m.Authority(ctx, id)
}

// DeleteAuthority removes an authority nobody uses.
func (m *Manager) DeleteAuthority(ctx context.Context, id string) error {
	if _, ok := m.builtin(id); ok {
		return invalid("a built-in authority cannot be removed")
	}
	if _, err := m.st.CertAuthority(ctx, id); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	m.mu.Lock()
	n := 0
	for _, e := range m.cache {
		if e.rec.AuthorityID == id || e.rec.FallbackID == id {
			n++
		}
	}
	m.mu.Unlock()
	if n > 0 {
		return conflict("%d certificate(s) use this authority: move them to another first", n)
	}
	if m.DefaultAuthorityID(ctx) == id {
		return conflict("it is the default authority: choose another default first")
	}
	if m.FallbackAuthorityID(ctx) == id {
		_ = m.st.SetSetting(ctx, settingFallbackAuthority, "")
	}
	m.acme.forget(id)
	return m.st.DeleteCertAuthority(ctx, id)
}

// SetDefaultAuthority chooses the authority new certificates use.
func (m *Manager) SetDefaultAuthority(ctx context.Context, id string) error {
	a, err := m.Authority(ctx, id)
	if err != nil {
		return err
	}
	if a.Kind != KindACME {
		return invalid("the default authority must be an ACME authority (origin certificates are not publicly trusted)")
	}
	return m.st.SetSetting(ctx, settingDefaultAuthority, id)
}

// SetFallbackAuthority chooses the authority tried when the default keeps
// failing or is rate limited ("" turns it off).
func (m *Manager) SetFallbackAuthority(ctx context.Context, id string) error {
	if id != "" {
		a, err := m.Authority(ctx, id)
		if err != nil {
			return err
		}
		if a.Kind != KindACME {
			return invalid("the fallback authority must be an ACME authority")
		}
	}
	return m.st.SetSetting(ctx, settingFallbackAuthority, id)
}

// TestAuthority registers the account (or finds it) and records the result.
func (m *Manager) TestAuthority(ctx context.Context, id string) (Authority, error) {
	a, err := m.Authority(ctx, id)
	if err != nil {
		return Authority{}, err
	}
	if a.Kind == KindOrigin {
		if m.dnsHost == nil {
			return Authority{}, invalid("no DNS provider is connected")
		}
		return a, nil
	}
	m.acme.forget(id)
	_, err = m.acme.get(ctx, a)
	if rec, rerr := m.st.CertAuthority(ctx, id); rerr == nil {
		rec.Status, rec.LastError = "ok", ""
		if err != nil {
			rec.Status, rec.LastError = "error", err.Error()
		}
		_ = m.st.PutCertAuthority(ctx, rec)
	}
	if err != nil {
		return Authority{}, invalid("%v", err)
	}
	return m.Authority(ctx, id)
}

// ── ACME clients, one per authority ─────────────────────────────────────────

type acmeClient struct {
	m *Manager

	mu      sync.Mutex
	clients map[string]*acme.Client
}

func (a *acmeClient) forget(id string) {
	a.mu.Lock()
	delete(a.clients, id)
	a.mu.Unlock()
}

// get returns the registered ACME client of an authority.
func (a *acmeClient) get(ctx context.Context, au Authority) (*acme.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[au.ID]; ok {
		return c, nil
	}
	if au.Kind != KindACME {
		return nil, fmt.Errorf("%s is not an ACME authority", au.Name)
	}
	key, err := a.accountKey(ctx, au)
	if err != nil {
		return nil, err
	}
	c := &acme.Client{Key: key, DirectoryURL: au.DirectoryURL, HTTPClient: a.m.cfg.HTTPClient, UserAgent: "syncloud"}
	acct := &acme.Account{}
	if au.Email != "" {
		acct.Contact = []string{"mailto:" + au.Email}
	}
	if au.HasEAB {
		rec, err := a.m.st.CertAuthority(ctx, au.ID)
		if err != nil {
			return nil, err
		}
		hmacKey, err := a.m.box.Open(rec.EABHMACEnc, []byte("certauth:"+au.ID+":eab"))
		if err != nil {
			return nil, fmt.Errorf("the external account binding cannot be read: %w", err)
		}
		acct.ExternalAccountBinding = &acme.ExternalAccountBinding{KID: au.EABKid, Key: hmacKey}
	}
	if _, err := c.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("register an account at %s: %w", au.Name, err)
	}
	if a.clients == nil {
		a.clients = map[string]*acme.Client{}
	}
	a.clients[au.ID] = c
	return c, nil
}

// accountKey loads an authority's account key, creating it once. Let's
// Encrypt keeps the key it has always had (setting acme.account_key).
func (a *acmeClient) accountKey(ctx context.Context, au Authority) (*ecdsa.PrivateKey, error) {
	st, box := a.m.st, a.m.box
	name, aad := "acme.account_key", []byte(settingAccountKey)
	if au.ID != AuthorityLetsEncrypt {
		name, aad = "acme.account_key."+au.ID, []byte("acme.account_key."+au.ID)
	}
	if au.Builtin {
		if v, ok, err := st.GetSetting(ctx, name); err != nil {
			return nil, err
		} else if ok {
			sealed, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				return nil, err
			}
			der, err := box.Open(sealed, aad)
			if err != nil {
				return nil, err
			}
			return x509.ParseECPrivateKey(der)
		}
	} else {
		rec, err := st.CertAuthority(ctx, au.ID)
		if err != nil {
			return nil, err
		}
		if len(rec.AccountKeyEnc) > 0 {
			der, err := box.Open(rec.AccountKeyEnc, []byte("certauth:"+au.ID+":key"))
			if err != nil {
				return nil, err
			}
			return x509.ParseECPrivateKey(der)
		}
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if au.Builtin {
		return k, st.SetSetting(ctx, name, base64.StdEncoding.EncodeToString(box.Seal(der, aad)))
	}
	rec, err := st.CertAuthority(ctx, au.ID)
	if err != nil {
		return nil, err
	}
	rec.AccountKeyEnc = box.Seal(der, []byte("certauth:"+au.ID+":key"))
	return k, st.PutCertAuthority(ctx, rec)
}
