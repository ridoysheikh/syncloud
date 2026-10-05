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
	"runtime"
	"syscall"
	"time"

	"syncloud/internal/api"
	"syncloud/internal/auth"
	"syncloud/internal/config"
	"syncloud/internal/events"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
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

	bus := events.NewBus()
	srv := api.New(api.Options{Store: st, Secrets: box, Bus: bus, Log: log, Web: web.FS()})

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
			if n, err := st.DeleteExpiredSessions(ctx, time.Now()); err != nil {
				log.Warn("cleanup sessions", "err", err)
			} else if n > 0 {
				log.Debug("expired sessions removed", "count", n)
			}
		}
	}
}
