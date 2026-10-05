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
	if r := get().HTTP.Routers["syncloud-dashboard"]; r.Rule != "Host(`203-0-113-10.sslip.io`)" {
		t.Fatalf("dashboard router with domain: %+v", r)
	}
}
