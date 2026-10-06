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
	if got := s.StaticArgs(); !slices.Equal(got, []string{"--log.level=INFO"}) {
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
	if got := WithStatic(base, Settings{LogLevel: "DEBUG"}); !slices.Equal(got, []string{"--ping=true", "--log.level=DEBUG"}) {
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
