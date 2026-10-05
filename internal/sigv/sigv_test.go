package sigv

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	now := time.Date(2026, 10, 6, 10, 15, 0, 0, time.UTC)
	body := []byte(`{"a":1}`)
	req := httptest.NewRequest("POST", "http://ctl.example/api/v1/x?b=2&a=1&a=0", strings.NewReader(string(body)))
	Sign(req, "SYNAKTEST", "s3cret", body, now)

	keyID, sig, err := ParseAuthorization(req.Header.Get("Authorization"))
	if err != nil || keyID != "SYNAKTEST" {
		t.Fatalf("parse: %q %v", keyID, err)
	}
	if err := Verify(req, "s3cret", sig, body, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	cases := map[string]func() error{
		"wrong secret": func() error { return Verify(req, "other", sig, body, now) },
		"body changed": func() error { return Verify(req, "s3cret", sig, []byte(`{"a":2}`), now) },
		"too old":      func() error { return Verify(req, "s3cret", sig, body, now.Add(MaxSkew+time.Second)) },
		"future":       func() error { return Verify(req, "s3cret", sig, body, now.Add(-MaxSkew-time.Second)) },
		"path changed": func() error {
			r2 := req.Clone(req.Context())
			r2.URL.Path = "/api/v1/y"
			return Verify(r2, "s3cret", sig, body, now)
		},
		"method changed": func() error {
			r2 := req.Clone(req.Context())
			r2.Method = "DELETE"
			return Verify(r2, "s3cret", sig, body, now)
		},
	}
	for name, f := range cases {
		if f() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCanonicalQueryOrderIndependent(t *testing.T) {
	if CanonicalQuery("b=2&a=1&a=0") != CanonicalQuery("a=0&a=1&b=2") {
		t.Fatal("query order changed the canonical form")
	}
}
