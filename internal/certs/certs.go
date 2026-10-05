// Package certs keeps a TLS certificate for every public hostname (§5.0.2).
// The controller is the ACME client (HTTP-01 through Traefik) and hands the
// certificates to Traefik in its dynamic config. Until a real certificate is
// issued, or when ACME fails, a self-signed placeholder keeps HTTPS working.
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
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"syncloud/internal/events"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

// Status values.
const (
	StatusValid       = "valid"        // issued by ACME
	StatusPending     = "pending"      // self-signed placeholder, ACME attempt due
	StatusFailed      = "failed"       // ACME failed, retrying with backoff
	StatusRateLimited = "rate_limited" // the CA rate-limited us (§5.0.2)
	StatusSelfSigned  = "self_signed"  // ACME disabled

	IssuerACME       = "acme"
	IssuerSelfSigned = "self-signed"

	// TopicUpdated carries a View whenever a certificate changes.
	TopicUpdated = "certificate.updated"
	// ChallengePrefix is routed from Traefik's HTTP entrypoint to the controller.
	ChallengePrefix = "/.well-known/acme-challenge/"
)

type Config struct {
	// ACME enables issuing from DirectoryURL; otherwise certificates stay self-signed.
	ACME         bool
	DirectoryURL string
	Email        string
	// HTTPClient talks to the ACME server (tests trust Pebble's CA here).
	HTTPClient  *http.Client
	RenewBefore time.Duration // default 30 days
}

