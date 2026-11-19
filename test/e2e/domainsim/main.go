// Command domainsim is a fake outside world for the end-to-end tests of
// domains and certificates (Phase 28), in one process:
//
//   - the Cloudflare API (zones, DNS records, origin certificates),
//   - a DNS server that answers from the same records (what the controller's
//     resolvers see), and
//   - an ACME CA whose DNS-01 validation looks at those records.
//
// So a TXT record written through "Cloudflare", or put there by hand through
// /sim/records, is exactly what the CA and the lookups see.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/ridoysheikh/syncloud/internal/acmesim"
)

type record struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  *bool  `json:"proxied,omitempty"`
	Priority *int   `json:"priority,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

var (
	mu      sync.Mutex
	zones   = map[string][]record{} // every zone the DNS server answers for
	visible = map[string]bool{}     // the zones the Cloudflare API shows
	next    int
	token   = flag.String("token", "cf-test-token", "the Cloudflare API token that works")
	// forwards: challenge zones SynCloud's own server answers (zone -> its address),
	// reached only while the zone's NS record exists, as on the internet.
	forwards = map[string]string{}
	caKey    *ecdsa.PrivateKey
	caCert   *x509.Certificate
)

// forwardFor returns the address of the challenge server for a name below a
// forwarded zone that is delegated (its NS record exists). The caller holds mu.
func forwardFor(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for fz, addr := range forwards {
		if !strings.HasSuffix(name, "."+fz) {
			continue
		}
		for _, r := range zones[zoneOf(fz)] {
			if r.Type == "NS" && strings.EqualFold(r.Name, fz) {
				return addr, true
			}
		}
	}
	return "", false
}

func zoneOf(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	best := ""
	for z := range zones {
		if (name == z || strings.HasSuffix(name, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best
}

func txtAt(name string) []string {
	mu.Lock()
	defer mu.Unlock()
	return txtFollow(name, 0)
}

// txtFollow returns the TXT values at a name, following a CNAME as the CA's resolver does.
func txtFollow(name string, depth int) []string {
	var out []string
	if addr, ok := forwardFor(name); ok {
		mu.Unlock() // the controller's server answers: do not hold the lock while asking
		defer mu.Lock()
		m := new(mdns.Msg)
		m.SetQuestion(mdns.Fqdn(name), mdns.TypeTXT)
		if in, _, err := new(mdns.Client).Exchange(m, addr); err == nil {
			for _, rr := range in.Answer {
				if t, ok := rr.(*mdns.TXT); ok {
					out = append(out, strings.Join(t.Txt, ""))
				}
			}
		}
		return out
	}
	for _, r := range zones[zoneOf(name)] {
		if !strings.EqualFold(r.Name, name) {
			continue
		}
		switch {
		case r.Type == "TXT":
			out = append(out, strings.Trim(r.Content, `"`))
		case r.Type == "CNAME" && depth < 8:
			out = append(out, txtFollow(strings.ToLower(strings.TrimSuffix(r.Content, ".")), depth+1)...)
		}
	}
	return out
}

func cfReply(w http.ResponseWriter, status int, result any, pages int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"success": status < 300, "errors": []any{}, "result": result}
	if pages > 0 {
		body["result_info"] = map[string]any{"page": 1, "total_pages": pages}
	}
	_ = json.NewEncoder(w).Encode(body)
}

func cfError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 1000, "message": msg}}, "result": nil})
}

