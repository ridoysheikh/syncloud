package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ridoysheikh/syncloud/internal/agentgw"
	"github.com/ridoysheikh/syncloud/internal/api"
	"github.com/ridoysheikh/syncloud/internal/events"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/pki"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// fakeTraefik passes TLS connections on undecrypted to the gateway's PROXY
// port, announcing client as the connection's source, as Traefik does for
// agent.<domain>:443.
func fakeTraefik(t *testing.T, target, client string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer up.Close()
				io.WriteString(up, "PROXY TCP4 "+client+" 127.0.0.1 51000 443\r\n")
				go io.Copy(up, c)
				io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String()
}

// An agent tries the gateway addresses in order: a dead one is skipped, the
// Traefik route (another TLS name, PROXY protocol) connects, and the
// controller sees the agent's own address.
func TestGatewayFallbackThroughTraefik(t *testing.T) {
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

	direct, _ := net.Listen("tcp", "127.0.0.1:0")
	proxied, _ := net.Listen("tcp", "127.0.0.1:0")
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	dead.Close() // refused
	traefik := fakeTraefik(t, proxied.Addr().String(), "203.0.113.9")

	endpoints := []api.GatewayEndpoint{
		{Address: dead.Addr().String(), ServerName: "agent.example.test"},
		{Address: traefik, ServerName: "agent.example.test"},
		{Address: direct.Addr().String()},
	}
	gw := agentgw.New(st, reg, log)
	gw.SetEndpoints(func(store.Node) []*agentv1.GatewayEndpoint {
		var out []*agentv1.GatewayEndpoint
		for _, e := range endpoints {
			out = append(out, &agentv1.GatewayEndpoint{Address: e.Address, ServerName: e.ServerName})
		}
		return out
	})
	var mu sync.Mutex
	var remotes []string
	gw.AddHooks(agentgw.Hooks{OnConnect: func(c agentgw.Conn) {
		mu.Lock()
		remotes = append(remotes, c.Remote)
		mu.Unlock()
	}})
	go gw.ServeListener(ctx, ca, []string{"127.0.0.1"}, direct, agentgw.ProxyListener(proxied))

	box, _ := secrets.New(make([]byte, 32))
	srv := httptest.NewServer(api.New(api.Options{
		Store: st, Secrets: box, CA: ca, Nodes: reg, GatewayAddr: direct.Addr().String(),
		GatewayEndpoints: func(string) []api.GatewayEndpoint { return endpoints },
		Bus:              bus, Log: log, Web: fstest.MapFS{},
	}).Handler())
	t.Cleanup(srv.Close)
	c := &cluster{st: st, registry: reg, apiURL: srv.URL, ca: ca}

	dataDir := t.TempDir()
	joined, err := Join(ctx, dataDir, c.apiURL, c.joinToken(t, true), "w-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := joined.endpoints(); len(got) != 3 || got[1].ServerName != "agent.example.test" {
		t.Fatalf("endpoints after join: %+v", got)
	}

	var out bytes.Buffer
	ok, err := CheckGateways(ctx, dataDir, &out)
	if err != nil || ok != 2 {
		t.Fatalf("check: %d answered, %v\n%s", ok, err, out.String())
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); !strings.HasPrefix(lines[0], "✗") || !strings.HasPrefix(lines[1], "✓") {
		t.Fatalf("check output:\n%s", out.String())
	}

	go Run(ctx, dataDir, log, Options{})
	waitFor(t, "connected through Traefik", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(remotes) > 0
	})
	mu.Lock()
	defer mu.Unlock()
	if remotes[0] != "203.0.113.9" {
		t.Fatalf("remote address %q: want the PROXY header's", remotes[0])
	}
}

// A TLS name other than the gateway's is accepted only with a certificate
// from the cluster CA that names pki.GatewayName.
func TestEndpointTLSChecksClusterCA(t *testing.T) {
	ca, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := &tls.Config{RootCAs: ca.Pool(), MinVersion: tls.VersionTLS13}
	cases := []struct {
		name  string
		ca    *pki.CA
		hosts []string
		ok    bool
	}{
		{"cluster CA", ca, nil, true},
		{"other CA", other, nil, false},
		{"no gateway name", ca, []string{"example.test"}, false},
	}
	for _, tc := range cases {
		hosts := tc.hosts
		if hosts == nil {
			hosts = []string{pki.GatewayName}
		}
		cert, err := tc.ca.ServerCert(hosts, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			c, err := ln.Accept()
			if err == nil {
				_ = c.(*tls.Conn).Handshake()
				c.Close()
			}
		}()
		err = probeEndpoint(context.Background(), base, Endpoint{Address: ln.Addr().String(), ServerName: "agent.example.test"})
		ln.Close()
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}
