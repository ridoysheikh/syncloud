package pki

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPinnedTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	pin, err := SPKIPin(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	if err != nil || !strings.HasPrefix(pin, "sha256//") {
		t.Fatalf("pin %q %v", pin, err)
	}
	get := func(pin string) error {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedTLS(pin)}}
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(pin); err != nil {
		t.Fatalf("the pinned self-signed certificate was refused: %v", err)
	}
	if err := get("sha256//AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a wrong pin was accepted: %v", err)
	}
}
