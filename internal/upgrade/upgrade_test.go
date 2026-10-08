package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeRelease(t *testing.T, base, version string, files map[string]string, corrupt string) {
	t.Helper()
	dir := filepath.Join(base, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sums []string
	for name, body := range files {
		h := sha256.Sum256([]byte(body))
		sum := hex.EncodeToString(h[:])
		if name == corrupt {
			sum = strings.Repeat("0", 64)
		}
		sums = append(sums, sum+"  "+name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Join(sums, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSourceFetch(t *testing.T) {
	base := t.TempDir()
	arch := runtime.GOARCH
	files := map[string]string{"syncloud-controller-linux-" + arch: "ctl", "syncloud-agent-linux-" + arch: "agent", "synctl-linux-amd64": "cli"}
	writeRelease(t, base, "1.2.3", files, "")
	writeRelease(t, base, "1.2.4", files, "syncloud-agent-linux-"+arch)
	if err := os.MkdirAll(filepath.Join(base, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "channels", "stable"), []byte("1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := Source{Base: "file://" + base}
	ctx := context.Background()

	if v, err := src.Latest(ctx, "stable"); err != nil || v != "1.2.3" {
		t.Fatalf("Latest = %q, %v", v, err)
	}
	got, err := src.Fetch(ctx, "1.2.3", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("fetched %v", got)
	}
	if b, _ := os.ReadFile(got["synctl-linux-amd64"]); string(b) != "cli" {
		t.Fatalf("synctl = %q", b)
	}
	if _, err := src.Fetch(ctx, "1.2.4", t.TempDir()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("corrupt release: %v", err)
	}
	if _, err := src.Fetch(ctx, "9.9.9", t.TempDir()); err == nil {
		t.Fatal("missing release fetched")
	}
	if _, err := src.Fetch(ctx, "../etc", t.TempDir()); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestWaitHealthy(t *testing.T) {
	var calls atomic.Int32
	var script func(n int32) (int, Health)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		code, h := script(calls.Add(1))
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(h)
	}))
	defer srv.Close()
	ctx := context.Background()
	noPID := func() int { return 0 }

	// The old version answers first, then the new one, ready.
	script = func(n int32) (int, Health) {
		if n < 2 {
			return 200, Health{Version: "1.0.0", Ready: true}
		}
		return 200, Health{Version: "1.1.0", Ready: true}
	}
	if err := waitHealthy(ctx, srv.URL, "1.1.0", noPID, 2*time.Second, 20*time.Second); err != nil {
		t.Fatalf("healthy upgrade: %v", err)
	}

	// Healthy at first, then a system task fails while settling.
	calls.Store(0)
	script = func(n int32) (int, Health) {
		if n < 2 {
			return 200, Health{Version: "1.1.0", Ready: true}
		}
		return 503, Health{Version: "1.1.0", Problems: []string{"system task syncloud-traefik is exited"}}
	}
	err := waitHealthy(ctx, srv.URL, "1.1.0", noPID, time.Minute, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "became unhealthy") || !strings.Contains(err.Error(), "traefik") {
		t.Fatalf("unhealthy while settling: %v", err)
	}

	// Never the right version: times out.
	script = func(int32) (int, Health) { return 200, Health{Version: "1.0.0", Ready: true} }
	if err := waitHealthy(ctx, srv.URL, "1.1.0", noPID, time.Second, 3*time.Second); err == nil || !strings.Contains(err.Error(), "not healthy within") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestAgentConnected(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(agentDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(agentPendingFile(dir), AgentPending{Version: "2.0.0", From: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	// The old agent (restarted by something else) leaves the pending upgrade alone.
	if r := AgentConnected(dir, "1.0.0"); r != nil {
		t.Fatalf("unexpected result %v", r)
	}
	if _, ok := ReadAgentPending(dir); !ok {
		t.Fatal("pending removed by the old agent")
	}
	// The new agent confirms it.
	AgentConnected(dir, "2.0.0")
	if _, ok := ReadAgentPending(dir); ok {
		t.Fatal("pending not removed by the new agent")
	}
	// A rolled-back upgrade is reported once.
	if err := writeJSON(agentResultFile(dir), AgentResult{Version: "2.0.0", Error: "the new agent exited; rolled back to 1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if r := AgentConnected(dir, "1.0.0"); r == nil || !strings.Contains(r.Error, "rolled back") {
		t.Fatalf("result = %v", r)
	}
	if r := AgentConnected(dir, "1.0.0"); r != nil {
		t.Fatal("result reported twice")
	}
}

func TestBeginRefusesWhileRunning(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "upgrade"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := State{To: "2.0.0", Phase: PhaseVerifying}
	if err := s.save(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(context.Background(), Options{DataDir: dir, Version: "2.0.1", Current: "1.0.0"}); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("Begin = %v", err)
	}
	s.Phase = PhaseRolledBack
	_ = s.save(dir)
	if _, err := Begin(context.Background(), Options{DataDir: dir, Version: "1.0.0", Current: "1.0.0"}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("same version: %v", err)
	}
}

func TestAlive(t *testing.T) {
	if !alive(os.Getpid()) {
		t.Fatal("self not alive")
	}
	if alive(0) || alive(1<<30) {
		t.Fatal("bogus PID alive")
	}
}

// toServer sends every request (whatever its host) to a test server, so a
// github.com base can be served locally.
type toServer struct{ url string }

func (r toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.URL.Host = strings.TrimPrefix(r.url, "http://")
	out.Header.Set("X-Orig-Host", req.URL.Host)
	return http.DefaultTransport.RoundTrip(out)
}

func TestGitHubSource(t *testing.T) {
	arch := runtime.GOARCH
	ctl := "syncloud-controller-linux-" + arch
	agent := "syncloud-agent-linux-" + arch
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	assets := map[string]string{
		ctl:          "ctl",
		agent:        "agent",
		"SHA256SUMS": sum("ctl") + "  " + ctl + "\n" + sum("agent") + "  " + agent + "\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Header.Get("X-Orig-Host")
		switch {
		case host == "github.com" && r.URL.Path == "/acme/cloud/releases/latest":
			http.Redirect(w, r, "https://github.com/acme/cloud/releases/tag/v1.4.2", http.StatusFound)
		case host == "github.com" && strings.HasPrefix(r.URL.Path, "/acme/cloud/releases/download/v1.4.2/"):
			body, ok := assets[strings.TrimPrefix(r.URL.Path, "/acme/cloud/releases/download/v1.4.2/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(body))
		case host == "api.github.com" && r.URL.Path == "/repos/acme/cloud/releases":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v1.5.0-rc.2", "draft": true},
				{"tag_name": "v1.5.0-rc.1", "prerelease": true},
				{"tag_name": "v1.4.2"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	src := Source{Base: "https://github.com/acme/cloud/", HTTP: &http.Client{Transport: toServer{srv.URL}}}
	ctx := context.Background()

	if v, err := src.Latest(ctx, "stable"); err != nil || v != "1.4.2" {
		t.Fatalf("stable = %q, %v", v, err)
	}
	if v, err := src.Latest(ctx, "beta"); err != nil || v != "1.5.0-rc.1" {
		t.Fatalf("beta = %q, %v (drafts are skipped, prereleases count)", v, err)
	}
	for _, v := range []string{"1.4.2", "v1.4.2"} {
		got, err := src.Fetch(ctx, v, t.TempDir())
		if err != nil {
			t.Fatalf("Fetch(%s): %v", v, err)
		}
		if b, _ := os.ReadFile(got[ctl]); string(b) != "ctl" {
			t.Fatalf("controller = %q", b)
		}
	}
	if _, err := src.Fetch(ctx, "9.9.9", t.TempDir()); err == nil {
		t.Fatal("a missing release was fetched")
	}
	for base, want := range map[string]bool{
		"https://github.com/acme/cloud":       true,
		"https://github.com/acme":             false,
		"https://github.com/acme/cloud/extra": false,
		"https://mirror.example.com/a/b":      false,
		"http://github.com/acme/cloud":        false,
	} {
		if _, ok := (Source{Base: base}).githubRepo(); ok != want {
			t.Errorf("githubRepo(%s) = %v", base, ok)
		}
	}
}
