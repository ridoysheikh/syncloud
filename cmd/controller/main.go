// Command syncloud-controller runs the SynCloud control plane.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
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
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // job schedules use IANA timezones even on hosts without tzdata

	"syncloud/internal/agentgw"
	"syncloud/internal/alerts"
	"syncloud/internal/api"
	"syncloud/internal/auth"
	"syncloud/internal/autoscale"
	"syncloud/internal/backup"
	"syncloud/internal/builds"
	"syncloud/internal/certs"
	"syncloud/internal/cli"
	"syncloud/internal/config"
	"syncloud/internal/discovery"
	"syncloud/internal/domain"
	"syncloud/internal/edge"
	"syncloud/internal/events"
	"syncloud/internal/execrelay"
	"syncloud/internal/fwstats"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/health"
	"syncloud/internal/jobs"
	"syncloud/internal/logs"
	"syncloud/internal/mesh"
	"syncloud/internal/metrics"
	"syncloud/internal/nodepool"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	"syncloud/internal/quota"
	dockerregistry "syncloud/internal/registry"
	"syncloud/internal/regmaint"
	"syncloud/internal/secrets"
	"syncloud/internal/shell"
	"syncloud/internal/store"
	"syncloud/internal/system"
	"syncloud/internal/traefik"
	"syncloud/internal/upstream"
	"syncloud/internal/version"
	"syncloud/internal/gc"
	"syncloud/internal/s3"
	"syncloud/internal/upgrade"
	"syncloud/internal/upgrade/rollout"
	"syncloud/internal/web"
	"syncloud/internal/workload"
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
       syncloud-controller upgrade [flags]   upgrade to a new release, rolling back if it is unhealthy
       syncloud-controller uninstall [--purge]  remove SynCloud from this host
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
		case "upgrade":
			return upgradeCmd(args[1:])
		case "upgrade-guard":
			return upgradeGuard(args[1:])
		case "uninstall":
			return uninstallCmd(args[1:])
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
	ctlPorts := []string{portOf(cfg.PublicHTTP), portOf(cfg.PublicHTTPS), portOf(cfg.AgentListen)}
	if host, _, _ := net.SplitHostPort(cfg.Listen); !isLoopback(host) {
		ctlPorts = append(ctlPorts, portOf(cfg.Listen)) // the API was exposed on purpose
	}
	// Edge nodes open HTTP and HTTPS; node pools say which nodes are edges.
	var edgeCheck atomic.Pointer[func(string) bool]
	isEdge := func(id string) bool {
		if f := edgeCheck.Load(); f != nil {
			return (*f)(id)
		}
		return false
	}
	meshMgr := mesh.NewManager(st, gw, bus, log, detector.PublicIP, mesh.Options{Firewall: cfg.Firewall, ControllerPorts: ctlPorts, IsEdge: isEdge})
	gw.AddHooks(meshMgr.Hooks())
	go meshMgr.Run(ctx)
	var workloads *workload.Manager // set below; routes need certificates too
	certHosts := func(ep domain.Endpoints) []string {
		if ep.BaseDomain == "" {
			return nil
		}
		hosts := []string{ep.BaseDomain, domain.RegistryHost(ep.BaseDomain)}
		if workloads != nil {
			for _, r := range workloads.Routes(context.Background(), ep.BaseDomain) {
				hosts = append(hosts, r.Host)
			}
		}
		return hosts
	}
	workloads = workload.NewManager(st, gw, registry, meshMgr.NetworkReady, bus, log)
	workloads.OnChange = func() { certMgr.SetHosts(certHosts(domains.Endpoints())) }
	certMgr.SetHosts(certHosts(domains.Endpoints()))
	gw.AddHooks(workloads.Hooks())
	go workloads.Run(ctx)
	registryHost := func() string {
		if cfg.RegistryPullHost != "" {
			return cfg.RegistryPullHost
		}
		return domains.Endpoints().RegistryHost
	}
	// "@registry/shop/api:tag" in a task definition means the private
	// registry; nodes get a pull-only token for that repository (§5.9).
	upstreams := upstream.New(st, box)
	workloads.ResolveImage = func(image string) (string, string) {
		path, ok := strings.CutPrefix(image, "@registry/")
		if !ok {
			// Third-party registries: stored upstream credentials, if any.
			return image, upstreams.RegistryAuth(context.Background(), image)
		}
		host := registryHost()
		repo := path
		if i := strings.IndexByte(repo, '@'); i >= 0 {
			repo = repo[:i]
		}
		if i := strings.LastIndexByte(repo, ':'); i > strings.LastIndexByte(repo, '/') {
			repo = repo[:i]
		}
		tok, err := regIssuer.IssueTTL("node", []dockerregistry.Access{{Type: "repository", Name: repo, Actions: []string{"pull"}}}, time.Now(), 30*time.Minute)
		if err != nil {
			return host + "/" + path, ""
		}
		authJSON, _ := json.Marshal(map[string]string{"registrytoken": tok})
		return host + "/" + path, base64.URLEncoding.EncodeToString(authJSON)
	}
	disco := discovery.NewManager(st, gw, workloads, log)
	disco.Security = cfg.SecurityGroups
	gw.AddHooks(disco.Hooks())
	go disco.Run(ctx)
	workloads.OnTaskChange = disco.Kick
	prevOnChange := workloads.OnChange
	workloads.OnChange = func() { prevOnChange(); disco.Kick() }
	workloads.DNS = func(nodeID string, sv store.Service) ([]string, []string) {
		nn, err := st.NodeNetwork(context.Background(), nodeID)
		if err != nil || !meshMgr.IsMember(nodeID) {
			return nil, nil // no private network (development): Docker's default DNS
		}
		return []string{mesh.Subnet(nn.SubnetIndex).Addr().Next().String()}, discovery.SearchDomains(sv)
	}
	workload.Discovery = func(sv store.Service) (string, string) { return disco.VIP(sv.ID), discovery.ServiceName(sv) }
	jobMgr := jobs.NewManager(st, gw, workloads, registry, bus, log)
	gw.AddHooks(jobMgr.Hooks())
	jobMgr.OnRunAddress = disco.Kick
	workloads.DeployHooks = jobMgr
	go jobMgr.Run(ctx)
	buildMgr := builds.New(st, box, jobMgr, workloads, regIssuer, bus, log, builds.Config{
		RegistryHost: registryHost, RegistryInsecure: cfg.RegistryInsecure, Node: cfg.BuildNode,
		UpstreamAuths: func() map[string]any { return upstreams.DockerConfigAuths(context.Background()) },
	})
	go buildMgr.Run(ctx)
	// Task resource samples ride on agent heartbeats (§9.1).
	metricStore := metrics.New(cfg.VictoriaMetricsURL, log)
	go metricStore.Run(ctx)
	gw.AddHooks(agentgw.Hooks{OnHeartbeat: func(node store.Node, hb *agentv1.Heartbeat) {
		metricStore.Add(node.Name, hb.GetTasks())
		metricStore.AddNode(node.Name, hb)
	}})
	if cfg.SystemTasks {
		// Traefik's request metrics, labelled with SynCloud's names (§5.7).
		serviceNames := func(ctx context.Context) map[string][3]string {
			out := map[string][3]string{}
			svcs, _ := st.ListServices(ctx)
			for _, sv := range svcs {
				out[sv.ID] = [3]string{sv.Project, sv.Environment, sv.Name}
			}
			return out
		}
		go metricStore.ScrapeTraefik(ctx, "http://"+cfg.TraefikAdmin+"/metrics", LocalNodeName, serviceNames, 10*time.Second)
	}
	pools := nodepool.New(st, box, registry, workloads, bus, log)
	if err := pools.Reload(ctx); err != nil {
		return fmt.Errorf("load node pools: %w", err)
	}
	pools.ControllerURL = func() string { return domains.Endpoints().DashboardURL }
	workloads.NodePool = pools.PoolOf
	s3Mgr := s3.New(st, box)
	workloads.S3Bindings = func(ctx context.Context, serviceID string) ([]workload.S3Ref, error) {
		bs, err := st.ServiceS3Bindings(ctx, serviceID)
		if err != nil {
			return nil, err
		}
		var refs []workload.S3Ref
		for _, b := range bs {
			refs = append(refs, workload.S3Ref{Endpoint: b.Endpoint, Bucket: b.Bucket, Prefix: b.Prefix, EnvPrefix: b.EnvPrefix})
		}
		return refs, nil
	}
	workloads.S3Env = func(ctx context.Context, ref workload.S3Ref) (map[string]string, error) {
		c, err := s3Mgr.Credentials(ctx, ref.Endpoint)
		if err != nil {
			return nil, err
		}
		return s3.Env(ref.EnvPrefix, ref.Bucket, ref.Prefix, c), nil
	}
	edgeFn := func(id string) bool { _, role := pools.PoolOf(id); return role == "edge" }
	edgeCheck.Store(&edgeFn)
	go pools.Run(ctx)
	quotas := quota.New(st, workloads, metricStore, log)
	workloads.Admit = quotas.Admit
	workloads.AdmitCount = quotas.AdmitCount
	jobMgr.AdmitCount = quotas.AdmitCount
	buildMgr.BuildSlots = quotas.BuildSlots
	go quotas.Run(ctx)
	autoscaler := autoscale.New(st, workloads, metricStore, bus, log)
	go autoscaler.Run(ctx)
	healthMon := health.New(st, workloads, bus, log, health.Config{
		BaseDomain: domains.Base, HTTPAddr: cfg.PublicHTTP, HTTPSAddr: cfg.PublicHTTPS, VictoriaMetricsURL: cfg.VictoriaMetricsURL,
	})
	go healthMon.Run(ctx)
	if cfg.CentralProbes {
		workloads.Reachable = healthMon.TaskReachable
		workload.CentralCheck = func(id string) (string, string) {
			if c := healthMon.TaskCheckOf(id); c != nil {
				return c.State, c.Error
			}
			return "", ""
		}
		go healthMon.RunTaskProbes(ctx)
	}
	execs := execrelay.New(gw)
	gw.AddHooks(execs.Hooks())
	regBrowser := &dockerregistry.Browser{URL: "http://" + system.RegistryAddr, Issuer: regIssuer}
	// Push, pull and delete notifications from the registry (§5.9).
	regEvents := &dockerregistry.Events{
		Store: st, Token: traefikToken, TokenHeader: system.TraefikTokenHeader, Log: log,
		OnEvent: func(repos []string) { bus.Publish("registry.event", repos) },
	}
	go regEvents.Prune(ctx)
	// Lifecycle policies and garbage collection need the registry system task.
	var regMaint *regmaint.Manager
	if cfg.SystemTasks {
		regMaint = regmaint.New(st, regBrowser, sysMgr, execs, workloads, bus, log)
		regMaint.RegistryHosts = func() []string { return []string{registryHost(), domains.Endpoints().RegistryHost} }
		go regMaint.Run(ctx)
	}
	logStore := logs.New(st, cfg.VictoriaLogsURL, log)
	logStore.OnIngest = quotas.AddLogBytes
	gw.AddHooks(agentgw.Hooks{OnLogs: logStore.OnLogs})
	go logStore.Run(ctx)
	fwStats := fwstats.New(st, metricStore, logStore, log)
	gw.AddHooks(agentgw.Hooks{OnHeartbeat: fwStats.OnHeartbeat})
	alertMgr := alerts.New(st, box, metricStore, logStore, healthMon, bus, log)
	alertMgr.DashboardURL = func() string { return domains.Endpoints().DashboardURL }
	go alertMgr.Run(ctx)
	workload.Endpoints = func(sv store.Service, spec workload.Spec, custom []store.Domain) []string {
		scheme, port, base := "https", httpsPort, domains.Base()
		if base == "" {
			scheme, port = "http", devPort(httpPort)
		}
		out := workload.ServiceEndpoints(sv, spec, base, scheme, port)
		for _, d := range custom {
			u := scheme + "://" + d.Host
			if port != "" {
				u += ":" + port
			}
			out = append(out, u)
		}
		return out
	}
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
	meshControllerURL := func() string { return "http://" + net.JoinHostPort(mesh.MeshAddr(1).String(), portOf(cfg.Listen)) }
	edges := edge.New(st, gw, metricStore, func(ctx context.Context) map[string][3]string {
		out := map[string][3]string{}
		svcs, _ := st.ListServices(ctx)
		for _, sv := range svcs {
			out[sv.ID] = [3]string{sv.Project, sv.Environment, sv.Name}
		}
		return out
	}, edge.Config{Image: system.ImageTraefik, TraefikToken: traefikToken, TokenHeader: system.TraefikTokenHeader, ControllerURL: meshControllerURL,
		Edges: pools.EdgeNodes}, log)
	gw.AddHooks(edges.Hooks())
	go edges.Run(ctx)
	traefikExtras := traefik.NewExtras(st, log)
	if err := traefikExtras.Reload(ctx); err != nil {
		return fmt.Errorf("load Traefik middlewares: %w", err)
	}
	traefikProvider := &traefik.Provider{
		Token: traefikToken, TokenHeader: system.TraefikTokenHeader, ControllerURL: controllerURL,
		RegistryURL: "http://" + system.RegistryAddr, BaseDomain: domains.Base, HTTPSPort: httpsPort,
		ServiceRoutes: func() []traefik.ServiceRoute {
			var out []traefik.ServiceRoute
			for _, r := range workloads.Routes(context.Background(), domains.Base()) {
				chain, own := traefikExtras.Chain(r.ServiceID)
				out = append(out, traefik.ServiceRoute{Name: r.Name, Host: r.Host, Servers: r.Servers, Middlewares: chain, OwnRetry: own})
			}
			return out
		},
		Middlewares:       traefikExtras.Definitions,
		MeshControllerURL: meshControllerURL,
		Custom:            traefikExtras.Custom,
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

	// Cloud Shell runs on the controller node and reaches the API over the
	// private network (the listener below).
	shellSynctl := cfg.ShellSynctl
	if shellSynctl == "" {
		if p := filepath.Join(cfg.DownloadsDir, "synctl-linux-"+runtime.GOARCH); fileExists(p) {
			shellSynctl = p
		}
	}
	shells := shell.New(gw, shell.Config{
		Image: cfg.ShellImage, Synctl: shellSynctl,
		Endpoint: func() string { return "http://" + net.JoinHostPort(mesh.MeshAddr(1).String(), portOf(cfg.Listen)) },
		Node: func() (store.Node, bool) {
			n, err := st.NodeByName(context.Background(), mesh.ControllerNode)
			return n, err == nil
		},
	}, log)
	gw.AddHooks(shells.Hooks())
	go shells.Run(ctx)

	upgrades := &upgrade.Service{
		DataDir: cfg.DataDir, DBPath: cfg.DBPath(), Downloads: cfg.DownloadsDir, Source: upgrade.Source{Base: cfg.ReleaseURL},
		Channel: cfg.ReleaseChannel, Current: version.Version, Settle: cfg.UpgradeSettle, Snapshot: snapshotDB(cfg.DBPath()),
	}
	agentRollout := rollout.New(gw, registry, cfg.DownloadsDir, version.Version, log)
	go gc.Run(ctx, st, cfg.DataDir, store.DefaultRetention, log)
	gw.AddHooks(agentRollout.Hooks())

	api.CLICommands = cli.OperationCommands
	srv := api.New(api.Options{
		Store: st, Secrets: box, CA: ca, Nodes: registry, GatewayAddr: cfg.AgentAdvertise,
		System: sysMgr, Registry: regIssuer,
		Internal: map[string]http.Handler{
			"GET /internal/traefik/config":      traefikProvider,
			"POST " + system.RegistryEventsPath: regEvents,
			"GET " + certs.ChallengePrefix:      certMgr,
		},
		Domains: domains, Detector: detector, Certs: certMgr, Backups: backups, Mesh: meshMgr,
		DownloadsDir: cfg.DownloadsDir, Workloads: workloads, Logs: logStore, Exec: execs, Jobs: jobMgr, Health: healthMon,
		RegistryBrowser:       regBrowser,
		RegistryMaint:         regMaint,
		Upstreams:             upstreams,
		RegistryHosts:         func() []string { return []string{registryHost(), domains.Endpoints().RegistryHost} },
		Builds:                buildMgr,
		Metrics:               metricStore,
		Autoscaler:            autoscaler,
		Alerts:                alertMgr,
		SecurityGroups:        cfg.SecurityGroups,
		OnSecurityChange:      disco.Kick,
		FirewallStats:         fwStats,
		Quotas:                quotas,
		Shell:                 shells,
		Pools:                 pools,
		Edges:                 edges,
		Upgrades:              upgrades,
		S3:                    s3Mgr,
		AgentRollout:          agentRollout,
		Discovery:             disco,
		Traefik:               traefikProvider,
		TraefikExtras:         traefikExtras,
		ControllerSchedulable: cfg.ControllerSchedulable,
		ACME:                  api.ACMEInfo{Enabled: cfg.ACME, DirectoryURL: cfg.ACMEDirectory, Email: cfg.ACMEEmail},
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

	if err := upgrade.WriteRunInfo(filepath.Join(cfg.DataDir, upgrade.RunInfoFile), controllerURL+"/api/v1/system/health"); err != nil {
		log.Warn("record run info (upgrades need it)", "err", err)
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
	go serveOnMesh(ctx, cfg.Listen, httpSrv, log)

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
		if svc, ok := domain.WildcardService(cur); ok && (!cfg.Dev || cfg.PublicIP != "") {
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

func portOf(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// devPort is the port to put in development URLs ("" for 80).
func devPort(p string) string {
	if p == "80" {
		return ""
	}
	return p
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// serveOnMesh also serves the API on the controller's private-network address
// (10.90.0.1), for Cloud Shell and other nodes, once the local agent has
// brought the mesh up. It is not needed when the API already listens on
// every address.
func serveOnMesh(ctx context.Context, listen string, srv *http.Server, log *slog.Logger) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return
	}
	addr := net.JoinHostPort(mesh.MeshAddr(1).String(), port)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			log.Info("API listening on the private network", "addr", addr)
			go func() { <-ctx.Done(); ln.Close() }()
			_ = srv.Serve(ln)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
