// Package acmesim is a small ACME server (RFC 8555) for tests and labs:
// HTTP-01 and DNS-01, wildcards, external account binding and revocation. It
// does not check signatures; DNS-01 validation asks DNSHas.
package acmesim

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Server is the fake CA.
type Server struct {
	srv  *httptest.Server
	base string // the address the current request came to

	mu       sync.Mutex
	caKey    *ecdsa.PrivateKey
	caCert   *x509.Certificate
	next     int
	orders   map[string]*simOrder
	authzs   map[string]*simAuthz
	certs    map[string][]byte
	eabKids  []string
	revoked  [][]byte
	accounts int

	// DNSHas says whether a TXT record exists at a name (DNS-01 validation).
	DNSHas func(name string) bool
	// FailOrders makes new orders fail with this status (403: rate limited).
	FailOrders int
	newOrders  int
}

type simOrder struct {
	id     string
	idents []string
	authz  []string
	status string
	cert   string
}

type simAuthz struct {
	id, ident string
	wildcard  bool
	status    string
	token     string
	chalType  string
}

// New returns the CA's handler state; serve Handler anywhere.
func New() *Server {
	f := &Server{orders: map[string]*simOrder{}, authzs: map[string]*simAuthz{}, certs: map[string][]byte{}}
	f.caKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake ACME CA", Organization: []string{"Sim"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &f.caKey.PublicKey, f.caKey)
	f.caCert, _ = x509.ParseCertificate(der)
	f.DNSHas = func(string) bool { return true }
	return f
}

// Start serves a new CA on a test server.
func Start(t testing.TB) *Server {
	t.Helper()
	f := New()
	f.srv = httptest.NewServer(http.HandlerFunc(f.ServeHTTP))
	t.Cleanup(f.srv.Close)
	return f
}

// Directory is the ACME directory URL of a Start-ed server.
func (f *Server) Directory() string { return f.srv.URL + "/directory" }

// NewOrders counts the orders made so far.
func (f *Server) NewOrders() int { f.mu.Lock(); defer f.mu.Unlock(); return f.newOrders }

// EABKids lists the key ids accounts were registered with.
func (f *Server) EABKids() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.eabKids...)
}

// Revoked counts the revoked certificates.
func (f *Server) Revoked() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.revoked) }

// SetFail makes new orders fail with a status (0: work again).
func (f *Server) SetFail(status int) { f.mu.Lock(); f.FailOrders = status; f.mu.Unlock() }

func (f *Server) id() string { f.next++; return fmt.Sprint(f.next) }

