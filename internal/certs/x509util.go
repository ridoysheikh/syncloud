package certs

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/pkcs12"
)

// Key types.
const (
	KeyECDSA   = "ecdsa-p256"
	KeyRSA2048 = "rsa-2048"
	KeyRSA3072 = "rsa-3072"
	KeyRSA4096 = "rsa-4096"
)

// KeyTypes lists what a certificate's key can be.
var KeyTypes = []string{KeyECDSA, KeyRSA2048, KeyRSA3072, KeyRSA4096}

// ValidKeyType reports whether t is a known key type ("" means the default).
func ValidKeyType(t string) bool { return t == "" || slices.Contains(KeyTypes, t) }

// newKey makes a key of a type ("" = ECDSA P-256) and its PEM.
func newKey(typ string) (crypto.Signer, []byte, error) {
	var k crypto.Signer
	var err error
	switch typ {
	case "", KeyECDSA:
		k, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case KeyRSA2048:
		k, err = rsa.GenerateKey(rand.Reader, 2048)
	case KeyRSA3072:
		k, err = rsa.GenerateKey(rand.Reader, 3072)
	case KeyRSA4096:
		k, err = rsa.GenerateKey(rand.Reader, 4096)
	default:
		return nil, nil, fmt.Errorf("unknown key type %q", typ)
	}
	if err != nil {
		return nil, nil, err
	}
	return k, encodeKey(k), nil
}

func encodeKey(k crypto.Signer) []byte {
	switch v := k.(type) {
	case *ecdsa.PrivateKey:
		der, _ := x509.MarshalECPrivateKey(v)
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	case *rsa.PrivateKey:
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(v)})
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// keyTypeOf names a key's type.
func keyTypeOf(k crypto.PublicKey) string {
	switch v := k.(type) {
	case *ecdsa.PublicKey:
		return "ecdsa-" + strings.ToLower(strings.TrimPrefix(v.Curve.Params().Name, "P-")) // ecdsa-256
	case *rsa.PublicKey:
		return fmt.Sprintf("rsa-%d", v.N.BitLen())
	case ed25519.PublicKey:
		return "ed25519"
	}
	return "unknown"
}

func normKeyType(t string) string {
	if t == "ecdsa-256" || t == "" {
		return KeyECDSA
	}
	return t
}

// parseKey reads a private key in PEM (PKCS#8, PKCS#1 or SEC1), asking for
// the passphrase when it is encrypted. The passphrase is not kept.
func parseKey(pemText, passphrase string) (crypto.Signer, error) {
	rest := []byte(pemText)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return nil, errors.New("no private key found in the PEM")
		}
		if !strings.Contains(b.Type, "PRIVATE KEY") {
			continue
		}
		der := b.Bytes
		//nolint:staticcheck // legacy encrypted PEM (RFC 1423) is what many providers still send
		if x509.IsEncryptedPEMBlock(b) {
			if passphrase == "" {
				return nil, errors.New("the private key is encrypted: its passphrase is needed (it is not stored)")
			}
			var err error
			//nolint:staticcheck
			if der, err = x509.DecryptPEMBlock(b, []byte(passphrase)); err != nil {
				return nil, errors.New("the passphrase does not open the private key")
			}
		} else if b.Type == "ENCRYPTED PRIVATE KEY" {
			return nil, errors.New("PKCS#8 encrypted keys are not supported: decrypt it first (openssl pkcs8 -in key.pem -out key-plain.pem)")
		}
		if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
			if s, ok := k.(crypto.Signer); ok {
				return s, nil
			}
		}
		if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
			return k, nil
		}
		if k, err := x509.ParseECPrivateKey(der); err == nil {
			return k, nil
		}
		return nil, errors.New("the private key is not in a readable format")
	}
}

// parseChain reads certificates in PEM.
func parseChain(pemText string) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := []byte(pemText)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("a certificate in the PEM cannot be read: %w", err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no certificate found in the PEM")
	}
	return out, nil
}

// orderChain puts the leaf first and each issuer after the certificate it
// signed; certificates that do not belong are left out.
func orderChain(certs []*x509.Certificate) (leaf *x509.Certificate, chain []*x509.Certificate) {
	// The leaf is the one that no other certificate in the set issued.
	for _, c := range certs {
		isIssuer := false
		for _, o := range certs {
			if o != c && bytes.Equal(o.RawIssuer, c.RawSubject) && !bytes.Equal(o.RawSubject, o.RawIssuer) && o.CheckSignatureFrom(c) == nil {
				isIssuer = true
				break
			}
		}
		if !isIssuer {
			leaf = c
			break
		}
	}
	if leaf == nil {
		leaf = certs[0]
	}
	chain = []*x509.Certificate{leaf}
	used := map[*x509.Certificate]bool{leaf: true}
	for cur := leaf; ; {
		var next *x509.Certificate
		for _, c := range certs {
			if !used[c] && bytes.Equal(cur.RawIssuer, c.RawSubject) && cur.CheckSignatureFrom(c) == nil {
				next = c
				break
			}
		}
		if next == nil {
			break
		}
		used[next] = true
		chain = append(chain, next)
		cur = next
	}
	return leaf, chain
}

