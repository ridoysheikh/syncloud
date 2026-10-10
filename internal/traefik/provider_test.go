package traefik

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderRequiresTokenAndRoutesDashboard(t *testing.T) {
	domain := ""
	p := &Provider{Token: "secret", TokenHeader: "X-Syncloud-Token", ControllerURL: "http://127.0.0.1:7070", BaseDomain: func() string { return domain }}

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/internal/traefik/config", nil))
	if rec.Code != 403 {
		t.Fatalf("without token: %d", rec.Code)
	}

	get := func() Dynamic {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/internal/traefik/config", nil)
		req.Header.Set("X-Syncloud-Token", "secret")
		p.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("with token: %d", rec.Code)
		}
		var d Dynamic
		if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := get()
	if r := d.HTTP.Routers["syncloud-dashboard"]; r.Rule != "PathPrefix(`/`)" || r.Service != "syncloud-controller" {
		t.Fatalf("dashboard router without domain: %+v", r)
	}
	if s := d.HTTP.Services["syncloud-controller"].LoadBalancer.Servers; len(s) != 1 || s[0].URL != "http://127.0.0.1:7070" {
		t.Fatalf("controller service: %+v", s)
	}

	domain = "203-0-113-10.sslip.io"
	p.RegistryURL = "http://127.0.0.1:5000"
	p.Certificates = func() []Certificate { return []Certificate{{CertFile: "CERT", KeyFile: "KEY"}} }
	d = get()
	if r := d.HTTP.Routers["syncloud-dashboard"]; r.Rule != "Host(`203-0-113-10.sslip.io`)" || r.TLS == nil || r.EntryPoints[0] != "websecure" {
		t.Fatalf("dashboard router with domain: %+v", r)
	}
	if r := d.HTTP.Routers["syncloud-registry"]; r.Rule != "Host(`registry.203-0-113-10.sslip.io`)" || r.TLS == nil {
		t.Fatalf("registry router: %+v", r)
	}
	if r := d.HTTP.Routers["syncloud-acme"]; r.EntryPoints[0] != "web" || r.Priority < 1000 {
		t.Fatalf("acme router: %+v", r)
	}
	if m := d.HTTP.Middlewares["syncloud-https"]; m.RedirectScheme == nil || m.RedirectScheme.Scheme != "https" {
		t.Fatalf("redirect: %+v", m)
	}
	if d.TLS == nil || len(d.TLS.Certificates) != 1 || d.TLS.Certificates[0].KeyFile != "KEY" {
		t.Fatalf("tls: %+v", d.TLS)
	}
	p.ServiceRoutes = func() []ServiceRoute {
		return []ServiceRoute{{Name: "svc-1-http", Host: "web-production-shop.203-0-113-10.sslip.io", Servers: []string{"http://10.91.1.2:8080", "http://10.91.2.2:8080"}}}
	}
	d = get()
	if r := d.HTTP.Routers["svc-1-http"]; r.Rule != "Host(`web-production-shop.203-0-113-10.sslip.io`)" || r.TLS == nil || r.Middlewares[0] != "syncloud-retry" {
		t.Fatalf("service router: %+v", r)
	}
	if r := d.HTTP.Routers["svc-1-http-http"]; r.Middlewares[0] != "syncloud-https" {
		t.Fatalf("service redirect: %+v", r)
	}
	if s := d.HTTP.Services["svc-1-http"].LoadBalancer.Servers; len(s) != 2 {
		t.Fatalf("service servers: %+v", s)
	}
	if host("evil`) || Host(`x") != "Host(`invalid.invalid`)" {
		t.Fatal("rule injection")
	}
}

