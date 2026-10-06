package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/spf13/cobra"

	"syncloud/internal/api"
	"syncloud/internal/auth"
	"syncloud/internal/events"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

// Operations deliberately without a dedicated command, and why.
var noCommand = map[string]string{
	"completeSetup":    "browser-only: first-run wizard",
	"login":            "browser-only: dashboard sessions; the CLI uses access keys",
	"logout":           "browser-only: dashboard sessions",
	"stream":           "WebSocket for the dashboard; CLI watch commands arrive with resources",
	"getOpenAPI":       "reachable with `synctl api GET /api/v1/openapi.json`",
	"joinNode":         "agent-only: `syncloud-agent join` (it generates the node key)",
	"getRegistryToken": "used by `docker login` / the Docker registry token protocol",
	"gitWebhook":       "called by Git hosts on push",
}

// Every API operation has a synctl command (§5.1 parity check).
func TestEveryOperationHasACommand(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(api.OpenAPISpec, &doc); err != nil {
		t.Fatal(err)
	}
	covered := map[string]string{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, id := range strings.Split(c.Annotations[opAnnotation], ",") {
			if id == "" {
				continue
			}
			covered[id] = c.CommandPath()
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(NewRoot(nil, io.Discard, io.Discard))

	inSpec := map[string]bool{}
	for _, methods := range doc.Paths {
		for _, op := range methods {
			inSpec[op.OperationID] = true
			_, hasCmd := covered[op.OperationID]
			_, exempt := noCommand[op.OperationID]
			if !hasCmd && !exempt {
				t.Errorf("operation %q has no synctl command (annotate one with op(%q) or add it to noCommand with a reason)", op.OperationID, op.OperationID)
			}
		}
	}
	for id, path := range covered {
		if !inSpec[id] {
			t.Errorf("%q is annotated with unknown operation %q", path, id)
		}
	}
	for id := range noCommand {
		if !inSpec[id] {
			t.Errorf("noCommand lists unknown operation %q", id)
		}
	}
}

// End to end: a real API server, an access key, and synctl commands.
func TestSynctlAgainstServer(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secrets.New(make([]byte, 32))
	setupTok, _ := auth.EnsureSetupToken(ctx, st, time.Now())
	srv := httptest.NewServer(api.New(api.Options{
		Store: st, Secrets: box, Bus: events.NewBus(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Web: fstest.MapFS{},
	}).Handler())
	defer srv.Close()

	// Create the root account and an access key through the API, as the dashboard would.
	jar := &cookieClient{}
	jar.post(t, srv.URL+"/api/v1/setup", `{"setupToken":"`+setupTok+`","email":"a@example.com","name":"A","password":"a-long-enough-password"}`)
	var key struct {
		ID     string `json:"id"`
		Secret string `json:"secretAccessKey"`
	}
	json.Unmarshal(jar.post(t, srv.URL+"/api/v1/iam/access-keys", `{"description":"cli"}`), &key)

	t.Setenv(EnvConfigDir, t.TempDir())
	t.Setenv(EnvProfile, "")
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvKeyID, "")
	t.Setenv(EnvToken, "")

	// configure reads endpoint, key ID and secret from stdin.
	out := run(t, srv.URL+"\n"+key.ID+"\n"+key.Secret+"\n", "configure")
	if !strings.Contains(out, `Saved profile "default"`) {
		t.Fatalf("configure: %s", out)
	}
	if out := run(t, "", "whoami"); !strings.Contains(out, "a@example.com") {
		t.Fatalf("whoami: %s", out)
	}
	var tok struct{ Token string }
	json.Unmarshal([]byte(run(t, "", "iam", "tokens", "create", "--name", "ci", "-o", "json")), &tok)
	if !strings.HasPrefix(tok.Token, "syn_pat_") {
		t.Fatalf("tokens create: %+v", tok)
	}
	if out := run(t, "", "iam", "tokens", "list"); !strings.Contains(out, "ci") {
		t.Fatalf("tokens list: %s", out)
	}
	if out := run(t, "", "api", "GET", "/api/v1/auth/me"); !strings.Contains(out, `"isRoot": true`) {
		t.Fatalf("api: %s", out)
	}
	// Bearer tokens work through env vars, overriding the profile.
	t.Setenv(EnvToken, tok.Token)
	if out := run(t, "", "whoami", "--profile", "missing", "--endpoint", srv.URL); !strings.Contains(out, "a@example.com") {
		t.Fatalf("token whoami: %s", out)
	}
}

func run(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(strings.NewReader(stdin), &out, &errOut)
	root.SetArgs(args)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("synctl %s: %v\n%s", strings.Join(args, " "), err, errOut.String())
	}
	return out.String()
}

type cookieClient struct{ cookies []*http.Cookie }

func (c *cookieClient) post(t *testing.T, url, body string) []byte {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %s: %d", url, resp.StatusCode)
	}
	c.cookies = append(c.cookies, resp.Cookies()...)
	b, _ := io.ReadAll(resp.Body)
	return b
}
