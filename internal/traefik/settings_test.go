package traefik

import (
	"slices"
	"strings"
	"testing"
)

func TestSettingsValidate(t *testing.T) {
	for _, bad := range []Settings{
		{LogLevel: "LOUD"},
		{ReadTimeout: "soon"},
		{TrustedIPs: []string{"10.0.0.0/8,1.2.3.4"}},
		{TrustedIPs: []string{"example.com"}},
		{ProxyProtocol: true},
		{MinTLS: "1.0"},
		{RetryAttempts: 11},
		{MaxBodyMB: -1},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	s := Settings{LogLevel: "warn", TrustedIPs: []string{" 173.245.48.0/20 ", "", "203.0.113.7"}, ProxyProtocol: true, ReadTimeout: "2m"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.LogLevel != "WARN" || len(s.TrustedIPs) != 2 || s.MinTLS != "1.2" {
		t.Fatalf("normalized: %+v", s)
	}
}

func TestStaticArgsAreStable(t *testing.T) {
	s := DefaultSettings()
	if got := s.StaticArgs(); !slices.Equal(got, []string{"--log.level=INFO", "--serversTransport.forwardingTimeouts.dialTimeout=2s"}) {
		t.Fatalf("defaults add flags: %v", got)
	}
	s.ReadTimeout, s.IdleTimeout, s.TrustedIPs, s.ProxyProtocol, s.HTTP3 = "120s", "5m", []string{"10.0.0.0/8"}, true, true
	a := s.StaticArgs()
	for range 20 {
		if !slices.Equal(a, s.StaticArgs()) {
			t.Fatal("flag order changes between calls (would restart Traefik)")
		}
	}
	joined := strings.Join(a, " ")
	for _, want := range []string{
		"--entrypoints.websecure.transport.respondingTimeouts.readTimeout=120s",
		"--entrypoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8",
		"--entrypoints.websecure.proxyProtocol.trustedIPs=10.0.0.0/8",
		"--entrypoints.websecure.http3=true",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %v", want, a)
		}
	}
	base := []string{"--ping=true", "--log.level=INFO"}
	if got := WithStatic(base, Settings{LogLevel: "DEBUG"}); !slices.Equal(got, []string{"--ping=true", "--log.level=DEBUG", "--serversTransport.forwardingTimeouts.dialTimeout=2s"}) {
		t.Fatalf("WithStatic: %v", got)
	}
}

func TestSettingsShapeRoutes(t *testing.T) {
	set := DefaultSettings()
	p := &Provider{ControllerURL: "http://c", BaseDomain: func() string { return "example.com" }, Settings: func() Settings { return set },
		ServiceRoutes: func() []ServiceRoute {
			return []ServiceRoute{{Name: "svc-a", Host: "a.example.com", Servers: []string{"http://10.91.0.2:80"}, Middlewares: []string{"mw-x"}}}
		}}
	d := p.Config()
	if r := d.HTTP.Routers["svc-a"]; !slices.Equal(r.Middlewares, []string{"mw-x", "syncloud-retry"}) {
		t.Fatalf("default chain: %v", r.Middlewares)
	}
	if r := d.HTTP.Routers["svc-a-http"]; !slices.Equal(r.Middlewares, []string{"syncloud-https"}) {
		t.Fatalf("http router should redirect: %v", r.Middlewares)
	}
	if d.TLS == nil || d.TLS.Options["default"].MinVersion != "VersionTLS12" {
		t.Fatalf("tls options: %+v", d.TLS)
	}

	set = Settings{RedirectHTTPS: false, MinTLS: "1.3", SNIStrict: true, RetryAttempts: 0, HSTSSeconds: 600, Compress: true, MaxBodyMB: 5}
	d = p.Config()
	want := []string{"syncloud-body-limit", "syncloud-hsts", "syncloud-compress", "mw-x"}
	if r := d.HTTP.Routers["svc-a"]; !slices.Equal(r.Middlewares, want) {
		t.Fatalf("chain with defaults: %v", r.Middlewares)
	}
	if r := d.HTTP.Routers["svc-a-http"]; !slices.Equal(r.Middlewares, want) {
		t.Fatalf("http router should serve directly: %v", r.Middlewares)
	}
	if _, ok := d.HTTP.Middlewares["syncloud-retry"]; ok {
		t.Fatal("retry disabled but defined")
	}
	if b := d.HTTP.Middlewares["syncloud-body-limit"].Buffering; b == nil || b.MaxRequestBodyBytes != 5<<20 {
		t.Fatalf("body limit: %+v", b)
	}
	if o := d.TLS.Options["default"]; o.MinVersion != "VersionTLS13" || !o.SNIStrict {
		t.Fatalf("tls options: %+v", o)
	}
}

func TestGitServerRoute(t *testing.T) {
	gh := ""
	p := &Provider{ControllerURL: "http://c", BaseDomain: func() string { return "example.com" }, GitHost: func() string { return gh }, GitServerURL: "http://127.0.0.1:3002"}
	if _, ok := p.Config().HTTP.Routers["syncloud-git"]; ok {
		t.Fatal("git router while the server is off")
	}
	gh = "git.example.com"
	d := p.Config()
	if r := d.HTTP.Routers["syncloud-git"]; r.Rule != "Host(`git.example.com`)" || r.TLS == nil {
		t.Fatalf("git router %+v", r)
	}
	if !strings.Contains(d.HTTP.Routers["syncloud-https-redirect"].Rule, "git.example.com") {
		t.Fatal("plain HTTP to git.example.com is not redirected")
	}
	if _, ok := p.EdgeConfig()["http"].(map[string]any)["routers"].(map[string]any)["syncloud-git"]; ok {
		t.Fatal("edges cannot reach the controller's loopback Git server")
	}
}

func TestRouteHealthCheck(t *testing.T) {
	p := &Provider{ControllerURL: "http://c", BaseDomain: func() string { return "example.com" }, ServiceRoutes: func() []ServiceRoute {
		return []ServiceRoute{{Name: "a", Host: "a.example.com", Servers: []string{"http://10.91.0.2:80"}, HealthPath: "/healthz"},
			{Name: "b", Host: "b.example.com", Servers: []string{"http://10.91.0.3:80"}}}
	}}
	d := p.Config()
	if h := d.HTTP.Services["a"].LoadBalancer.HealthCheck; h == nil || h.Path != "/healthz" || h.Interval != "2s" {
		t.Fatalf("health check: %+v", h)
	}
	if d.HTTP.Services["b"].LoadBalancer.HealthCheck != nil {
		t.Fatal("a service without a health check is probed")
	}
}
