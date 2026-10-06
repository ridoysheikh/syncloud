package gitconn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"syncloud/internal/gitprovider"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

func newManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secrets.New(make([]byte, 32))
	return New(st, box, func() string { return "https://dash.example.com" }, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func TestTokenConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token good" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"message":"invalid token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"login":"ann"}`))
	}))
	defer srv.Close()
	m, st := newManager(t)
	ctx := context.Background()
	if _, err := m.AddToken(ctx, TokenInput{Kind: "gitea", Name: "forge", URL: srv.URL, Token: "bad"}); err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("bad token accepted: %v", err)
	}
	if _, err := m.AddToken(ctx, TokenInput{Kind: "gitea", Name: "Forge!", URL: srv.URL, Token: "good"}); err == nil {
		t.Fatal("bad name accepted")
	}
	v, err := m.AddToken(ctx, TokenInput{Kind: "gitea", Name: "forge", URL: srv.URL + "/", Token: "good"})
	if err != nil || v.Account != "ann" || v.APIURL != srv.URL {
		t.Fatalf("add: %+v %v", v, err)
	}
	raw, _ := st.GitConnection(ctx, "forge")
	if strings.Contains(string(raw.SecretsEnc), "good") {
		t.Fatal("token stored in the clear")
	}
	if _, p, err := m.Provider(ctx, "forge"); err != nil || p == nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := m.AddToken(ctx, TokenInput{Kind: "gitea", Name: "forge", URL: srv.URL, Token: "good"}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if err := m.Delete(ctx, "forge"); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "forge"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestGitHubAppManifestFlow(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/app-manifests/the-code/conversions" {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "slug": "syncloud-dash", "pem": "-----BEGIN RSA PRIVATE KEY-----\n-----END RSA PRIVATE KEY-----\n",
			"webhook_secret": "whsec", "client_id": "Iv1", "client_secret": "cs", "owner": map[string]string{"login": "acme"}})
	}))
	defer gh.Close()
	m, _ := newManager(t)
	ctx := context.Background()
	if _, err := m.Manifest("usr_1", ManifestInput{GitHubURL: "http://insecure"}); err == nil {
		t.Fatal("plain-HTTP GitHub server accepted")
	}
	man, err := m.Manifest("usr_1", ManifestInput{Org: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(man.PostURL, "https://github.com/organizations/acme/settings/apps/new?state=") {
		t.Fatalf("post url %s", man.PostURL)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(man.Manifest), &body)
	hook := body["hook_attributes"].(map[string]any)["url"].(string)
	if body["redirect_url"] != "https://dash.example.com/api/v1/integrations/github/callback" || !strings.HasPrefix(hook, "https://dash.example.com"+HookPathGitHubApp+"gc_") {
		t.Fatalf("manifest %s", man.Manifest)
	}
	if n := body["name"].(string); len(n) > 34 {
		t.Fatalf("app name too long for GitHub: %q", n)
	}
	// Point the pending flow at the fake GitHub.
	m.mu.Lock()
	p := m.pending[man.State]
	p.apiURL = gh.URL
	m.pending[man.State] = p
	m.mu.Unlock()
	if _, err := m.CompleteManifest(ctx, "usr_2", "the-code", man.State); err == nil {
		t.Fatal("another user completed the flow")
	}
	// The state is single-use: a fresh one for the real completion.
	man, _ = m.Manifest("usr_1", ManifestInput{})
	m.mu.Lock()
	p = m.pending[man.State]
	p.apiURL = gh.URL
	m.pending[man.State] = p
	m.mu.Unlock()
	v, err := m.CompleteManifest(ctx, "usr_1", "the-code", man.State)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != gitprovider.KindGitHubApp || v.Account != "acme" || v.InstallURL != "https://github.com/apps/syncloud-dash/installations/new" || v.ID != p.id {
		t.Fatalf("view %+v", v)
	}
	if sec, err := m.AppWebhookSecret(ctx, v.ID); err != nil || sec != "whsec" {
		t.Fatalf("webhook secret %q %v", sec, err)
	}
	if _, err := m.CompleteManifest(ctx, "usr_1", "the-code", man.State); err == nil {
		t.Fatal("state reused")
	}
}
