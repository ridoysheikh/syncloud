// Command syncloud-controller runs the SynCloud control plane.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"syncloud/internal/agentgw"
	"syncloud/internal/api"
	"syncloud/internal/auth"
	"syncloud/internal/config"
	"syncloud/internal/events"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
	"syncloud/internal/system"
	"syncloud/internal/traefik"
	"syncloud/internal/version"
	"syncloud/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "syncloud-controller:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.LoadController(args)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if cfg.Dev {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	box, err := secrets.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return err
	}

	token, err := auth.EnsureSetupToken(ctx, st, time.Now())
	if err != nil {
		return fmt.Errorf("setup token: %w", err)
	}

	ca, err := pki.LoadOrCreateCA(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("certificate authority: %w", err)
	}

	bus := events.NewBus()
	registry := nodes.NewRegistry(st, bus, log)
	if err := registry.Load(ctx); err != nil {
		return fmt.Errorf("load nodes: %w", err)
	}
	if err := ensureLocalJoinToken(ctx, st, cfg.DataDir); err != nil {
		return fmt.Errorf("local join token: %w", err)
	}

	gwHost, _, err := net.SplitHostPort(cfg.AgentAdvertise)
	if err != nil {
		return fmt.Errorf("--agent-advertise: %w", err)
	}
	gw := agentgw.New(st, registry, log)

	traefikToken, err := loadOrCreateToken(filepath.Join(cfg.DataDir, "traefik.token"))
	if err != nil {
		return err
	}
	controllerURL := "http://" + loopbackURLHost(cfg.Listen)
	sysMgr := system.NewManager(gw, bus, log, system.Config{
		ControllerURL: controllerURL, HTTPAddr: cfg.PublicHTTP, HTTPSAddr: cfg.PublicHTTPS,
		AdminAddr: cfg.TraefikAdmin, TraefikToken: traefikToken,
	})
	if cfg.SystemTasks {
		gw.SetHooks(sysMgr.Hooks())
		go sysMgr.Run(ctx)
	}
	traefikProvider := &traefik.Provider{
		Token: traefikToken, TokenHeader: system.TraefikTokenHeader, ControllerURL: controllerURL,
		BaseDomain: func() string {
			d, _, _ := st.GetSetting(context.Background(), store.SettingBaseDomain)
			return d
		},
	}

	srv := api.New(api.Options{
		Store: st, Secrets: box, CA: ca, Nodes: registry, GatewayAddr: cfg.AgentAdvertise,
		System: sysMgr, Internal: map[string]http.Handler{"GET /internal/traefik/config": traefikProvider},
		Bus: bus, Log: log, Web: web.FS(),
	})
	go func() {
		if err := gw.Serve(ctx, ca, cfg.AgentListen, []string{gwHost, "localhost", "127.0.0.1"}); err != nil {
			log.Error("agent gateway stopped", "err", err)
			stop()
		}
	}()
	go registry.Run(ctx)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Info("controller started", "version", version.Version, "listen", ln.Addr().String(), "data", cfg.DataDir)
	if token != "" {
		printSetupBanner(ln.Addr().String(), token)
	}

	go publishControllerStats(ctx, bus)
	go cleanupSessions(ctx, st, log)

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

func printSetupBanner(addr, token string) {
	fmt.Fprintf(os.Stderr, `
  ┌──────────────────────────────────────────────────────────────────┐
  │  SynCloud is ready for setup                                     │
  │                                                                  │
  │  Dashboard:    http://%-42s│
  │  Setup token:  %-50s│
  │                                                                  │
  │  The token is valid for 1 hour. Restart the controller to get a  │
  │  new one if it expires.                                          │
  └──────────────────────────────────────────────────────────────────┘

`, addr, token)
}

// ControllerStats is published on the bus every few seconds; the dashboard's
// overview charts it until node metrics exist (Phase 1).
type ControllerStats struct {
	Goroutines int     `json:"goroutines"`
	HeapMB     float64 `json:"heapMB"`
	UptimeSec  int64   `json:"uptimeSec"`
}

func publishControllerStats(ctx context.Context, bus *events.Bus) {
	start := time.Now()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var m runtime.MemStats
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runtime.ReadMemStats(&m)
			bus.Publish("controller.stats", ControllerStats{
				Goroutines: runtime.NumGoroutine(),
				HeapMB:     float64(m.HeapAlloc) / (1 << 20),
				UptimeSec:  int64(time.Since(start).Seconds()),
			})
		}
	}
}

func cleanupSessions(ctx context.Context, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := st.DeleteExpiredJoinTokens(ctx, time.Now()); err != nil {
				log.Warn("cleanup join tokens", "err", err)
			}
			if n, err := st.DeleteExpiredSessions(ctx, time.Now()); err != nil {
				log.Warn("cleanup sessions", "err", err)
			} else if n > 0 {
				log.Debug("expired sessions removed", "count", n)
			}
		}
	}
}

// LocalNodeName is the controller's own node (§6.4).
const LocalNodeName = "ctl-0"

// ensureLocalJoinToken writes a single-use join token for the controller's own
// agent to <data>/local-join.token (0600) until node ctl-0 exists. The agent on
// this host joins with: syncloud-agent join --token-file <data>/local-join.token --name ctl-0
func ensureLocalJoinToken(ctx context.Context, st *store.Store, dataDir string) error {
	path := filepath.Join(dataDir, "local-join.token")
	if _, err := st.NodeByName(ctx, LocalNodeName); err == nil {
		_ = os.Remove(path)
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	tok := auth.NewToken("SYN-JOIN-")
	now := time.Now()
	err := st.CreateJoinToken(ctx, store.JoinToken{
		ID: auth.NewID("jt_"), TokenHash: auth.HashToken(tok), Description: "local agent (ctl-0)",
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), SingleUse: true,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// loadOrCreateToken reads a random secret from path, creating it (0600) once.
func loadOrCreateToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tok := auth.NewToken("")
	return tok, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// loopbackURLHost turns a listen address into something reachable from the
// host network namespace (":7070" or "0.0.0.0:7070" become "127.0.0.1:7070").
func loopbackURLHost(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
