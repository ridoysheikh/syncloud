// Command syncloud-controller runs the SynCloud control plane.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"syncloud/internal/backup"
	"syncloud/internal/certs"
	"syncloud/internal/config"
	"syncloud/internal/domain"
	"syncloud/internal/events"
	"syncloud/internal/mesh"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	dockerregistry "syncloud/internal/registry"
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

const usage = `Usage: syncloud-controller [serve] [flags]   run the controller (default)
       syncloud-controller doctor [flags]    check every component and print fixes
       syncloud-controller restore [flags]   restore a backup into the data directory
       syncloud-controller version`

func run(args []string) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "serve":
			return serve(args[1:])
		case "doctor":
			return doctor(args[1:])
		case "restore":
			return restore(args[1:])
		case "version":
			fmt.Println(version.Version)
			return nil
		case "help":
			fmt.Println(usage)
			return nil
		}
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	return serve(args)
}

func serve(args []string) error {
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

	box, recoveryKey, err := secrets.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return err
	}
	if recoveryKey != "" {
		if err := st.SetSetting(ctx, store.SettingRecoverySuffixHash, auth.HashToken(secrets.RecoveryKeySuffix(recoveryKey))); err != nil {
			return err
		}
		// For install.sh to show once; removed when setup completes.
		if err := os.WriteFile(filepath.Join(cfg.DataDir, recoveryKeyFile), []byte(recoveryKey+"\n"), 0o600); err != nil {
			return err
		}
	}

	token, err := auth.EnsureSetupToken(ctx, st, time.Now())
	if err != nil {
		return fmt.Errorf("setup token: %w", err)
	}
	if token != "" {
		if err := os.WriteFile(filepath.Join(cfg.DataDir, setupTokenFile), []byte(token+"\n"), 0o600); err != nil {
			return err
		}
	} else {
		_ = os.Remove(filepath.Join(cfg.DataDir, setupTokenFile))
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
	regIssuer, err := dockerregistry.LoadOrCreateIssuer(filepath.Join(cfg.DataDir, "registry"))
	if err != nil {
		return fmt.Errorf("registry token issuer: %w", err)
	}
	_, httpsPort, _ := net.SplitHostPort(cfg.PublicHTTPS)
	_, httpPort, _ := net.SplitHostPort(cfg.PublicHTTP)
	devURL := "http://localhost"
	if httpPort != "" && httpPort != "80" {
		devURL += ":" + httpPort
	}
	domains := domain.NewService(st, httpsPort, devURL, traefik.DevRegistryHost)
	if err := domains.Load(ctx); err != nil {
		return err
	}
	detector := domain.NewDetector(cfg.PublicIP)
	if err := initBaseDomain(ctx, cfg, domains, detector, log); err != nil {
		return err
	}

	acmeHTTP, err := acmeHTTPClient(cfg.ACMECAFile)
	if err != nil {
		return err
	}
	certMgr := certs.New(st, box, bus, log, certs.Config{
		ACME: cfg.ACME, DirectoryURL: cfg.ACMEDirectory, Email: cfg.ACMEEmail, HTTPClient: acmeHTTP,
	})
	if err := certMgr.Load(ctx); err != nil {
		return fmt.Errorf("load certificates: %w", err)
	}

	controllerURL := "http://" + loopbackURLHost(cfg.Listen)
	sysCfg := func(ep domain.Endpoints) system.Config {
		return system.Config{
			ControllerURL: controllerURL, HTTPAddr: cfg.PublicHTTP, HTTPSAddr: cfg.PublicHTTPS,
			AdminAddr: cfg.TraefikAdmin, TraefikToken: traefikToken,
			RegistryRealm: ep.DashboardURL + "/api/v1/registry/token", RegistryTokenCert: regIssuer.CertPath,
		}
	}
	sysMgr := system.NewManager(gw, bus, log, sysCfg(domains.Endpoints()))
	if cfg.SystemTasks {
		gw.AddHooks(sysMgr.Hooks())
		go sysMgr.Run(ctx)
	}
	meshMgr := mesh.NewManager(st, gw, bus, log, detector.PublicIP)
	gw.AddHooks(meshMgr.Hooks())
	go meshMgr.Run(ctx)
	certHosts := func(ep domain.Endpoints) []string {
		if ep.BaseDomain == "" {
			return nil
		}
		return []string{ep.BaseDomain, domain.RegistryHost(ep.BaseDomain)}
	}
	certMgr.SetHosts(certHosts(domains.Endpoints()))
	domains.OnChange(func(ep domain.Endpoints) {
		log.Info("base domain changed", "domain", ep.BaseDomain, "dashboard", ep.DashboardURL)
		sysMgr.SetConfig(sysCfg(ep)) // the registry's token realm follows the domain
		certMgr.SetHosts(certHosts(ep))
		bus.Publish("domain.updated", ep)
	})
	go certMgr.Run(ctx)

	if httpsPort == "443" {
		httpsPort = ""
	}
	traefikProvider := &traefik.Provider{
		Token: traefikToken, TokenHeader: system.TraefikTokenHeader, ControllerURL: controllerURL,
		RegistryURL: "http://" + system.RegistryAddr, BaseDomain: domains.Base, HTTPSPort: httpsPort,
		Certificates: func() []traefik.Certificate {
			var out []traefik.Certificate
			for _, p := range certMgr.Pairs() {
				out = append(out, traefik.Certificate{CertFile: p.CertPEM, KeyFile: p.KeyPEM})
			}
			return out
		},
	}

	backups := backup.NewManager(st, box, cfg.DataDir, bus, log)
	if err := backups.Load(ctx); err != nil {
		return fmt.Errorf("load backup settings: %w", err)
	}
	go backups.Run(ctx)

	srv := api.New(api.Options{
		Store: st, Secrets: box, CA: ca, Nodes: registry, GatewayAddr: cfg.AgentAdvertise,
		System: sysMgr, Registry: regIssuer,
		Internal: map[string]http.Handler{
			"GET /internal/traefik/config": traefikProvider,
			"GET " + certs.ChallengePrefix: certMgr,
		},
		Domains: domains, Detector: detector, Certs: certMgr, Backups: backups, Mesh: meshMgr,
		DownloadsDir: cfg.DownloadsDir,
		ACME:         api.ACMEInfo{Enabled: cfg.ACME, DirectoryURL: cfg.ACMEDirectory, Email: cfg.ACMEEmail},
		OnSetup: func() {
			_ = os.Remove(filepath.Join(cfg.DataDir, setupTokenFile))
			_ = os.Remove(filepath.Join(cfg.DataDir, recoveryKeyFile))
		},
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
		printSetupBanner(domains.Endpoints().DashboardURL, token, recoveryKey)
	} else if recoveryKey != "" {
		log.Warn("a recovery key was created for this install; save it now, it is needed to restore backups", "recovery_key", recoveryKey,
			"file", filepath.Join(cfg.DataDir, recoveryKeyFile))
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

// Files install.sh reads to show the first-run secrets (both 0600).
const (
	setupTokenFile  = "setup-token"
	recoveryKeyFile = "recovery-key"
)

func printSetupBanner(url, token, recoveryKey string) {
	line := func(label, v string) { fmt.Fprintf(os.Stderr, "  %-14s %s\n", label, v) }
	fmt.Fprintln(os.Stderr, "\n  SynCloud is ready for setup")
	fmt.Fprintln(os.Stderr, "  "+strings.Repeat("─", 66))
	line("Dashboard:", url)
	line("Setup token:", token+"  (valid 1 hour; restart for a new one)")
	if recoveryKey != "" {
		line("Recovery key:", recoveryKey)
		fmt.Fprintln(os.Stderr, "\n  Store the recovery key somewhere safe. It is shown only once and is")
		fmt.Fprintln(os.Stderr, "  needed to restore backups. Setup asks for its last 6 characters.")
	}
	fmt.Fprintln(os.Stderr, "  "+strings.Repeat("─", 66)+"\n")
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

// initBaseDomain stores the first base domain: --base-domain, or outside dev
// mode <public-ip>.sslip.io (§5.0.2). An existing setting is never replaced.
func initBaseDomain(ctx context.Context, cfg config.Controller, domains *domain.Service, det *domain.Detector, log *slog.Logger) error {
	if cur := domains.Base(); cur != "" {
		// After a restore on a new host, an sslip.io/nip.io domain still names the old IP.
		if svc, ok := domain.WildcardService(cur); ok && !cfg.Dev {
			dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			ip, err := det.PublicIP(dctx)
			cancel()
			if want := domain.Wildcard(ip, svc); err == nil && want != cur {
				log.Warn("public IP changed: moving the base domain", "from", cur, "to", want)
				_, err = domains.Set(ctx, want)
				return err
			}
		}
		return nil
	}
	base := cfg.BaseDomain
	if base == "" {
		if cfg.Dev {
			return nil
		}
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		ip, err := det.PublicIP(dctx)
		cancel()
		if err != nil {
			log.Warn("no base domain: public IP detection failed; set one with --base-domain or in Settings → Domains", "err", err)
			return nil
		}
		base = domain.Wildcard(ip, "sslip.io")
	}
	if _, err := domains.Set(ctx, base); err != nil {
		return fmt.Errorf("--base-domain: %w", err)
	}
	log.Info("base domain set", "domain", base)
	return nil
}

// acmeHTTPClient trusts the system roots plus caFile, if given.
func acmeHTTPClient(caFile string) (*http.Client, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	if caFile == "" {
		return c, nil
	}
	pemData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("--acme-ca-file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("--acme-ca-file: no certificates in %s", caFile)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	c.Transport = t
	return c, nil
}
