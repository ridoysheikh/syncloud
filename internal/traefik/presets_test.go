package traefik

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPresets(t *testing.T) {
	good := map[string]string{
		"rate-limit":       `{"average": 10}`,
		"ip-allowlist":     `{"sourceRange": ["203.0.113.5", "10.0.0.0/8"]}`,
		"redirect-www":     `{}`,
		"security-headers": `{"hsts": true, "frameDeny": true, "noSniff": true, "referrerPolicy": "same-origin"}`,
		"cors":             `{"origins": ["https://app.example.com"]}`,
		"circuit-breaker":  `{}`,
		"compress":         `{}`,
		"retry":            `{"attempts": 4}`,
	}
	for typ, raw := range good {
		out, err := ValidatePreset(typ, json.RawMessage(raw), nil)
		if err != nil {
			t.Errorf("%s: %v", typ, err)
			continue
		}
		if _, err := RenderPreset(typ, out); err != nil {
			t.Errorf("%s render: %v", typ, err)
		}
	}
	bad := map[string]string{
		"rate-limit":      `{"average": 0}`,
		"ip-allowlist":    `{"sourceRange": ["example.com"]}`,
		"cors":            `{"origins": ["javascript:alert(1)"]}`,
		"circuit-breaker": `{"expression": "os.Exit(1)"}`,
		"retry":           `{"attempts": 99}`,
		"compress":        `{"level": 9}`,
		"nope":            `{}`,
	}
	for typ, raw := range bad {
		if _, err := ValidatePreset(typ, json.RawMessage(raw), nil); err == nil {
			t.Errorf("%s %s accepted", typ, raw)
		}
	}
}

func TestBasicAuthHashesAndKeepsPasswords(t *testing.T) {
	out, err := ValidatePreset("basic-auth", json.RawMessage(`{"users":[{"username":"ann","password":"correct horse"}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "correct horse") || !strings.Contains(string(out), `"hash":"$2a$`) {
		t.Fatalf("stored: %s", out)
	}
	if pub := PublicPreset("basic-auth", out); strings.Contains(string(pub), "$2a$") {
		t.Fatalf("public config leaks the hash: %s", pub)
	}
	// Re-saving without a password keeps the hash; a new user needs one.
	again, err := ValidatePreset("basic-auth", json.RawMessage(`{"users":[{"username":"ann"}]}`), out)
	if err != nil || !strings.Contains(string(again), `"hash":"$2a$`) {
		t.Fatalf("kept: %s %v", again, err)
	}
	if _, err := ValidatePreset("basic-auth", json.RawMessage(`{"users":[{"username":"bob"}]}`), out); err == nil {
		t.Fatal("a new user without a password was accepted")
	}
	mw, _ := RenderPreset("basic-auth", out)
	if len(mw.BasicAuth.Users) != 1 || !strings.HasPrefix(mw.BasicAuth.Users[0], "ann:$2a$") {
		t.Fatalf("rendered: %+v", mw.BasicAuth)
	}
}

func TestRouteChainAndCustom(t *testing.T) {
	p := &Provider{ControllerURL: "http://127.0.0.1:7070", BaseDomain: func() string { return "" },
		ServiceRoutes: func() []ServiceRoute {
			return []ServiceRoute{
				{Name: "svc-a-http", Host: "a.localhost", Servers: []string{"http://10.91.1.2:80"}, Middlewares: []string{"mw-1", "mw-2"}},
				{Name: "svc-b-http", Host: "b.localhost", Servers: []string{"http://10.91.1.3:80"}, Middlewares: []string{"mw-3"}, OwnRetry: true},
			}
		},
		Middlewares: func() map[string]Middleware { return map[string]Middleware{"mw-1": {Compress: &Compress{}}} },
	}
	d := p.Config()
	if got := d.HTTP.Routers["svc-a-http"].Middlewares; strings.Join(got, ",") != "mw-1,mw-2,syncloud-retry" {
		t.Errorf("chain a: %v", got)
	}
	if got := d.HTTP.Routers["svc-b-http"].Middlewares; strings.Join(got, ",") != "mw-3" {
		t.Errorf("chain b: %v", got)
	}
	gen := p.Merged()
	custom, err := ValidateCustom(`
http:
  routers:
    legacy:
      rule: "Host(`+"`legacy.example.com`"+`)"
      entryPoints: [web]
      middlewares: [strip, mw-1]
      service: legacy
  middlewares:
    strip:
      stripPrefix:
        prefixes: ["/old"]
  services:
    legacy:
      loadBalancer:
        servers:
          - url: http://10.0.0.5:8080
`, gen)
	if err != nil {
		t.Fatal(err)
	}
	p.Custom = func() map[string]any { return custom }
	merged := p.Merged()
	if _, ok := merged["http"].(map[string]any)["routers"].(map[string]any)["legacy"]; !ok {
		t.Fatal("custom router not merged")
	}
	for _, bad := range []string{
		"tls:\n  certificates: []",
		"http:\n  routers:\n    x:\n      rule: Host(`x`)\n      service: api@internal",
		"http:\n  routers:\n    x:\n      rule: Host(`x`)\n      service: nope",
		"http:\n  routers:\n    svc-a-http:\n      rule: Host(`x`)\n      service: svc-a-http",
		"http:\n  routers:\n    x:\n      rule: Host(`x`)\n      service: svc-a-http\n      priority: high",
		"http:\n  routers:\n    x:\n      rule: Host(`x`)\n      service: svc-a-http\n      entryPoints: [traefik]",
		"http:\n  middlewares:\n    x:\n      teleport: {}",
		"http:\n  services:\n    x:\n      loadBalancer:\n        servers: []",
		"http: [",
	} {
		if _, err := ValidateCustom(bad, gen); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
	p.Certificates = func() []Certificate { return []Certificate{{CertFile: "CERTPEM", KeyFile: "KEYPEM"}} }
	p.BaseDomain = func() string { return "example.com" }
	b, _ := json.Marshal(p.Redacted())
	if strings.Contains(string(b), "KEYPEM") {
		t.Fatal("redacted config shows the key")
	}
}
