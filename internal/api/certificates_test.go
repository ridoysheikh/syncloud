package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testPair(t *testing.T, names ...string) (chain, key string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tm := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tm, tm, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
}

func TestCertificatesAPIAndKeyExport(t *testing.T) {
	e := newEnv(t)
	e.setupRoot(t)
	if resp, p := e.do(t, "POST", "/api/v1/dns/providers", map[string]string{"name": "cf", "token": "t"}, nil); resp.StatusCode != 201 {
		t.Fatalf("provider: %d %v", resp.StatusCode, p)
	} else if resp, z := e.do(t, "POST", "/api/v1/domains", map[string]any{"name": "example.com", "provider": p["id"]}, nil); resp.StatusCode != 201 {
		t.Fatalf("domain: %d %v", resp.StatusCode, z)
	}

	chain, key := testPair(t, "shop.example.com", "*.shop.example.com")
	resp, imp := e.do(t, "POST", "/api/v1/certificates/import", map[string]string{"chain": chain, "key": key}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("import: %d %v", resp.StatusCode, imp)
	}
	cert := imp["certificate"].(map[string]any)
	id := cert["id"].(string)
	if cert["source"] != "imported" || cert["wildcard"] != true || cert["zone"] != "example.com" || cert["autoRenew"] != false {
		t.Fatalf("%v", cert)
	}
	if _, leaked := cert["privateKey"]; leaked || strings.Contains(mustJSON(imp), "PRIVATE KEY") {
		t.Fatal("the key came back")
	}
	// A key that is not the certificate's is refused.
	_, otherKey := testPair(t, "x.example.com")
	if resp, body := e.do(t, "POST", "/api/v1/certificates/import", map[string]string{"chain": chain, "key": otherKey}, nil); resp.StatusCode != 400 {
		t.Fatalf("mismatch: %d %v", resp.StatusCode, body)
	}

	// An editor of the domain manages certificates but cannot export the key.
	resp, ed := e.do(t, "POST", "/api/v1/iam/users", map[string]string{"email": "ed@example.com", "name": "Ed", "password": pw}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("user: %d %v", resp.StatusCode, ed)
	}
	if resp, body := e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "user", "principalId": ed["id"].(string), "policy": "DomainEditor:example.com"}, nil); resp.StatusCode != 200 {
		t.Fatalf("attach: %d %v", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "user", "principalId": ed["id"].(string), "policy": "DomainEditor:nope.org"}, nil); resp.StatusCode != 400 {
		t.Fatalf("a template for a domain that is not registered: %d", resp.StatusCode)
	}
	a := e.as(t)
	if resp, _ := a.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "ed@example.com", "password": pw}, nil); resp.StatusCode != 200 {
		t.Fatal("login")
	}
	if resp, body := a.do(t, "GET", "/api/v1/certificates/"+id, nil, nil); resp.StatusCode != 200 {
		t.Fatalf("editor reads: %d %v", resp.StatusCode, body)
	}
	if resp, body := a.do(t, "PUT", "/api/v1/certificates/"+id, map[string]any{"alertDays": []int{45, 10}}, nil); resp.StatusCode != 200 {
		t.Fatalf("editor updates: %d %v", resp.StatusCode, body)
	}
	if resp, body := a.do(t, "GET", "/api/v1/certificates/"+id+"/bundle", nil, nil); resp.StatusCode != 403 {
		t.Fatalf("the editor must not get the private key: %d %v", resp.StatusCode, body)
	}
	// Names outside the domain are not the editor's to certify.
	other, otherK := testPair(t, "app.other.org")
	if resp, body := a.do(t, "POST", "/api/v1/certificates/import", map[string]string{"chain": other, "key": otherK}, nil); resp.StatusCode != 403 {
		t.Fatalf("another domain: %d %v", resp.StatusCode, body)
	}
	if resp, body := a.do(t, "POST", "/api/v1/certificates", map[string]any{"names": []string{"x.other.org"}}, nil); resp.StatusCode != 403 {
		t.Fatalf("another domain's request: %d %v", resp.StatusCode, body)
	}
	if resp, body := a.do(t, "POST", "/api/v1/certificates", map[string]any{"names": []string{"x.example.com"}}, nil); resp.StatusCode != 201 {
		t.Fatalf("the editor's own domain: %d %v", resp.StatusCode, body)
	}
	// ReadOnly cannot export, either; granting the action does it, and it is audited.
	if resp, _ := e.do(t, "GET", "/api/v1/certificates/"+id+"/bundle", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("root exports: %d", resp.StatusCode)
	}
	doc := `{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":"certificate:ExportCertificate","Resource":"srn:syncloud:certificate/example.com/*"}]}`
	_, pol := e.do(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "key-export", "document": json.RawMessage(doc)}, nil)
	e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "user", "principalId": ed["id"].(string), "policy": pol["id"].(string)}, nil)
	resp, bundle := a.do(t, "GET", "/api/v1/certificates/"+id+"/bundle", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(bundle["privateKey"].(string), "PRIVATE KEY") {
		t.Fatalf("granted export: %d", resp.StatusCode)
	}
	resp, events := e.do(t, "GET", "/api/v1/certificates/"+id+"/events", nil, nil)
	var kinds []string
	for _, ev := range events["items"].([]any) {
		kinds = append(kinds, ev.(map[string]any)["kind"].(string)+"/"+ev.(map[string]any)["actor"].(string))
	}
	if resp.StatusCode != 200 || !strings.Contains(strings.Join(kinds, " "), "exported/ed@example.com") {
		t.Fatalf("history: %v", kinds)
	}
	// Authorities: the built-in default is listed; presets are offered; a binding key is never returned.
	resp, as := e.do(t, "GET", "/api/v1/certificate-authorities", nil, nil)
	if resp.StatusCode != 200 || len(as["items"].([]any)) != 2 || len(as["presets"].([]any)) < 4 {
		t.Fatalf("authorities: %d %v", resp.StatusCode, as)
	}
	resp, ca := e.do(t, "POST", "/api/v1/certificate-authorities", map[string]any{"name": "Provider", "directoryUrl": "https://acme.example.net/dir", "eabKid": "kid", "eabHmac": "c2VjcmV0"}, nil)
	if resp.StatusCode != 201 || ca["hasEab"] != true || strings.Contains(mustJSON(ca), "c2VjcmV0") {
		t.Fatalf("add authority: %d %v", resp.StatusCode, ca)
	}
	if resp, _ := a.do(t, "POST", "/api/v1/certificate-authorities", map[string]any{"name": "X", "directoryUrl": "https://x.example/d"}, nil); resp.StatusCode != 403 {
		t.Fatalf("an editor adds an authority: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "DELETE", "/api/v1/certificates/"+id, nil, nil); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