func chainPEM(chain []*x509.Certificate) string {
	var b bytes.Buffer
	for _, c := range chain {
		_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	return b.String()
}

// sameKey reports whether a private key belongs to a certificate.
func sameKey(k crypto.Signer, c *x509.Certificate) bool {
	type eq interface{ Equal(crypto.PublicKey) bool }
	pub, ok := k.Public().(eq)
	return ok && pub.Equal(c.PublicKey)
}

func fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// certNames lists the names a certificate covers (its DNS names, else its common name).
func certNames(c *x509.Certificate) []string {
	if len(c.DNSNames) > 0 {
		return slices.Clone(c.DNSNames)
	}
	if c.Subject.CommonName != "" {
		return []string{c.Subject.CommonName}
	}
	return nil
}

func issuerName(c *x509.Certificate) string {
	if c.Issuer.CommonName != "" {
		if len(c.Issuer.Organization) > 0 && !strings.Contains(c.Issuer.CommonName, c.Issuer.Organization[0]) {
			return c.Issuer.Organization[0] + " " + c.Issuer.CommonName
		}
		return c.Issuer.CommonName
	}
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return ""
}

// csrFor makes a certificate request for names with key.
func csrFor(names []string, key crypto.Signer) (der []byte, pemText string, err error) {
	der, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		DNSNames: names,
	}, key)
	if err != nil {
		return nil, "", err
	}
	return der, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), nil
}

// fetchIntermediates follows the certificate's "issuing certificate" links
// (AIA) for a missing intermediate: one try per link, short, over HTTP.
func fetchIntermediates(leaf *x509.Certificate, hc *http.Client) []*x509.Certificate {
	hc = publicOnly(hc)
	var out []*x509.Certificate
	cur := leaf
	for depth := 0; depth < 3; depth++ {
		var next *x509.Certificate
		for _, u := range cur.IssuingCertificateURL {
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				continue
			}
			c := *hc
			c.Timeout = 5 * time.Second
			resp, err := c.Get(u)
			if err != nil {
				continue
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if cert, err := x509.ParseCertificate(raw); err == nil {
				next = cert
			} else if b, _ := pem.Decode(raw); b != nil {
				if cert, err := x509.ParseCertificate(b.Bytes); err == nil {
					next = cert
				}
			}
			if next != nil {
				break
			}
		}
		if next == nil {
			break
		}
		out = append(out, next)
		cur = next
	}
	return out
}

// publicOnly returns a client that refuses to connect to private, loopback and
// link-local addresses: the link to an issuing certificate comes from an
// uploaded file, so it must not be a way to reach this network.
func publicOnly(base *http.Client) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					return nil, fmt.Errorf("%s is not a public address", ip)
				}
			}
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		Proxy: nil,
	}
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	if base != nil && base.Timeout > 0 && base.Timeout < c.Timeout {
		c.Timeout = base.Timeout
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return c
}

// fromPKCS12 reads a PKCS#12 (.pfx/.p12) bundle into PEM chain and key.
func fromPKCS12(data []byte, password string) (chain, key string, err error) {
	blocks, err := pkcs12.ToPEM(data, password)
	if err != nil {
		return "", "", errors.New("the PKCS#12 file cannot be opened: wrong password, or a format this server cannot read (convert it with openssl pkcs12 -in f.pfx -nodes -out all.pem)")
	}
	var c, k bytes.Buffer
	for _, b := range blocks {
		if b.Type == "CERTIFICATE" {
			_ = pem.Encode(&c, b)
		} else if strings.Contains(b.Type, "PRIVATE KEY") {
			_ = pem.Encode(&k, b)
		}
	}
	return c.String(), k.String(), nil
}

// NamesOfPEM reads the names of the first certificate in a PEM, for a
// permission check before an import is accepted ("" input gives none).
func NamesOfPEM(pemText string) []string {
	chain, err := parseChain(pemText)
	if err != nil {
		return nil
	}
	leaf, _ := orderChain(chain)
	return certNames(leaf)
}