func cloudflare(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+*token {
		cfError(w, http.StatusForbidden, "Invalid access token")
		return
	}
	mu.Lock()
	defer mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/client/v4")
	switch {
	case p == "/zones" && r.Method == "GET":
		var out []map[string]any
		for z := range zones {
			if visible[z] {
				out = append(out, map[string]any{"id": "zone-" + z, "name": z, "status": "active", "name_servers": []string{"ada.ns.cloudflare.test", "bob.ns.cloudflare.test"}})
			}
		}
		cfReply(w, 200, out, 1)
	case p == "/certificates" && r.Method == "POST":
		var in struct {
			CSR       string   `json:"csr"`
			Hostnames []string `json:"hostnames"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		b, _ := pem.Decode([]byte(in.CSR))
		if b == nil {
			cfError(w, 400, "bad csr")
			return
		}
		csr, err := x509.ParseCertificateRequest(b.Bytes)
		if err != nil {
			cfError(w, 400, err.Error())
			return
		}
		tm := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: in.Hostnames[0], Organization: []string{"CloudFlare Origin SSL Certificate Authority"}},
			DNSNames: in.Hostnames, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(15, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, tm, caCert, csr.PublicKey, caKey)
		if err != nil {
			cfError(w, 500, err.Error())
			return
		}
		cfReply(w, 200, map[string]string{"certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}, 0)
	case strings.HasPrefix(p, "/zones/"):
		parts := strings.Split(strings.TrimPrefix(p, "/zones/"), "/")
		zone := strings.TrimPrefix(parts[0], "zone-")
		if !visible[zone] {
			cfError(w, 404, "zone not found")
			return
		}
		if len(parts) < 2 || parts[1] != "dns_records" {
			cfError(w, 404, "not found")
			return
		}
		switch {
		case len(parts) == 2 && r.Method == "GET":
			cfReply(w, 200, zones[zone], 1)
		case len(parts) == 2 && r.Method == "POST":
			var rec record
			_ = json.NewDecoder(r.Body).Decode(&rec)
			if rec.Type == "" || rec.Name == "" || rec.Content == "" {
				cfError(w, 400, "type, name and content are required")
				return
			}
			for _, o := range zones[zone] {
				if strings.EqualFold(o.Name, rec.Name) && o.Type == rec.Type && o.Content == rec.Content {
					cfError(w, 400, "An identical record already exists.")
					return
				}
				if strings.EqualFold(o.Name, rec.Name) && ((o.Type == "CNAME") != (rec.Type == "CNAME")) {
					cfError(w, 400, "A CNAME record cannot be created beside other records of that name.")
					return
				}
			}
			next++
			rec.ID, rec.Name = "r"+strconv.Itoa(next), strings.ToLower(rec.Name)
			zones[zone] = append(zones[zone], rec)
			cfReply(w, 200, rec, 0)
		case len(parts) == 3 && r.Method == "PUT":
			var rec record
			_ = json.NewDecoder(r.Body).Decode(&rec)
			for i := range zones[zone] {
				if zones[zone][i].ID == parts[2] {
					rec.ID, rec.Name = parts[2], strings.ToLower(rec.Name)
					zones[zone][i] = rec
					cfReply(w, 200, rec, 0)
					return
				}
			}
			cfError(w, 404, "Record does not exist.")
		case len(parts) == 3 && r.Method == "DELETE":
			for i := range zones[zone] {
				if zones[zone][i].ID == parts[2] {
					zones[zone] = append(zones[zone][:i], zones[zone][i+1:]...)
					cfReply(w, 200, map[string]string{"id": parts[2]}, 0)
					return
				}
			}
			cfError(w, 404, "Record does not exist.")
		default:
			cfError(w, 404, "not found")
		}
	default:
		cfError(w, 404, "not found")
	}
}

// sim: control endpoints for the tests.
func sim(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	switch {
	case r.URL.Path == "/sim/records" && r.Method == "POST":
		var in struct {
			Zone string `json:"zone"`
			record
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if _, ok := zones[in.Zone]; !ok {
			http.Error(w, "no such zone", 404)
			return
		}
		next++
		in.record.ID, in.record.Name = "r"+strconv.Itoa(next), strings.ToLower(in.record.Name)
		zones[in.Zone] = append(zones[in.Zone], in.record)
		_ = json.NewEncoder(w).Encode(in.record)
	case r.URL.Path == "/sim/records" && r.Method == "GET":
		out := map[string][]record{}
		for z, rs := range zones {
			out[z] = append([]record{}, rs...)
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/sim/records" && r.Method == "DELETE":
		z, id := r.URL.Query().Get("zone"), r.URL.Query().Get("id")
		for i := range zones[z] {
			if zones[z][i].ID == id {
				zones[z] = append(zones[z][:i], zones[z][i+1:]...)
			}
		}
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

func dnsHandler(w mdns.ResponseWriter, q *mdns.Msg) {
	m := new(mdns.Msg)
	m.SetReply(q)
	m.Authoritative = true
	if len(q.Question) == 0 {
		_ = w.WriteMsg(m)
		return
	}
	mu.Lock()
	qname := strings.ToLower(strings.TrimSuffix(q.Question[0].Name, "."))
	if addr, ok := forwardFor(qname); ok {
		mu.Unlock()
		if in, _, err := new(mdns.Client).Exchange(q, addr); err == nil {
			in.Id = q.Id
			_ = w.WriteMsg(in)
			return
		}
		m.Rcode = mdns.RcodeServerFailure
		_ = w.WriteMsg(m)
		return
	}
	rrs, found := answers(qname, q.Question[0].Qtype, q.Question[0].Name, 0)
	mu.Unlock()
	m.Answer = rrs
	if !found {
		m.Rcode = mdns.RcodeNameError
	}
	_ = w.WriteMsg(m)
}

// answers collects the records of one name for a type; a CNAME on the name
// is returned with the answers at its target, as a recursive resolver does.
// The caller holds mu.
func answers(qn string, qtype uint16, owner string, depth int) (out []mdns.RR, found bool) {
	z := zoneOf(qn)
	var cname *record
	for i, r := range zones[z] {
		name := strings.ToLower(r.Name)
		// A wildcard record answers one or more labels below it, as DNS does.
		match := name == qn || (strings.HasPrefix(name, "*.") && strings.HasSuffix(qn, name[1:]))
		if !match {
			continue
		}
		found = true
		hdr := mdns.RR_Header{Name: owner, Class: mdns.ClassINET, Ttl: 60}
		var rr mdns.RR
		switch {
		case r.Type == "A" && qtype == mdns.TypeA:
			rr = &mdns.A{Hdr: withType(hdr, mdns.TypeA), A: net.ParseIP(r.Content)}
		case r.Type == "AAAA" && qtype == mdns.TypeAAAA:
			rr = &mdns.AAAA{Hdr: withType(hdr, mdns.TypeAAAA), AAAA: net.ParseIP(r.Content)}
		case r.Type == "NS" && qtype == mdns.TypeNS:
			rr = &mdns.NS{Hdr: withType(hdr, mdns.TypeNS), Ns: mdns.Fqdn(r.Content)}
		case r.Type == "TXT" && qtype == mdns.TypeTXT:
			rr = &mdns.TXT{Hdr: withType(hdr, mdns.TypeTXT), Txt: []string{strings.Trim(r.Content, `"`)}}
		case r.Type == "CNAME":
			if qtype == mdns.TypeCNAME {
				rr = &mdns.CNAME{Hdr: withType(hdr, mdns.TypeCNAME), Target: mdns.Fqdn(r.Content)}
			} else {
				cname = &zones[z][i]
			}
		}
		if rr != nil {
			out = append(out, rr)
		}
	}
	if cname != nil && depth < 8 {
		to := strings.ToLower(strings.TrimSuffix(cname.Content, "."))
		out = append(out, &mdns.CNAME{Hdr: withType(mdns.RR_Header{Name: owner, Class: mdns.ClassINET, Ttl: 60}, mdns.TypeCNAME), Target: mdns.Fqdn(to)})
		if addr, ok := forwardFor(to); ok {
			// The target is in a zone SynCloud's own server answers: ask it, as a resolver restarts at the target.
			m := new(mdns.Msg)
			m.SetQuestion(mdns.Fqdn(to), qtype)
			if in, _, err := new(mdns.Client).Exchange(m, addr); err == nil {
				out = append(out, in.Answer...)
			}
		} else {
			more, _ := answers(to, qtype, mdns.Fqdn(to), depth+1)
			out = append(out, more...)
		}
	}
	return out, found
}

