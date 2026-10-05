package registry

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestIssueVerifiableToken(t *testing.T) {
	is, err := LoadOrCreateIssuer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	tok, err := is.Issue("usr_1", []Access{{Type: "repository", Name: "shop/api", Actions: []string{"pull", "push"}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWS: %q", tok)
	}
	var header struct {
		Alg string   `json:"alg"`
		Kid string   `json:"kid"`
		X5c []string `json:"x5c"`
	}
	decode(t, parts[0], &header)
	if header.Alg != "ES256" || len(header.X5c) != 1 || strings.Count(header.Kid, ":") != 11 {
		t.Fatalf("header: %+v", header)
	}

	// Verify the signature with the public key from the x5c certificate.
	der, _ := base64.StdEncoding.DecodeString(header.X5c[0])
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(cert.PublicKey.(*ecdsa.PublicKey), digest[:], r, s) {
		t.Fatal("signature does not verify")
	}

	var claims struct {
		Iss    string   `json:"iss"`
		Sub    string   `json:"sub"`
		Aud    string   `json:"aud"`
		Exp    int64    `json:"exp"`
		Access []Access `json:"access"`
	}
	decode(t, parts[1], &claims)
	if claims.Iss != IssuerName || claims.Aud != Service || claims.Sub != "usr_1" || claims.Exp != now.Add(TokenTTL).Unix() {
		t.Fatalf("claims: %+v", claims)
	}
	if len(claims.Access) != 1 || claims.Access[0].Name != "shop/api" {
		t.Fatalf("access: %+v", claims.Access)
	}

	// Reloading keeps the same key (the registry container trusts its certificate).
	again, err := LoadOrCreateIssuer(strings.TrimSuffix(is.CertPath, "/"+CertFile))
	if err != nil || again.kid != is.kid {
		t.Fatalf("reload changed the key: %v", err)
	}
}

func TestParseScope(t *testing.T) {
	cases := map[string]Access{
		"repository:shop/api:pull,push": {Type: "repository", Name: "shop/api", Actions: []string{"pull", "push"}},
		"registry:catalog:*":            {Type: "registry", Name: "catalog", Actions: []string{"*"}},
		"repository:host:5000/x:pull":   {Type: "repository", Name: "host:5000/x", Actions: []string{"pull"}},
	}
	for in, want := range cases {
		got, err := ParseScope(in)
		if err != nil || got.Type != want.Type || got.Name != want.Name || strings.Join(got.Actions, ",") != strings.Join(want.Actions, ",") {
			t.Errorf("%s: got %+v err=%v", in, got, err)
		}
	}
	for _, bad := range []string{"", "repository", "repository:x"} {
		if _, err := ParseScope(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func decode(t *testing.T, seg string, v any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}