func (f *Server) reply(w http.ResponseWriter, status int, loc string, v any) {
	w.Header().Set("Replay-Nonce", "n"+fmt.Sprint(time.Now().UnixNano()))
	w.Header().Set("Content-Type", "application/json")
	if loc != "" {
		w.Header().Set("Location", loc)
	}
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// ServeHTTP is the CA.
func (f *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.base = "http://" + r.Host
	base := f.base
	path := r.URL.Path
	if path == "/directory" {
		f.reply(w, 200, "", map[string]string{"newNonce": base + "/nonce", "newAccount": base + "/acct", "newOrder": base + "/order", "revokeCert": base + "/revoke", "keyChange": base + "/kc"})
		return
	}
	if path == "/nonce" {
		f.reply(w, 200, "", nil)
		return
	}
	var jws struct{ Protected, Payload string }
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &jws)
	payload, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
	switch {
	case path == "/acct":
		var p struct {
			EAB *struct{ Protected string } `json:"externalAccountBinding"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.EAB != nil {
			pb, _ := base64.RawURLEncoding.DecodeString(p.EAB.Protected)
			var h struct{ Kid string }
			_ = json.Unmarshal(pb, &h)
			f.eabKids = append(f.eabKids, h.Kid)
		}
		f.accounts++
		f.reply(w, 201, base+"/acct/1", map[string]any{"status": "valid"})
	case path == "/order":
		f.newOrders++
		if f.FailOrders != 0 {
			w.Header().Set("Replay-Nonce", "n")
			w.Header().Set("Content-Type", "application/problem+json")
			if f.FailOrders == 403 {
				w.Header().Set("Retry-After", "7200")
			}
			w.WriteHeader(f.FailOrders)
			_, _ = w.Write([]byte(`{"type":"urn:ietf:params:acme:error:rateLimited","detail":"too many certificates already issued"}`))
			return
		}
		var p struct {
			Identifiers []struct{ Type, Value string }
		}
		_ = json.Unmarshal(payload, &p)
		o := &simOrder{id: f.id(), status: "pending"}
		for _, i := range p.Identifiers {
			o.idents = append(o.idents, i.Value)
			a := &simAuthz{id: f.id(), ident: strings.TrimPrefix(i.Value, "*."), wildcard: strings.HasPrefix(i.Value, "*."), status: "pending", token: "tok" + f.id()}
			f.authzs[a.id] = a
			o.authz = append(o.authz, a.id)
		}
		f.orders[o.id] = o
		f.reply(w, 201, base+"/order/"+o.id, f.orderJSON(o))
	case strings.HasPrefix(path, "/order/"):
		f.reply(w, 200, "", f.orderJSON(f.orders[strings.TrimPrefix(path, "/order/")]))
	case strings.HasPrefix(path, "/authz/"):
		f.reply(w, 200, "", f.authzJSON(f.authzs[strings.TrimPrefix(path, "/authz/")]))
	case strings.HasPrefix(path, "/chal/"):
		id, typ, _ := strings.Cut(strings.TrimPrefix(path, "/chal/"), "/")
		a := f.authzs[id]
		if len(payload) > 0 && string(payload) != "" && a.status == "pending" { // accept
			ok := true
			if typ == "dns-01" {
				ok = f.DNSHas("_acme-challenge." + a.ident)
			}
			a.status = "valid"
			if !ok {
				a.status = "invalid"
			}
		}
		f.reply(w, 200, "", f.chalJSON(a, typ))
	case strings.HasPrefix(path, "/finalize/"):
		o := f.orders[strings.TrimPrefix(path, "/finalize/")]
		var p struct{ CSR string }
		_ = json.Unmarshal(payload, &p)
		der, _ := base64.RawURLEncoding.DecodeString(p.CSR)
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil {
			f.reply(w, 400, "", map[string]string{"detail": err.Error()})
			return
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: csr.DNSNames[0]}, DNSNames: csr.DNSNames,
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		leaf, _ := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, csr.PublicKey, f.caKey)
		chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.caCert.Raw})...)
		f.certs[o.id] = chain
		o.status, o.cert = "valid", base+"/cert/"+o.id
		f.reply(w, 200, base+"/order/"+o.id, f.orderJSON(o))
	case strings.HasPrefix(path, "/cert/"):
		w.Header().Set("Replay-Nonce", "n")
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(f.certs[strings.TrimPrefix(path, "/cert/")])
	case path == "/revoke":
		var p struct{ Certificate string }
		_ = json.Unmarshal(payload, &p)
		der, _ := base64.RawURLEncoding.DecodeString(p.Certificate)
		f.revoked = append(f.revoked, der)
		f.reply(w, 200, "", nil)
	default:
		f.reply(w, 404, "", map[string]string{"detail": "no " + path})
	}
}

func (f *Server) orderJSON(o *simOrder) map[string]any {
	st := o.status
	if st == "pending" {
		ok := true
		for _, id := range o.authz {
			ok = ok && f.authzs[id].status == "valid"
		}
		if ok {
			st = "ready"
		}
		o.status = st
	}
	var ids, az []map[string]string
	_ = ids
	idents := []map[string]string{}
	for _, v := range o.idents {
		idents = append(idents, map[string]string{"type": "dns", "value": v})
	}
	_ = az
	auths := []string{}
	for _, id := range o.authz {
		auths = append(auths, f.base+"/authz/"+id)
	}
	out := map[string]any{"status": st, "expires": time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339), "identifiers": idents, "authorizations": auths, "finalize": f.base + "/finalize/" + o.id}
	if o.cert != "" {
		out["certificate"] = o.cert
	}
	return out
}

func (f *Server) authzJSON(a *simAuthz) map[string]any {
	chals := []map[string]any{
		{"type": "http-01", "url": f.base + "/chal/" + a.id + "/http-01", "token": a.token, "status": a.status},
		{"type": "dns-01", "url": f.base + "/chal/" + a.id + "/dns-01", "token": a.token, "status": a.status},
	}
	out := map[string]any{"status": a.status, "identifier": map[string]string{"type": "dns", "value": a.ident}, "challenges": chals, "expires": time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)}
	if a.wildcard {
		out["wildcard"] = true
	}
	return out
}

func (f *Server) chalJSON(a *simAuthz, typ string) map[string]any {
	return map[string]any{"type": typ, "url": f.base + "/chal/" + a.id + "/" + typ, "token": a.token, "status": a.status}
}
