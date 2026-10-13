package api

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

func pngDataURL(t *testing.T, size int) string {
	t.Helper()
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, size, size)))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func (e *testEnv) get(t *testing.T, path string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestBranding(t *testing.T) {
	e := newEnv(t)
	// Defaults before setup, publicly.
	if _, body := e.do(t, "GET", "/api/v1/branding", nil, nil); body["name"] != "SynCloud" || body["hasLogo"] != false {
		t.Fatalf("defaults: %v", body)
	}
	if resp, b := e.get(t, "/branding/logo.png", nil); resp.StatusCode != 200 || b != "default-logo" {
		t.Fatalf("default logo: %d %q", resp.StatusCode, b)
	}
	// A bad logo fails setup before the account exists.
	resp, _ := e.do(t, "POST", "/api/v1/setup", map[string]string{"setupToken": e.token, "email": "root@example.com", "name": "Root", "password": pw,
		"workspaceName": "Acme Cloud", "logo": "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("svg logo: %d", resp.StatusCode)
	}
	if _, body := e.do(t, "GET", "/api/v1/system/status", nil, nil); body["setupRequired"] != true {
		t.Fatal("setup completed despite a bad logo")
	}
	resp, body := e.do(t, "POST", "/api/v1/setup", map[string]string{"setupToken": e.token, "email": "root@example.com", "name": "Root", "password": pw,
		"workspaceName": "Acme Cloud", "title": "Acme Console", "logo": pngDataURL(t, 64)}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("setup: %d %v", resp.StatusCode, body)
	}
	_, body = e.do(t, "GET", "/api/v1/branding", nil, nil)
	if body["name"] != "Acme Cloud" || body["title"] != "Acme Console" || body["hasLogo"] != true {
		t.Fatalf("after setup: %v", body)
	}
	ver := body["version"].(string)
	resp, b := e.get(t, "/branding/favicon.png", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(b, "\x89PNG") {
		t.Fatalf("favicon: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := e.get(t, "/branding/logo.png", map[string]string{"If-None-Match": `"` + ver + `"`}); resp.StatusCode != 304 {
		t.Fatalf("etag: %d", resp.StatusCode)
	}
	// The tab title and icons are in index.html before any script runs.
	_, page := e.get(t, "/compute/services", nil)
	if !strings.Contains(page, "<title>Acme Console</title>") || !strings.Contains(page, "/branding/favicon.png?v="+ver) || strings.Contains(page, "/favicon.ico") {
		t.Fatalf("index: %s", page)
	}
	// Change later; remove the logo.
	if resp, body := e.do(t, "PUT", "/api/v1/branding", map[string]string{"name": "Acme", "title": ""}, nil); resp.StatusCode != 200 || body["title"] != "Acme" {
		t.Fatalf("set: %d %v", resp.StatusCode, body)
	}
	if resp, body := e.do(t, "DELETE", "/api/v1/branding/logo", nil, nil); resp.StatusCode != 200 || body["hasLogo"] != false {
		t.Fatalf("remove logo: %d %v", resp.StatusCode, body)
	}
	if _, b := e.get(t, "/branding/logo.png", nil); b != "default-logo" {
		t.Fatalf("logo after removal: %q", b)
	}
	// Signed out, branding can't be changed.
	if resp, _ := e.as(t).do(t, "PUT", "/api/v1/branding", map[string]string{"name": "x"}, nil); resp.StatusCode != 401 {
		t.Fatalf("anonymous set: %d", resp.StatusCode)
	}
}
