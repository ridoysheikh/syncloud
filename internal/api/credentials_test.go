package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"syncloud/internal/client"
	"syncloud/internal/sigv"
)

// signIn completes setup so the env's cookie jar holds a root session.
func signIn(t *testing.T, e *testEnv) {
	t.Helper()
	resp, _ := e.do(t, "POST", "/api/v1/setup", map[string]string{"setupToken": e.token, "email": "admin@example.com", "name": "Admin", "password": pw}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
}

func TestAccessKeySignedRequests(t *testing.T) {
	e := newEnv(t)
	signIn(t, e)
	ctx := context.Background()

	resp, body := e.do(t, "POST", "/api/v1/iam/access-keys", map[string]string{"description": "laptop"}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create key: %d %v", resp.StatusCode, body)
	}
	id, secret := body["id"].(string), body["secretAccessKey"].(string)
	if !strings.HasPrefix(id, "SYNAK") || len(secret) != 40 {
		t.Fatalf("unexpected key %q / secret len %d", id, len(secret))
	}

	c, _ := client.New(e.srv.URL, client.Credentials{AccessKeyID: id, SecretAccessKey: secret})
	u, err := c.Whoami(ctx)
	if err != nil || u.Email != "admin@example.com" {
		t.Fatalf("signed whoami: %+v %v", u, err)
	}
	// A signed POST (body is covered by the signature).
	tok, err := c.CreateToken(ctx, "from-key", 7)
	if err != nil || !strings.HasPrefix(tok.Token, "syn_pat_") {
		t.Fatalf("signed create token: %+v %v", tok, err)
	}

	// The secret is never returned again.
	keys, err := c.ListAccessKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].SecretAccessKey != "" || keys[0].LastUsedAt == nil {
		t.Fatalf("list keys: %+v %v", keys, err)
	}

	// Wrong secret is rejected.
	bad, _ := client.New(e.srv.URL, client.Credentials{AccessKeyID: id, SecretAccessKey: strings.Repeat("A", 40)})
	if _, err := bad.Whoami(ctx); !isStatus(err, 401) {
		t.Fatalf("wrong secret: %v", err)
	}

	// A body swapped after signing is rejected.
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/iam/tokens", strings.NewReader(`{"name":"x"}`))
	signWith(req, id, secret, []byte(`{"name":"original"}`))
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != 401 {
		t.Fatalf("tampered body: %v %v", r.StatusCode, err)
	}

	// Two keys max; deleting one frees a slot.
	if _, err := c.CreateAccessKey(ctx, "second"); err != nil {
		t.Fatalf("second key: %v", err)
	}
	if _, err := c.CreateAccessKey(ctx, "third"); !isStatus(err, 409) {
		t.Fatalf("third key: %v", err)
	}
	if err := c.DeleteAccessKey(ctx, id); err != nil {
		t.Fatalf("delete own key: %v", err)
	}
	if _, err := c.Whoami(ctx); !isStatus(err, 401) {
		t.Fatalf("deleted key still works: %v", err)
	}

	var credType string
	if err := e.st.R.QueryRow(`SELECT json_extract(detail, '$.credType') FROM audit_events WHERE action = 'iam:CreateToken'`).Scan(&credType); err != nil || credType != CredAccessKey {
		t.Fatalf("audit credType = %q err=%v", credType, err)
	}
}

func TestBearerTokens(t *testing.T) {
	e := newEnv(t)
	signIn(t, e)
	ctx := context.Background()

	resp, body := e.do(t, "POST", "/api/v1/iam/tokens", map[string]any{"name": "ci", "expiresInDays": 30}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create token: %d %v", resp.StatusCode, body)
	}
	c, _ := client.New(e.srv.URL, client.Credentials{Token: body["token"].(string)})
	if u, err := c.Whoami(ctx); err != nil || !u.IsRoot {
		t.Fatalf("bearer whoami: %+v %v", u, err)
	}
	if err := c.DeleteToken(ctx, body["id"].(string)); err != nil {
		t.Fatalf("delete token: %v", err)
	}
	if _, err := c.Whoami(ctx); !isStatus(err, 401) {
		t.Fatalf("deleted token still works: %v", err)
	}

	if resp, _ := e.do(t, "POST", "/api/v1/iam/tokens", map[string]any{"name": "x", "expiresInDays": 999}, nil); resp.StatusCode != 400 {
		t.Fatalf("too-long expiry: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/auth/me", nil, map[string]string{"Authorization": "Basic Zm9v"}); resp.StatusCode != 401 {
		t.Fatalf("unsupported scheme: %d", resp.StatusCode)
	}
}

func TestCannotDeleteOthersCredentials(t *testing.T) {
	e := newEnv(t)
	signIn(t, e)
	if resp, _ := e.do(t, "DELETE", "/api/v1/iam/access-keys/SYNAKDOESNOTEXIST", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("delete unknown key: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "DELETE", "/api/v1/iam/tokens/tok_nope", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("delete unknown token: %d", resp.StatusCode)
	}
}

func isStatus(err error, status int) bool {
	var ce *client.Error
	return errors.As(err, &ce) && ce.Status == status
}

func signWith(req *http.Request, id, secret string, body []byte) {
	sigv.Sign(req, id, secret, body, time.Now())
}

func TestRegistryTokenEndpoint(t *testing.T) {
	e := newEnv(t)
	signIn(t, e)
	_, key := e.do(t, "POST", "/api/v1/iam/access-keys", map[string]string{}, nil)
	_, pat := e.do(t, "POST", "/api/v1/iam/tokens", map[string]any{"name": "docker", "expiresInDays": 1}, nil)

	get := func(user, pass, query string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/registry/token?"+query, nil)
		if user != "" || pass != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	q := "service=syncloud-registry&scope=repository:shop/api:pull,push"

	if code, _ := get("", "", q); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := get(key["id"].(string), "wrong-secret", q); code != 401 {
		t.Fatalf("wrong secret: %d", code)
	}
	if code, _ := get(key["id"].(string), key["secretAccessKey"].(string), "service=other"); code != 400 {
		t.Fatalf("wrong service: %d", code)
	}
	for name, creds := range map[string][2]string{
		"access key": {key["id"].(string), key["secretAccessKey"].(string)},
		"token":      {"anything", pat["token"].(string)},
	} {
		code, out := get(creds[0], creds[1], q)
		if code != 200 || out["token"] == "" || out["expires_in"].(float64) != 300 {
			t.Fatalf("%s: %d %v", name, code, out)
		}
		claims := strings.Split(out["token"].(string), ".")[1]
		b, _ := base64.RawURLEncoding.DecodeString(claims)
		if !strings.Contains(string(b), `"actions":["pull","push"]`) {
			t.Fatalf("%s: root not granted pull,push: %s", name, b)
		}
	}
}
