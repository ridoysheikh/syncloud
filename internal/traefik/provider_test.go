package traefik

import (
	"encoding/json"
	"net/http/httptest"
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