// View is a certificate as shown in the API (never the key).
type View struct {
	Host          string     `json:"host"`
	Issuer        string     `json:"issuer"`
	Status        string     `json:"status"`
	NotAfter      time.Time  `json:"notAfter"`
	LastError     string     `json:"lastError"`
	Failures      int        `json:"failures"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// Pair is a PEM certificate chain and key, as Traefik takes them.
type Pair struct{ CertPEM, KeyPEM string }

type entry struct {
	rec    store.Certificate
	keyPEM string
}

// issueFunc obtains a certificate for host; tests replace it.
type issueFunc func(ctx context.Context, host string) (certPEM, keyPEM []byte, notAfter time.Time, err error)

type Manager struct {
	st  *store.Store
	box *secrets.Box
	bus *events.Bus
	log *slog.Logger
	cfg Config
	now func() time.Time

	mu    sync.Mutex
	hosts []string
	cache map[string]*entry
	kick  chan struct{}

	acme       *acmeClient
	issue      issueFunc
	challenges sync.Map // token -> key authorization
}

func New(st *store.Store, box *secrets.Box, bus *events.Bus, log *slog.Logger, cfg Config) *Manager {
	if cfg.RenewBefore == 0 {
		cfg.RenewBefore = 30 * 24 * time.Hour
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	m := &Manager{st: st, box: box, bus: bus, log: log, cfg: cfg, now: time.Now, cache: map[string]*entry{}, kick: make(chan struct{}, 1)}
	m.acme = &acmeClient{m: m}
	m.issue = m.acme.issue
	return m
}

// Load reads stored certificates. Call before serving Traefik config.
func (m *Manager) Load(ctx context.Context) error {
	recs, err := m.st.ListCertificates(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range recs {
		key, err := m.box.Open(r.KeyEnc, []byte("cert:"+r.Host))
		if err != nil {
			m.log.Warn("unreadable certificate key, will reissue", "host", r.Host, "err", err)
			continue
		}
		m.cache[r.Host] = &entry{rec: r, keyPEM: string(key)}
	}
	return nil
}

// SetHosts declares the hostnames that need certificates.
func (m *Manager) SetHosts(hosts []string) {
	hs := slices.Clone(hosts)
	slices.Sort(hs)
	hs = slices.Compact(hs)
	m.mu.Lock()
	m.hosts = hs
	m.mu.Unlock()
	m.Kick()
}

// Kick asks the loop to reconcile now.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// ErrUnknownHost is returned by Renew for hosts that need no certificate.
var ErrUnknownHost = errors.New("no certificate is managed for this host")

// Renew clears any backoff for host and retries ACME now.
func (m *Manager) Renew(ctx context.Context, host string) error {
	m.mu.Lock()
	e, ok := m.cache[host]
	wanted := slices.Contains(m.hosts, host)
	if ok && wanted {
		e.rec.NextAttemptAt = time.Time{}
		e.rec.Failures = 0
		if e.rec.Issuer == IssuerACME {
			e.rec.NotAfter = m.now() // force renewal
		}
	}
	m.mu.Unlock()
	if !wanted {
		return ErrUnknownHost
	}
	m.Kick()
	return nil
}

// Run reconciles on every Kick and every few minutes until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		m.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
		case <-t.C:
		}
	}
}

// Reconcile makes sure every host has a certificate and renews due ones.
func (m *Manager) Reconcile(ctx context.Context) {
	m.mu.Lock()
	hosts := slices.Clone(m.hosts)
	m.mu.Unlock()
	for _, h := range hosts {
		if ctx.Err() != nil {
			return
		}
		m.reconcileHost(ctx, h)
	}
}

func (m *Manager) reconcileHost(ctx context.Context, host string) {
	now := m.now()
	m.mu.Lock()
	var e *entry
	if c := m.cache[host]; c != nil {
		cp := *c
		e = &cp
	}
	m.mu.Unlock()

	if e == nil {
		certPEM, keyPEM, notAfter, err := selfSigned(host, now)
		if err != nil {
			m.log.Error("self-signed certificate", "host", host, "err", err)
			return
		}
		status := StatusSelfSigned
		if m.cfg.ACME {
			status = StatusPending
		}
		e = &entry{rec: store.Certificate{Host: host, Issuer: IssuerSelfSigned, CertPEM: string(certPEM), NotAfter: notAfter, Status: status}, keyPEM: string(keyPEM)}
		m.save(ctx, e)
	}
	if !m.cfg.ACME {
		return
	}
	if e.rec.Issuer == IssuerACME && e.rec.NotAfter.Sub(now) > m.cfg.RenewBefore {
		return
	}
	if now.Before(e.rec.NextAttemptAt) {
		return
	}

	m.log.Info("requesting certificate", "host", host, "directory", m.cfg.DirectoryURL)
	ictx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	certPEM, keyPEM, notAfter, err := m.issue(ictx, host)
	cancel()
	next := *e
	if err != nil {
		next.rec.Failures++
		next.rec.LastError = err.Error()
		next.rec.Status = StatusFailed
		wait := backoff(next.rec.Failures)
		if d, ok := rateLimited(err); ok {
			next.rec.Status = StatusRateLimited
			wait = max(d, time.Hour)
		}
		if e.rec.Issuer == IssuerACME && e.rec.NotAfter.After(now) {
			next.rec.Status = StatusValid // still serving a valid certificate, renewal will be retried
		}
		next.rec.NextAttemptAt = now.Add(wait)
		m.log.Warn("certificate request failed", "host", host, "err", err, "retry_in", wait)
	} else {
		next.rec = store.Certificate{Host: host, Issuer: IssuerACME, CertPEM: string(certPEM), NotAfter: notAfter, Status: StatusValid}
		next.keyPEM = string(keyPEM)
		m.log.Info("certificate issued", "host", host, "not_after", notAfter)
	}
	m.save(ctx, &next)
}

func (m *Manager) save(ctx context.Context, e *entry) {
	e.rec.UpdatedAt = m.now().UTC().Truncate(time.Second)
	e.rec.KeyEnc = m.box.Seal([]byte(e.keyPEM), []byte("cert:"+e.rec.Host))
	if err := m.st.PutCertificate(ctx, e.rec); err != nil {
		m.log.Error("store certificate", "host", e.rec.Host, "err", err)
	}
	m.mu.Lock()
	m.cache[e.rec.Host] = e
	m.mu.Unlock()
	if m.bus != nil {
		m.bus.Publish(TopicUpdated, view(e.rec))
	}
}

func backoff(failures int) time.Duration {
	// The first retry is quick: on first start Traefik may not be serving yet.
	steps := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}
	if failures-1 < len(steps) {
		return steps[max(failures-1, 0)]
	}
	return 12 * time.Hour
}

// List returns the certificates for the current hosts.
func (m *Manager) List() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []View{}
	for _, h := range m.hosts {
		if e, ok := m.cache[h]; ok {
			out = append(out, view(e.rec))
		} else {
			out = append(out, View{Host: h, Status: StatusPending})
		}
	}
	return out
}

func view(r store.Certificate) View {
	v := View{Host: r.Host, Issuer: r.Issuer, Status: r.Status, NotAfter: r.NotAfter.UTC(), LastError: r.LastError, Failures: r.Failures, UpdatedAt: r.UpdatedAt}
	if !r.NextAttemptAt.IsZero() && r.NextAttemptAt.Unix() > 0 {
		t := r.NextAttemptAt.UTC()
		v.NextAttemptAt = &t
	}
	return v
}

// Pairs returns the certificates Traefik should serve.
func (m *Manager) Pairs() []Pair {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Pair, 0, len(m.hosts))
	for _, h := range m.hosts {
		if e, ok := m.cache[h]; ok {
			out = append(out, Pair{CertPEM: e.rec.CertPEM, KeyPEM: e.keyPEM})
		}
	}
	return out
}

// ServeHTTP answers HTTP-01 challenges at /.well-known/acme-challenge/{token}.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, ChallengePrefix)
	v, ok := m.challenges.Load(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(v.(string)))
}

func selfSigned(host string, now time.Time) (certPEM, keyPEM []byte, notAfter time.Time, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	notAfter = now.Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"SynCloud (self-signed)"}},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	kder, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), notAfter, nil
}
