package agent

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"syncloud/internal/agentgw"
	"syncloud/internal/api"
	"syncloud/internal/auth"
	"syncloud/internal/events"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

type cluster struct {
	st       *store.Store
	registry *nodes.Registry
	apiURL   string
	ca       *pki.CA
}

// startCluster runs the API and the agent gateway on random local ports.
func startCluster(t *testing.T) *cluster {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, err := pki.LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	reg := nodes.NewRegistry(st, bus, log)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go agentgw.New(st, reg, log).ServeListener(ctx, ca, ln, []string{"127.0.0.1"})

	box, _ := secrets.New(make([]byte, 32))
	srv := httptest.NewServer(api.New(api.Options{
		Store: st, Secrets: box, CA: ca, Nodes: reg, GatewayAddr: ln.Addr().String(),
		Bus: bus, Log: log, Web: fstest.MapFS{},
	}).Handler())
	t.Cleanup(srv.Close)
	return &cluster{st: st, registry: reg, apiURL: srv.URL, ca: ca}
}

func (c *cluster) joinToken(t *testing.T, single bool) string {
	t.Helper()
	tok := auth.NewToken("SYN-JOIN-")
	now := time.Now()
	if err := c.st.CreateJoinToken(context.Background(), store.JoinToken{
		ID: auth.NewID("jt_"), TokenHash: auth.HashToken(tok), CreatedAt: now, ExpiresAt: now.Add(time.Hour), SingleUse: single,
	}); err != nil {
		t.Fatal(err)
	}
	return tok
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestJoinConnectAndRevoke(t *testing.T) {
	c := startCluster(t)
	ctx := context.Background()
	dataDir := t.TempDir()

	tok := c.joinToken(t, true)
	st, err := Join(ctx, dataDir, c.apiURL, tok, "w-01")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if !strings.HasPrefix(st.NodeID, "node_") {
		t.Fatalf("node id %q", st.NodeID)
	}
	// Single-use token cannot be reused, and a machine cannot join twice.
	if _, err := Join(ctx, t.TempDir(), c.apiURL, tok, "w-02"); err == nil {
		t.Fatal("single-use token accepted twice")
	}
	if _, err := Join(ctx, dataDir, c.apiURL, c.joinToken(t, true), "w-03"); err == nil {
		t.Fatal("joined twice from the same data dir")
	}
	// Names are unique.
	if _, err := Join(ctx, t.TempDir(), c.apiURL, c.joinToken(t, true), "w-01"); err == nil {
		t.Fatal("duplicate node name accepted")
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- Run(runCtx, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{}) }()

	node := func() nodes.View {
		for _, n := range c.registry.List() {
			if n.ID == st.NodeID {
				return n
			}
		}
		return nodes.View{}
	}
	waitFor(t, "node ready and connected", func() bool { n := node(); return n.Status == store.NodeReady && n.Connected })
	if n := node(); n.Info.CPUCores == 0 || n.Info.Hostname == "" {
		t.Fatalf("node info missing: %+v", n.Info)
	}
	if got, _ := c.st.NodeByID(ctx, st.NodeID); got.Status != store.NodeReady {
		t.Fatalf("ready status not persisted: %q", got.Status)
	}

	// Deleting the node revokes it: the next connection attempt is refused for good.
	if err := c.st.DeleteNode(ctx, st.NodeID); err != nil {
		t.Fatal(err)
	}
	stop() // drop the current stream; Run returns nil on cancel
	<-done
	err = Run(ctx, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	if err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("removed node: want rejection, got %v", err)
	}
}

func TestHeartbeatsCarryMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a heartbeat")
	}
	c := startCluster(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataDir := t.TempDir()
	st, err := Join(ctx, dataDir, c.apiURL, c.joinToken(t, true), "w-01")
	if err != nil {
		t.Fatal(err)
	}
	go Run(ctx, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	waitFor(t, "first heartbeat", func() bool {
		for _, n := range c.registry.List() {
			if n.ID == st.NodeID && n.Metrics != nil {
				return n.Metrics.MemoryTotalBytes > 0 && n.Metrics.DiskTotalBytes > 0
			}
		}
		return false
	})
}

func TestUntrustedClientRejected(t *testing.T) {
	c := startCluster(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	if _, err := Join(ctx, dataDir, c.apiURL, c.joinToken(t, true), "w-01"); err != nil {
		t.Fatal(err)
	}
	// A certificate from a different CA must not be accepted.
	other, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, csrPEM, _ := pki.NewNodeKey("evil")
	st, _, _, _ := loadState(dataDir)
	certPEM, _, err := other.SignNodeCSR(csrPEM, st.NodeID) // same node ID, wrong CA
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dataDir, keyFile), keyPEM)
	writeFile(t, filepath.Join(dataDir, certFile), certPEM)

	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	go Run(rctx, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	time.Sleep(1500 * time.Millisecond)
	for _, n := range c.registry.List() {
		if n.Connected {
			t.Fatal("node with a foreign certificate connected")
		}
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCertificateRenewal(t *testing.T) {
	c := startCluster(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataDir := t.TempDir()
	st, err := Join(ctx, dataDir, c.apiURL, c.joinToken(t, true), "w-01")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dataDir, certFile))
	oldSerial, _, _ := c.st.NodeCertSerials(ctx, st.NodeID)

	// RenewBefore longer than the validity: renew on the first connection.
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- Run(runCtx, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{RenewBefore: 365 * 24 * time.Hour})
	}()
	waitFor(t, "renewed certificate saved", func() bool {
		after, _ := os.ReadFile(filepath.Join(dataDir, certFile))
		return len(after) > 0 && string(after) != string(before)
	})
	cur, prev, _ := c.st.NodeCertSerials(ctx, st.NodeID)
	if cur == oldSerial || prev != oldSerial {
		t.Fatalf("serials: cur=%s prev=%s old=%s", cur, prev, oldSerial)
	}
	stop()
	<-done

	// Reconnecting with the new certificate retires the old serial.
	go Run(ctx, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	waitFor(t, "previous serial dropped", func() bool {
		_, prev, _ := c.st.NodeCertSerials(ctx, st.NodeID)
		return prev == ""
	})
	if _, err := tls.LoadX509KeyPair(filepath.Join(dataDir, certFile), filepath.Join(dataDir, keyFile)); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedRenewalRecovers(t *testing.T) {
	dir := t.TempDir()
	key1, csr1, _ := pki.NewNodeKey("a")
	key2, csr2, _ := pki.NewNodeKey("a")
	ca, _ := pki.LoadOrCreateCA(t.TempDir())
	crt1, _, _ := ca.SignNodeCSR(csr1, "node_x")
	crt2, _, _ := ca.SignNodeCSR(csr2, "node_x")
	_ = key1
	// Crash after the key rename, before the certificate rename.
	writeFile(t, filepath.Join(dir, keyFile), key2)
	writeFile(t, filepath.Join(dir, certFile), crt1)
	writeFile(t, filepath.Join(dir, certFile+".tmp"), crt2)
	if _, err := loadNodeCert(dir); err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, certFile)); string(b) != string(crt2) {
		t.Fatal("new certificate not moved into place")
	}
}