func TestTCPRoutesForPublicDatabases(t *testing.T) {
	domain := ""
	routes := []TCPRoute{
		{Name: "db-1", Entrypoint: "valkey", Host: "cache.db.example.com", Servers: []string{"10.91.1.2:6379"}, Allow: []string{"203.0.113.0/24"}},
		{Name: "db-1-ro", Entrypoint: "valkey", Host: "cache-ro.db.example.com", Servers: []string{"10.91.1.3:6379", "10.91.2.3:6379"}},
		{Name: "db-2", Entrypoint: "valkey", Host: "down.db.example.com"}, // no primary running
	}
	p := &Provider{ControllerURL: "http://127.0.0.1:7070", BaseDomain: func() string { return domain }, TCPRoutes: func() []TCPRoute { return routes }}
	if p.Config().TCP != nil {
		t.Fatal("TCP routes without a base domain (no certificates)")
	}
	domain = "example.com"
	tcp := p.Config().TCP
	if tcp == nil {
		t.Fatal("no TCP config")
	}
	r := tcp.Routers["db-1"]
	if r.Rule != "HostSNI(`cache.db.example.com`)" || r.EntryPoints[0] != "valkey" || r.TLS == nil || len(r.Middlewares) != 1 {
		t.Errorf("router: %+v", r)
	}
	if mw := tcp.Middlewares[r.Middlewares[0]]; mw.IPAllowList == nil || mw.IPAllowList.SourceRange[0] != "203.0.113.0/24" {
		t.Errorf("allow-list: %+v", mw)
	}
	if ro := tcp.Routers["db-1-ro"]; len(ro.Middlewares) != 0 || len(tcp.Services["db-1-ro"].LoadBalancer.Servers) != 2 {
		t.Errorf("read-only route: %+v %+v", ro, tcp.Services["db-1-ro"])
	}
	if _, ok := tcp.Routers["db-2"]; ok {
		t.Error("a route without servers")
	}
	// TLS must be terminated: the router carries "tls": {} in JSON.
	b, _ := json.Marshal(tcp.Routers["db-1"])
	if !json.Valid(b) || !strings.Contains(string(b), `"tls":{}`) {
		t.Errorf("router JSON: %s", b)
	}
}

func TestDedicatedDatabasePortsAndDefaultCertificate(t *testing.T) {
	routes := []TCPRoute{
		{Name: "db-1-port", Entrypoint: "db-21000", Servers: []string{"10.91.1.2:6379"}, Allow: []string{"203.0.113.0/24"}},
		{Name: "db-1-port-plain", Entrypoint: "db-21000", Servers: []string{"10.91.1.2:6379"}, Allow: []string{"203.0.113.0/24"}, Plain: true},
	}
	var def *Certificate
	p := &Provider{ControllerURL: "http://127.0.0.1:7070", BaseDomain: func() string { return "example.com" },
		TCPRoutes: func() []TCPRoute { return routes }, DefaultCertificate: func() *Certificate { return def }}
	c := p.Config()
	tlsR, plainR := c.TCP.Routers["db-1-port"], c.TCP.Routers["db-1-port-plain"]
	if tlsR.Rule != "HostSNI(`*`)" || tlsR.TLS == nil || tlsR.EntryPoints[0] != "db-21000" || len(tlsR.Middlewares) != 1 {
		t.Errorf("TLS router: %+v", tlsR)
	}
	if plainR.Rule != "HostSNI(`*`)" || plainR.TLS != nil || len(plainR.Middlewares) != 1 {
		t.Errorf("plain router: %+v", plainR)
	}
	if c.TLS.Stores != nil {
		t.Error("a default certificate store without a certificate")
	}
	def = &Certificate{CertFile: "CERT", KeyFile: "KEY"}
	if st := p.Config().TLS.Stores["default"]; st.DefaultCertificate == nil || st.DefaultCertificate.CertFile != "CERT" {
		t.Errorf("default store: %+v", st)
	}
	b, _ := json.Marshal(p.Redacted())
	if strings.Contains(string(b), "KEY") {
		t.Errorf("the default certificate's key is not redacted: %s", b)
	}
}

