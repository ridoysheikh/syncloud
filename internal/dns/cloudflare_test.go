package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCloudflarePaginationRetryAndErrors(t *testing.T) {
	limited := 0
	var created cfRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`))
			return
		}
		switch {
		case r.URL.Path == "/zones":
			page := r.URL.Query().Get("page")
			if page == "" {
				page = "1"
			}
			if page == "1" {
				_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"z1","name":"Example.com","status":"active","name_servers":["a.ns.cloudflare.com"]}],"result_info":{"page":1,"total_pages":2}}`))
			} else {
				_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"z2","name":"example.org"}],"result_info":{"page":2,"total_pages":2}}`))
			}
		case r.URL.Path == "/zones/z1/dns_records" && r.Method == "GET":
			if limited == 0 {
				limited++
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"rate limited"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"r1","type":"a","name":"APP.example.com.","content":"1.2.3.4","ttl":1,"proxied":true}],"result_info":{"page":1,"total_pages":1}}`))
		case r.URL.Path == "/zones/z1/dns_records" && r.Method == "POST":
			_ = json.NewDecoder(r.Body).Decode(&created)
			created.ID = "r9"
			b, _ := json.Marshal(map[string]any{"success": true, "result": created})
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, "/dns_records/r9") && r.Method == "DELETE":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":81044,"message":"Record does not exist."}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	var slept []time.Duration
	c := NewCloudflare("tok", srv.URL, nil)
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	ctx := context.Background()

	zs, err := c.Zones(ctx)
	if err != nil || len(zs) != 2 || zs[0].Name != "example.com" || zs[0].NameServers[0] != "a.ns.cloudflare.com" {
		t.Fatalf("%+v %v", zs, err)
	}
	rs, err := c.Records(ctx, "z1")
	if err != nil || len(rs) != 1 || rs[0].Name != "app.example.com" || rs[0].Type != "A" || !rs[0].Proxied {
		t.Fatalf("%+v %v", rs, err)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("429 was not retried after Retry-After: %v", slept)
	}
	if _, err := c.Create(ctx, "z1", Record{Type: "TXT", Name: "_acme-challenge.example.com", Content: "x", Proxied: true}); err != nil {
		t.Fatal(err)
	}
	if created.Proxied != nil || created.TTL != 1 {
		t.Fatalf("TXT must not carry proxied, TTL automatic: %+v", created)
	}
	err = c.Delete(ctx, "z1", "r9")
	var pe *Error
	if !asErr(err, &pe) || pe.Message != "Record does not exist." {
		t.Fatalf("the provider's own message is shown: %v", err)
	}
	bad := NewCloudflare("nope", srv.URL, nil)
	if err := bad.Verify(ctx); err == nil || !strings.Contains(err.Error(), "Invalid access token") {
		t.Fatal(err)
	}
}

func asErr(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
