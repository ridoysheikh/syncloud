package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/events"
	"syncloud/internal/registry"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

type testEnv struct {
	srv   *httptest.Server
	st    *store.Store
	token string
	c     *http.Client
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tok, err := auth.EnsureSetupToken(ctx, st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	web := fstest.MapFS{
		"index.html":      {Data: []byte("<html>dashboard</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	}
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.LoadOrCreateIssuer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Secrets: box, Registry: reg, Bus: events.NewBus(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Web: web})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &testEnv{srv: srv, st: st, token: tok, c: &http.Client{Jar: jar}}
}

func (e *testEnv) do(t *testing.T, method, path string, body any, hdr map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

const pw = "a-long-enough-password"

func TestSetupLoginLogoutFlow(t *testing.T) {
	e := newEnv(t)

	resp, body := e.do(t, "GET", "/api/v1/system/status", nil, nil)
	if resp.StatusCode != 200 || body["setupRequired"] != true {
		t.Fatalf("status: %d %v", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/auth/me", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("me before login: %d", resp.StatusCode)
	}

	// Wrong token.
	resp, body = e.do(t, "POST", "/api/v1/setup", map[string]string{"setupToken": "syn_setup_nope", "email": "admin@example.com", "name": "Admin", "password": pw}, nil)
	if resp.StatusCode != 401 || errCode(body) != CodeUnauthorized {
		t.Fatalf("bad token: %d %v", resp.StatusCode, body)
	}
	// Weak password.
	resp, _ = e.do(t, "POST", "/api/v1/setup", map[string]string{"setupToken": e.token, "email": "admin@example.com", "name": "Admin", "password": "short"}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("weak password: %d", resp.StatusCode)
	}
	// Success signs in.
	resp, body = e.do(t, "POST", "/api/v1/setup", map[string]string{"setupToken": e.token, "email": "admin@example.com", "name": "Admin", "password": pw}, nil)
	if resp.StatusCode != 201 || body["isRoot"] != true {
		t.Fatalf("setup: %d %v", resp.StatusCode, body)
	}
	if resp, body := e.do(t, "GET", "/api/v1/auth/me", nil, nil); resp.StatusCode != 200 || body["email"] != "admin@example.com" {
		t.Fatalf("me after setup: %d %v", resp.StatusCode, body)
	}
	// Token is single-use.
	resp, _ = e.do(t, "POST", "/api/v1/setup", map[string]string{"setupToken": e.token, "email": "x@example.com", "name": "X", "password": pw}, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("second setup: %d", resp.StatusCode)
	}
	if _, body := e.do(t, "GET", "/api/v1/system/status", nil, nil); body["setupRequired"] != false {
		t.Fatalf("setupRequired after setup: %v", body)
	}

	// Logout invalidates the session server-side.
	if resp, _ := e.do(t, "POST", "/api/v1/auth/logout", nil, nil); resp.StatusCode != 204 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/auth/me", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("me after logout: %d", resp.StatusCode)
	}

	// Login: wrong then right.
	if resp, _ := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "admin@example.com", "password": "wrong-password-123"}, nil); resp.StatusCode != 401 {
		t.Fatalf("bad login: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "nobody@example.com", "password": pw}, nil); resp.StatusCode != 401 {
		t.Fatalf("unknown user login: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "ADMIN@example.com", "password": pw}, nil); resp.StatusCode != 200 {
		t.Fatalf("login (case-insensitive email): %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/auth/me", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("me after login: %d", resp.StatusCode)
	}

	var audits int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM audit_events`).Scan(&audits); err != nil || audits < 6 {
		t.Fatalf("audit events = %d err=%v", audits, err)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "a@b.co", "password": pw},
		map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != 403 || errCode(body) != CodeInvalidOrigin {
		t.Fatalf("cross-origin: %d %v", resp.StatusCode, body)
	}
}

func TestStrictJSON(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.do(t, "POST", "/api/v1/auth/login", map[string]any{"email": "a@b.co", "password": pw, "extra": 1}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("unknown field: %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	var last int
	for i := 0; i < 11; i++ {
		resp, _ := e.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "a@b.co", "password": "wrong-password-123"}, nil)
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("11th attempt: %d, want 429", last)
	}
}

func TestSPAFallbackAndAPINotFound(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/", "/compute/services", "/index.html"} {
		resp, err := e.c.Get(e.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(b) != "<html>dashboard</html>" {
			t.Fatalf("%s: %d %q", p, resp.StatusCode, b)
		}
	}
	resp, _ := e.c.Get(e.srv.URL + "/assets/app-1.js")
	if cc := resp.Header.Get("Cache-Control"); resp.StatusCode != 200 || cc == "" {
		t.Fatalf("asset: %d cache=%q", resp.StatusCode, cc)
	}
	resp.Body.Close()
	if resp, body := e.do(t, "GET", "/api/v1/nope", nil, nil); resp.StatusCode != 404 || errCode(body) != CodeNotFound {
		t.Fatalf("api 404: %d %v", resp.StatusCode, body)
	}
}

func TestClientIPTrustsOnlyLocalProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.9")
	r.Header.Set("X-Forwarded-Proto", "https")

	r.RemoteAddr = "127.0.0.1:5555" // Traefik on the same host
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("via local proxy: %q", got)
	}
	if !isHTTPS(r) {
		t.Fatal("X-Forwarded-Proto from local proxy ignored")
	}

	r.RemoteAddr = "192.0.2.1:5555" // anyone else: headers are ignored
	if got := clientIP(r); got != "192.0.2.1" {
		t.Fatalf("spoofed header trusted: %q", got)
	}
	if isHTTPS(r) {
		t.Fatal("spoofed X-Forwarded-Proto trusted")
	}
}
