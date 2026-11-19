package domain

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestWildcard(t *testing.T) {
	if got := Wildcard("203.0.113.10", "sslip.io"); got != "203-0-113-10.sslip.io" {
		t.Fatal(got)
	}
}

func TestWildcardService(t *testing.T) {
	if svc, ok := WildcardService("203-0-113-10.nip.io"); !ok || svc != "nip.io" {
		t.Fatal(svc, ok)
	}
	for _, s := range []string{"app.203-0-113-10.sslip.io", "example.com", "203-0-113.sslip.io"} {
		if _, ok := WildcardService(s); ok {
			t.Errorf("%s matched", s)
		}
	}
}

func TestNormalize(t *testing.T) {
	ok := map[string]string{"Example.COM.": "example.com", " 1-2-3-4.sslip.io ": "1-2-3-4.sslip.io"}
	for in, want := range ok {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "localhost", "https://x.com", "x.com:80", "a..b", "-a.com", "a_b.com"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestIsPublic(t *testing.T) {
	for s, want := range map[string]bool{"8.8.8.8": true, "10.0.0.1": false, "192.168.1.1": false, "100.64.1.1": false, "127.0.0.1": false} {
		if IsPublic(netip.MustParseAddr(s)) != want {
			t.Errorf("%s", s)
		}
	}
}

func TestDetectorFallsBackToEchoService(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>") }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "203.0.113.7\n") }))
	defer good.Close()
	d := NewDetector("")
	d.Endpoints = []string{bad.URL, good.URL}
	ip, err := d.detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The test host may itself have a public interface address.
	if a := netip.MustParseAddr(ip); !IsPublic(a) {
		t.Fatalf("got %s", ip)
	}
	if NewDetector("198.51.100.1").Override == "" {
		t.Fatal("override ignored")
	}
}

func TestNormalizeHostWildcards(t *testing.T) {
	for in, want := range map[string]string{
		"Shop.Example.com.":    "shop.example.com",
		"*.example.com":        "*.example.com",
		"*.Sub.Example.com.":   "*.sub.example.com",
		"  *.a.b.example.com ": "*.a.b.example.com",
	} {
		got, wild, err := NormalizeHost(in)
		if err != nil || got != want || wild != strings.HasPrefix(want, "*.") {
			t.Errorf("%q: %q %v %v", in, got, wild, err)
		}
	}
	for _, bad := range []string{"*", "*.", "*.com", "a.*.example.com", "*.*.example.com", "*a.example.com", "**.example.com", "*.localhost", "*.a.localhost", "*.exa_mple.com", "https://*.example.com"} {
		if got, _, err := NormalizeHost(bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
	// The old function still refuses wildcards: the base domain is never one.
	if _, err := Normalize("*.example.com"); err == nil {
		t.Error("Normalize accepted a wildcard")
	}
}
