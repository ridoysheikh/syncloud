package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHetznerAndDigitalOcean(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+" "+string(b))
		switch {
		case r.Method == "POST" && r.URL.Path == "/servers":
			_, _ = w.Write([]byte(`{"server":{"id":42,"name":"w-abc","status":"initializing","public_net":{"ipv4":{"ip":"203.0.113.5"}}}}`))
		case r.Method == "GET" && r.URL.Path == "/servers":
			_, _ = w.Write([]byte(`{"servers":[{"id":42,"name":"w-abc"}]}`))
		case r.Method == "POST" && r.URL.Path == "/droplets":
			_, _ = w.Write([]byte(`{"droplet":{"id":7,"name":"w-abc","networks":{"v4":[{"ip_address":"10.0.0.2","type":"private"},{"ip_address":"198.51.100.7","type":"public"}]}}}`))
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			http.Error(w, "nope", 404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	spec := ServerSpec{Name: "w-abc", Region: "fsn1", Type: "cx22", Image: "ubuntu-24.04", UserData: "#!/bin/bash\necho hi", Labels: map[string]string{"syncloud-pool": "w"}}

	h, _ := New("hetzner", Config{Token: "tok-hetzner-123456", URL: srv.URL})
	s, err := h.CreateServer(ctx, spec)
	if err != nil || s.ID != "42" || s.IP != "203.0.113.5" {
		t.Fatalf("hetzner create: %+v %v", s, err)
	}
	if !strings.Contains(got[0], "Bearer tok-hetzner-123456") || !strings.Contains(got[0], `"server_type":"cx22"`) || !strings.Contains(got[0], `"location":"fsn1"`) {
		t.Errorf("hetzner request: %s", got[0])
	}
	if l, err := h.ListServers(ctx, "syncloud-pool", "w"); err != nil || len(l) != 1 || !strings.Contains(got[1], "label_selector=syncloud-pool%3Dw") {
		t.Errorf("hetzner list: %v %v %s", l, err, got[1])
	}
	if err := h.DeleteServer(ctx, "42"); err != nil || !strings.HasPrefix(got[2], "DELETE /servers/42") {
		t.Errorf("hetzner delete: %v %s", err, got[2])
	}

	d, _ := New("digitalocean", Config{Token: "tok-do-1234567890", URL: srv.URL})
	s, err = d.CreateServer(ctx, spec)
	if err != nil || s.ID != "7" || s.IP != "198.51.100.7" {
		t.Fatalf("do create: %+v %v", s, err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(got[3][strings.Index(got[3], "{"):]), &body)
	if body["size"] != "cx22" || body["region"] != "fsn1" || body["tags"].([]any)[0] != "syncloud-pool:w" {
		t.Errorf("do request: %v", body)
	}
}

func TestErrorsHideURLs(t *testing.T) {
	w, _ := New("webhook", Config{URL: "http://127.0.0.1:1/secret-path?key=s3cret"})
	_, err := w.CreateServer(context.Background(), ServerSpec{Name: "x"})
	if err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("error leaks the URL: %v", err)
	}
}

func TestValidate(t *testing.T) {
	if _, err := (Config{Token: "short"}).Validate("hetzner"); err == nil {
		t.Error("short token accepted")
	}
	if s, err := (Config{Token: "abcdefghijklmnopWXYZ"}).Validate("hetzner"); err != nil || strings.Contains(s, "abcdefgh") {
		t.Errorf("summary %q %v", s, err)
	}
	if _, err := (Config{URL: "ftp://x"}).Validate("webhook"); err == nil {
		t.Error("bad webhook URL accepted")
	}
	if _, err := (Config{}).Validate("aws"); err == nil {
		t.Error("unknown type accepted")
	}
}