func TestServiceRoutesPathsRedirectsAndPublicPorts(t *testing.T) {
	domain := ""
	p := &Provider{ControllerURL: "http://127.0.0.1:7070", BaseDomain: func() string { return domain },
		ServiceRoutes: func() []ServiceRoute {
			return []ServiceRoute{
				{Name: "dom-a", Host: "shop.example.com", Path: "/api", StripPrefix: true, Servers: []string{"http://10.91.1.2:8080"}},
				{Name: "dom-b", Host: "shop.example.com", Servers: []string{"http://10.91.1.3:80"}},
				{Name: "dom-c", Host: "www.shop.example.com", RedirectTo: "shop.example.com", Servers: []string{"http://10.91.1.3:80"}},
			}
		},
		TCPRoutes: func() []TCPRoute {
			return []TCPRoute{
				{Name: "pub-tcp-20001", Entrypoint: "tcp-20001", Servers: []string{"10.91.1.4:5000"}, Plain: true, Allow: []string{"198.51.100.0/24"}},
				{Name: "db-1", Entrypoint: "valkey", Host: "cache.db.example.com", Servers: []string{"10.91.1.2:6379"}},
			}
		},
		UDPRoutes: func() []UDPRoute {
			return []UDPRoute{{Name: "pub-udp-20002", Entrypoint: "udp-20002", Servers: []string{"10.91.1.5:53"}}}
		},
	}
	// Without a base domain the plain TCP and UDP ports are still served.
	d := p.Config()
	if d.TCP == nil || len(d.TCP.Routers) != 1 || d.UDP == nil || d.UDP.Routers["pub-udp-20002"].EntryPoints[0] != "udp-20002" {
		t.Fatalf("dev config: tcp %+v udp %+v", d.TCP, d.UDP)
	}
	domain = "example.com"
	d = p.Config()
	a := d.HTTP.Routers["dom-a"]
	if a.Rule != "Host(`shop.example.com`) && PathPrefix(`/api`)" || a.Middlewares[0] != "dom-a-strip" ||
		d.HTTP.Middlewares["dom-a-strip"].StripPrefix.Prefixes[0] != "/api" {
		t.Errorf("path route: %+v", a)
	}
	if b := d.HTTP.Routers["dom-b"]; b.Rule != "Host(`shop.example.com`)" {
		t.Errorf("host route: %+v", b)
	}
	c := d.HTTP.Routers["dom-c"]
	if len(c.Middlewares) != 1 || d.HTTP.Middlewares[c.Middlewares[0]].RedirectRegex.Replacement != "https://shop.example.com${1}" {
		t.Errorf("redirect: %+v %+v", c, d.HTTP.Middlewares[c.Middlewares[0]])
	}
	r := d.TCP.Routers["pub-tcp-20001"]
	b, _ := json.Marshal(r)
	if r.Rule != "HostSNI(`*`)" || r.TLS != nil || strings.Contains(string(b), `"tls"`) || len(r.Middlewares) != 1 {
		t.Errorf("plain TCP router: %s", b)
	}
	if d.TCP.Routers["db-1"].TLS == nil {
		t.Error("the database route lost TLS")
	}
}

// The uptime monitor's probes take a twin router and service, so Traefik
// counts them apart from the service's traffic.
func TestProbeTwins(t *testing.T) {
	domain := ""
	p := &Provider{ControllerURL: "http://127.0.0.1:7070", BaseDomain: func() string { return domain }, ProbeToken: "TOK",
		ServiceRoutes: func() []ServiceRoute {
			return []ServiceRoute{
				{Name: "svc-svc_ab12-http", Host: "web.example.com", HealthPath: "/healthz", Servers: []string{"http://10.91.1.2:8080"}},
				{Name: "dom-c", Host: "www.shop.example.com", RedirectTo: "shop.example.com", Servers: []string{"http://10.91.1.3:80"}},
			}
		},
	}
	d := p.Config()
	pr, ok := d.HTTP.Routers["svc-svc_ab12-http_probe"]
	if !ok || pr.Rule != "(Host(`web.example.com`)) && Header(`X-Syncloud-Probe`, `TOK`)" || pr.Service != "svc-svc_ab12-http_probe" ||
		pr.EntryPoints[0] != "web" || pr.TLS != nil {
		t.Fatalf("dev probe router: %+v", pr)
	}
	ps := d.HTTP.Services["svc-svc_ab12-http_probe"].LoadBalancer
	if len(ps.Servers) != 1 || ps.HealthCheck == nil || ps.HealthCheck.Interval != "10s" || d.HTTP.Services["svc-svc_ab12-http"].LoadBalancer.HealthCheck.Interval != "2s" {
		t.Errorf("probe service: %+v", ps)
	}
	if _, ok := d.HTTP.Routers["dom-c_probe"]; ok {
		t.Error("a redirect got a probe twin")
	}
	domain = "example.com"
	if pr := p.Config().HTTP.Routers["svc-svc_ab12-http_probe"]; pr.EntryPoints[0] != "websecure" || pr.TLS == nil {
		t.Errorf("probe router with a base domain: %+v", pr)
	}
	if !IsProbe("svc-svc_ab12-http_probe@http") || IsProbe("svc-svc_ab12-http@http") || IsProbe("") {
		t.Error("IsProbe")
	}
	// Without a token nothing changes.
	p.ProbeToken = ""
	if _, ok := p.Config().HTTP.Routers["svc-svc_ab12-http_probe"]; ok {
		t.Error("a probe twin without a token")
	}
}
