package agent

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/pki"
)

// certHolder lets the TLS config pick up a renewed certificate.
type certHolder struct {
	mu   sync.Mutex
	cert tls.Certificate
}

func (h *certHolder) set(c tls.Certificate) {
	h.mu.Lock()
	h.cert = c
	h.mu.Unlock()
}

func (h *certHolder) get() *tls.Certificate {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.cert
	return &c
}

func (h *certHolder) notAfter() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cert.Leaf == nil && len(h.cert.Certificate) > 0 {
		h.cert.Leaf, _ = x509.ParseCertificate(h.cert.Certificate[0])
	}
	if h.cert.Leaf == nil {
		return time.Time{}
	}
	return h.cert.Leaf.NotAfter
}

// loadNodeCert loads node.crt/node.key. If a renewal was interrupted between
// the two renames, the new key is paired with node.crt.tmp and finished here.
func loadNodeCert(dataDir string) (tls.Certificate, error) {
	crt, key, tmp := filepath.Join(dataDir, certFile), filepath.Join(dataDir, keyFile), filepath.Join(dataDir, certFile+".tmp")
	cert, err := tls.LoadX509KeyPair(crt, key)
	if err == nil {
		return cert, nil
	}
	if c2, err2 := tls.LoadX509KeyPair(tmp, key); err2 == nil {
		if err := os.Rename(tmp, crt); err != nil {
			return tls.Certificate{}, err
		}
		return c2, nil
	}
	return tls.Certificate{}, err
}

// renewalRequest returns a renewal request when the certificate is due.
func (a *agentLink) renewalRequest() *agentv1.ConnectRequest {
	if a.certs == nil || time.Until(a.certs.notAfter()) > a.renewBefore {
		return nil
	}
	keyPEM, csrPEM, err := pki.NewNodeKey(a.info.Hostname)
	if err != nil {
		a.log.Error("renewal key", "err", err)
		return nil
	}
	a.mu.Lock()
	a.pendingKey = keyPEM
	a.mu.Unlock()
	a.log.Info("renewing node certificate", "expires", a.certs.notAfter())
	return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_RenewCertificate{RenewCertificate: &agentv1.RenewCertificate{Csr: string(csrPEM)}}}
}

// installCertificate saves a renewed certificate with its key and uses it
// from the next connection on.
func (a *agentLink) installCertificate(certPEM string) {
	a.mu.Lock()
	keyPEM := a.pendingKey
	a.pendingKey = nil
	a.mu.Unlock()
	if err := a.saveCert(keyPEM, []byte(certPEM)); err != nil {
		a.log.Error("install renewed certificate", "err", err)
		return
	}
	a.log.Info("node certificate renewed", "expires", a.certs.notAfter())
}

func (a *agentLink) saveCert(keyPEM, certPEM []byte) error {
	if keyPEM == nil {
		return errors.New("no renewal in progress")
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("certificate does not match the key: %w", err)
	}
	dir := a.dataDir
	// Order matters for crash safety: see loadNodeCert.
	steps := []func() error{
		func() error { return os.WriteFile(filepath.Join(dir, keyFile+".tmp"), keyPEM, 0o600) },
		func() error { return os.WriteFile(filepath.Join(dir, certFile+".tmp"), certPEM, 0o644) },
		func() error { return os.Rename(filepath.Join(dir, keyFile+".tmp"), filepath.Join(dir, keyFile)) },
		func() error { return os.Rename(filepath.Join(dir, certFile+".tmp"), filepath.Join(dir, certFile)) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	a.certs.set(cert)
	return nil
}
