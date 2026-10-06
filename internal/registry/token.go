// Package registry issues tokens for the private Docker registry (§5.9) using
// the Docker registry token protocol: clients get a short-lived ES256 JWT from
// the controller, and the registry verifies it against the issuer certificate.
package registry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Service is the registry's token "service" name (aud claim).
	Service = "syncloud-registry"
	// IssuerName is the iss claim the registry expects.
	IssuerName = "syncloud"
	// TokenTTL keeps tokens short-lived; Docker fetches new ones as needed.
	TokenTTL = 5 * time.Minute

	keyFile  = "registry-token.key"
	CertFile = "registry-token.crt"
)

// Access is one granted scope, e.g. {repository, shop/api, [pull push]}.
type Access struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

type Issuer struct {
	key     *ecdsa.PrivateKey
	certDER []byte
	kid     string
	// CertPath is the issuer certificate mounted into the registry container.
	CertPath string
}

// LoadOrCreateIssuer loads the token signing key from dir, creating it once.
func LoadOrCreateIssuer(dir string) (*Issuer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath, certPath := filepath.Join(dir, keyFile), filepath.Join(dir, CertFile)
	keyPEM, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := create(keyPath, certPath); err != nil {
			return nil, err
		}
		keyPEM, err = os.ReadFile(keyPath)
	}
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	cb, _ := pem.Decode(certPEM)
	if kb == nil || cb == nil {
		return nil, errors.New("invalid registry token key or certificate")
	}
	k, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &Issuer{key: k, certDER: cb.Bytes, kid: keyID(&k.PublicKey), CertPath: certPath}, nil
}

func create(keyPath, certPath string) error {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "SynCloud registry token issuer"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return err
	}
	kder, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return err
	}
	// Readable by the registry container's user.
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// keyID is the libtrust-style key ID distribution uses to match "kid" headers:
// base32 of the first 240 bits of the SHA-256 of the DER public key, in 12
// colon-separated groups of 4.
func keyID(pub *ecdsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	sum := sha256.Sum256(der)
	s := strings.TrimRight(base32.StdEncoding.EncodeToString(sum[:30]), "=")
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(s[i:min(i+4, len(s))])
	}
	return b.String()
}

// Issue returns a signed token for subject with the given access.
func (is *Issuer) Issue(subject string, access []Access, now time.Time) (string, error) {
	return is.IssueTTL(subject, access, now, TokenTTL)
}

// IssueTTL is Issue with a custom lifetime (node pull tokens outlive a
// docker login token because large pulls take a while).
func (is *Issuer) IssueTTL(subject string, access []Access, now time.Time, ttl time.Duration) (string, error) {
	if access == nil {
		access = []Access{}
	}
	header := map[string]any{
		"typ": "JWT", "alg": "ES256", "kid": is.kid,
		"x5c": []string{base64.StdEncoding.EncodeToString(is.certDER)},
	}
	jti := make([]byte, 12)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	claims := map[string]any{
		"iss": IssuerName, "sub": subject, "aud": Service,
		"iat": now.Unix(), "nbf": now.Add(-10 * time.Second).Unix(), "exp": now.Add(ttl).Unix(),
		"jti": base64.RawURLEncoding.EncodeToString(jti), "access": access,
	}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, is.key, digest[:])
	if err != nil {
		return "", err
	}
	// JWS ES256 signatures are r||s, each left-padded to 32 bytes.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ParseScope parses "repository:shop/api:pull,push" (names may contain ':' in
// the middle only for registry:catalog:*).
func ParseScope(scope string) (Access, error) {
	first := strings.Index(scope, ":")
	last := strings.LastIndex(scope, ":")
	if first <= 0 || last == first {
		return Access{}, fmt.Errorf("invalid scope %q", scope)
	}
	actions := strings.Split(scope[last+1:], ",")
	return Access{Type: scope[:first], Name: scope[first+1 : last], Actions: actions}, nil
}