func withType(h mdns.RR_Header, t uint16) mdns.RR_Header { h.Rrtype = t; return h }

func main() {
	cfAddr := flag.String("cloudflare", "127.0.0.1:9100", "Cloudflare API address")
	acmeAddr := flag.String("acme", "127.0.0.1:9101", "ACME CA address")
	dnsAddr := flag.String("dns", "127.0.0.1:9153", "DNS server address (udp and tcp)")
	controlAddr := flag.String("control", "127.0.0.1:9102", "control endpoints (/sim/records)")
	cfZones := flag.String("cf-zones", "example.test,apps.test", "zones the Cloudflare token can see")
	forward := flag.String("forward", "acme.example.test=127.0.0.1:9154", "challenge zones answered by SynCloud's own server: zone=host:port, comma separated (reached once the zone's NS record exists)")
	otherZones := flag.String("other-zones", "manual.test,deleg.test,builtin.test", "zones that exist in DNS but are not at Cloudflare")
	flag.Parse()

	caKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tm := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Cloudflare Origin CA (sim)"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(20, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tm, tm, &caKey.PublicKey, caKey)
	caCert, _ = x509.ParseCertificate(der)

	for _, z := range strings.Split(*cfZones, ",") {
		if z = strings.TrimSpace(z); z != "" {
			zones[z], visible[z] = nil, true
		}
	}
	for _, f := range strings.Split(*forward, ",") {
		if z, addr, ok := strings.Cut(strings.TrimSpace(f), "="); ok {
			forwards[z] = addr
		}
	}
	for _, z := range strings.Split(*otherZones, ",") {
		if z = strings.TrimSpace(z); z != "" {
			zones[z] = nil
		}
	}

	ca := acmesim.New()
	ca.DNSHas = func(name string) bool { return len(txtAt(name)) > 0 }

	mux := http.NewServeMux()
	mux.HandleFunc("/client/v4/", cloudflare)
	go func() { log.Fatal(http.ListenAndServe(*cfAddr, mux)) }()
	go func() { log.Fatal(http.ListenAndServe(*acmeAddr, ca)) }()
	go func() { log.Fatal(http.ListenAndServe(*controlAddr, http.HandlerFunc(sim))) }()
	h := mdns.HandlerFunc(dnsHandler)
	go func() { log.Fatal((&mdns.Server{Addr: *dnsAddr, Net: "udp", Handler: h}).ListenAndServe()) }()
	go func() { log.Fatal((&mdns.Server{Addr: *dnsAddr, Net: "tcp", Handler: h}).ListenAndServe()) }()
	fmt.Printf("domainsim: cloudflare http://%s/client/v4  acme http://%s/directory  dns %s  control http://%s\n", *cfAddr, *acmeAddr, *dnsAddr, *controlAddr)
	select {}
}
