package gitprovider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type call struct {
	method, path, auth string
	body               map[string]any
}

// recorder answers from a route table and records every request.
type recorder struct {
	mu     sync.Mutex
	calls  []call
	routes map[string]func(w http.ResponseWriter, r *http.Request)
}

func (rc *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &body)
	auth := r.Header.Get("Authorization")
	if t := r.Header.Get("PRIVATE-TOKEN"); t != "" {
		auth = "PRIVATE-TOKEN " + t
	}
	rc.mu.Lock()
	rc.calls = append(rc.calls, call{r.Method, r.URL.RequestURI(), auth, body})
	rc.mu.Unlock()
	h, ok := rc.routes[r.Method+" "+r.URL.Path]
	if !ok {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		return
	}
	h(w, r)
}

func (rc *recorder) find(method, prefix string) (call, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for _, c := range rc.calls {
		if c.method == method && strings.HasPrefix(c.path, prefix) {
			return c, true
		}
	}
	return call{}, false
}

func reply(v any) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(v) }
}

func TestGitHubApp(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	// verifyJWT checks the app JWT GitHub would.
	verifyJWT := func(r *http.Request) bool {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(tok, ".")
		if !ok || len(parts) != 3 {
			return false
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
			return false
		}
		var claims struct {
			Iss string `json:"iss"`
			Exp int64  `json:"exp"`
		}
		b, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(b, &claims)
		return claims.Iss == "42" && claims.Exp > time.Now().Unix()
	}
	jwtOnly := func(h func(w http.ResponseWriter, r *http.Request)) func(w http.ResponseWriter, r *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			if !verifyJWT(r) {
				w.WriteHeader(401)
				return
			}
			h(w, r)
		}
	}
	tokens := 0
	rc := &recorder{}
	rc.routes = map[string]func(w http.ResponseWriter, r *http.Request){
		"GET /app":               jwtOnly(reply(map[string]any{"slug": "syncloud-x", "owner": map[string]string{"login": "acme"}})),
		"GET /app/installations": jwtOnly(reply([]map[string]any{{"id": 7, "account": map[string]string{"login": "acme"}}})),
		"POST /app/installations/7/access_tokens": jwtOnly(func(w http.ResponseWriter, _ *http.Request) {
			tokens++
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_install", "expires_at": time.Now().Add(time.Hour)})
		}),
		"GET /installation/repositories": reply(map[string]any{"repositories": []map[string]any{
			{"full_name": "acme/api", "clone_url": "https://github.com/acme/api.git", "default_branch": "main", "private": true},
			{"full_name": "acme/web", "clone_url": "https://github.com/acme/web.git", "default_branch": "trunk"},
		}}),
		"GET /repos/acme/other/installation": jwtOnly(reply(map[string]any{"id": 7})),
		"POST /repos/acme/api/statuses/abc":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201) },
	}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	p, err := New(Conn{Kind: KindGitHubApp, APIURL: srv.URL, AppID: 42, Secrets: Secrets{PrivateKey: keyPEM}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if acct, err := p.Account(ctx); err != nil || acct != "acme" {
		t.Fatalf("account %q %v", acct, err)
	}
	repos, err := p.Repos(ctx, "AP")
	if err != nil || len(repos) != 1 || repos[0].FullName != "acme/api" || !repos[0].Private {
		t.Fatalf("repos %+v %v", repos, err)
	}
	if ins, _ := Installations(ctx, p); len(ins) != 1 || ins[0] != "acme" {
		t.Fatalf("installations %v", ins)
	}
	// The token is cached; another repository is looked up once.
	if tok, err := p.Token(ctx, "acme/api"); err != nil || tok != "ghs_install" {
		t.Fatalf("token %q %v", tok, err)
	}
	if tok, err := p.Token(ctx, "acme/other"); err != nil || tok != "ghs_install" {
		t.Fatalf("token for other repo %q %v", tok, err)
	}
	if tokens != 1 {
		t.Fatalf("minted %d installation tokens, want 1 (cached)", tokens)
	}
	if _, err := p.Token(ctx, "stranger/repo"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("uninstalled repo: %v", err)
	}
	if id, err := p.CreateHook(ctx, "acme/api", "https://x", "s"); err != nil || id != "" {
		t.Fatalf("an app needs no repository hook: %q %v", id, err)
	}
	if err := p.SetStatus(ctx, "acme/api", "abc", Status{State: StateRunning, Context: "github.com/ridoysheikh/syncloud/web", Description: "Building", TargetURL: "https://d"}); err != nil {
		t.Fatal(err)
	}
	c, _ := rc.find("POST", "/repos/acme/api/statuses/abc")
	if c.auth != "Bearer ghs_install" || c.body["state"] != "pending" || c.body["context"] != "github.com/ridoysheikh/syncloud/web" {
		t.Fatalf("status call %+v", c)
	}
}

func TestGitHubTokenGitLabGitea(t *testing.T) {
	rc := &recorder{}
	rc.routes = map[string]func(w http.ResponseWriter, r *http.Request){
		// GitHub
		"GET /user":                      reply(map[string]any{"login": "ann", "username": "ann"}),
		"GET /repos/ann/app":             reply(map[string]any{"full_name": "ann/app", "clone_url": "https://h/ann/app.git", "default_branch": "main"}),
		"POST /repos/ann/app/hooks":      reply(map[string]any{"id": 99}),
		"DELETE /repos/ann/app/hooks/99": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) },
		// GitLab (under /api/v4)
		"GET /api/v4/user":                               reply(map[string]any{"username": "gl-ann"}),
		"GET /api/v4/projects/grp/sub/app":               reply(map[string]any{"path_with_namespace": "grp/sub/app", "http_url_to_repo": "https://gl/grp/sub/app.git", "default_branch": "dev", "visibility": "private"}),
		"POST /api/v4/projects/grp/sub/app/hooks":        reply(map[string]any{"id": 5}),
		"POST /api/v4/projects/grp/sub/app/statuses/abc": reply(map[string]any{}),
		// Gitea (under /api/v1)
		"GET /api/v1/user":                           reply(map[string]any{"login": "gt-ann"}),
		"GET /api/v1/user/repos":                     reply([]map[string]any{{"full_name": "gt-ann/app", "clone_url": "http://g/gt-ann/app.git", "default_branch": "main"}}),
		"POST /api/v1/repos/gt-ann/app/hooks":        reply(map[string]any{"id": 3}),
		"POST /api/v1/repos/gt-ann/app/statuses/abc": reply(map[string]any{}),
	}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	ctx := context.Background()

	gh, _ := New(Conn{Kind: KindGitHub, APIURL: srv.URL, Secrets: Secrets{Token: "ghp_x"}})
	if a, err := gh.Account(ctx); err != nil || a != "ann" {
		t.Fatalf("github account %q %v", a, err)
	}
	if id, err := gh.CreateHook(ctx, "ann/app", "https://dash/api/v1/hooks/git/git_1", "sec"); err != nil || id != "99" {
		t.Fatalf("github hook %q %v", id, err)
	}
	c, _ := rc.find("POST", "/repos/ann/app/hooks")
	cfg, _ := c.body["config"].(map[string]any)
	if c.auth != "Bearer ghp_x" || cfg["url"] != "https://dash/api/v1/hooks/git/git_1" || cfg["secret"] != "sec" {
		t.Fatalf("github hook call %+v", c)
	}
	if err := gh.DeleteHook(ctx, "ann/app", "99"); err != nil {
		t.Fatal(err)
	}
	if err := gh.DeleteHook(ctx, "ann/app", "100"); err != nil {
		t.Fatalf("a hook already gone is fine: %v", err)
	}

	gl, _ := New(Conn{Kind: KindGitLab, APIURL: srv.URL, Secrets: Secrets{Token: "glpat"}})
	if a, err := gl.Account(ctx); err != nil || a != "gl-ann" {
		t.Fatalf("gitlab account %q %v", a, err)
	}
	r, err := gl.Repo(ctx, "grp/sub/app")
	if err != nil || r.DefaultBranch != "dev" || !r.Private {
		t.Fatalf("gitlab repo %+v %v", r, err)
	}
	if _, err := gl.CreateHook(ctx, "grp/sub/app", "https://dash/h", "tok"); err != nil {
		t.Fatal(err)
	}
	c, _ = rc.find("POST", "/api/v4/projects/grp%2Fsub%2Fapp/hooks")
	if c.auth != "PRIVATE-TOKEN glpat" || c.body["token"] != "tok" || c.body["push_events"] != true {
		t.Fatalf("gitlab hook call %+v", c)
	}
	if err := gl.SetStatus(ctx, "grp/sub/app", "abc", Status{State: StateFailure, Context: "github.com/ridoysheikh/syncloud/x"}); err != nil {
		t.Fatal(err)
	}
	c, _ = rc.find("POST", "/api/v4/projects/grp%2Fsub%2Fapp/statuses/abc")
	if c.body["state"] != "failed" || c.body["name"] != "github.com/ridoysheikh/syncloud/x" {
		t.Fatalf("gitlab status %+v", c)
	}

	gt, _ := New(Conn{Kind: KindGitea, APIURL: srv.URL, Secrets: Secrets{Token: "gtok"}})
	repos, err := gt.Repos(ctx, "")
	if err != nil || len(repos) != 1 {
		t.Fatalf("gitea repos %+v %v", repos, err)
	}
	if id, err := gt.CreateHook(ctx, "gt-ann/app", "https://dash/h", "s"); err != nil || id != "3" {
		t.Fatalf("gitea hook %q %v", id, err)
	}
	c, _ = rc.find("POST", "/api/v1/repos/gt-ann/app/hooks")
	if c.auth != "token gtok" || c.body["type"] != "gitea" {
		t.Fatalf("gitea hook call %+v", c)
	}
	if err := gt.SetStatus(ctx, "gt-ann/app", "abc", Status{State: StateSuccess, Context: "github.com/ridoysheikh/syncloud/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := gt.Repo(ctx, "gt-ann/missing"); !IsNotFound(err) {
		t.Fatalf("missing repo: %v", err)
	}
}

func TestValidRepoName(t *testing.T) {
	for _, ok := range []string{"a/b", "grp/sub/app", "Acme-1/api.v2"} {
		if !ValidRepoName(ok) {
			t.Errorf("rejected %s", ok)
		}
	}
	for _, bad := range []string{"", "a", "a/", "/b", "a/../b", "a/b c", "a/b?x"} {
		if ValidRepoName(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
