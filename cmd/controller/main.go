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
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // job schedules use IANA timezones even on hosts without tzdata

	"github.com/ridoysheikh/syncloud/internal/agentgw"
	"github.com/ridoysheikh/syncloud/internal/alerts"
	"github.com/ridoysheikh/syncloud/internal/api"
	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/autoscale"
	"github.com/ridoysheikh/syncloud/internal/backup"
	"github.com/ridoysheikh/syncloud/internal/builds"
	"github.com/ridoysheikh/syncloud/internal/certs"
	"github.com/ridoysheikh/syncloud/internal/cli"
	"github.com/ridoysheikh/syncloud/internal/config"
	"github.com/ridoysheikh/syncloud/internal/dbs"
	"github.com/ridoysheikh/syncloud/internal/discovery"
	"github.com/ridoysheikh/syncloud/internal/domain"
	"github.com/ridoysheikh/syncloud/internal/edge"
	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/execrelay"
	"github.com/ridoysheikh/syncloud/internal/fwstats"
	"github.com/ridoysheikh/syncloud/internal/gc"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/gitconn"
	"github.com/ridoysheikh/syncloud/internal/gitremote"
	"github.com/ridoysheikh/syncloud/internal/gitserver"
	"github.com/ridoysheikh/syncloud/internal/health"
	"github.com/ridoysheikh/syncloud/internal/jobs"
	"github.com/ridoysheikh/syncloud/internal/logs"
	"github.com/ridoysheikh/syncloud/internal/mesh"
	"github.com/ridoysheikh/syncloud/internal/metrics"
	"github.com/ridoysheikh/syncloud/internal/nodepool"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/pki"
	"github.com/ridoysheikh/syncloud/internal/quota"
	dockerregistry "github.com/ridoysheikh/syncloud/internal/registry"
	"github.com/ridoysheikh/syncloud/internal/regmaint"
	"github.com/ridoysheikh/syncloud/internal/s3"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/shell"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/sysimage"
	"github.com/ridoysheikh/syncloud/internal/system"
	"github.com/ridoysheikh/syncloud/internal/traefik"
	"github.com/ridoysheikh/syncloud/internal/upgrade"
	"github.com/ridoysheikh/syncloud/internal/upgrade/rollout"
	"github.com/ridoysheikh/syncloud/internal/upstream"
	"github.com/ridoysheikh/syncloud/internal/version"
	"github.com/ridoysheikh/syncloud/internal/web"
	"github.com/ridoysheikh/syncloud/internal/workload"
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
	publicIP := cfg.PublicIP
	if cfg.Dev && publicIP == "" {
		// Development and tests: the address this machine is reached at,
		// not the NAT's (sslip.io names, suggestions).
		publicIP = devAddress(cfg)
	}
	detector := domain.NewDetector(publicIP)
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
	// platformCA is what to trust for a platform host while its certificate
	// is self-signed (private networks): the registry bundle includes the
	// dashboard, where Docker fetches registry tokens.
	platformCA := func(host string) string {
		base := domains.Base()
		if base != "" && host == domain.RegistryHost(base) {
			return certMgr.SelfSignedBundle(host, base)
		}
		return certMgr.SelfSignedPEM(host)
	}
	if err := certMgr.Load(ctx); err != nil {
		return fmt.Errorf("load certificates: %w", err)
	}

	controllerURL := "http://" + loopbackURLHost(cfg.Listen)
	traefikExtras := traefik.NewExtras(st, log)
	if err := traefikExtras.Reload(ctx); err != nil {
		return fmt.Errorf("load Traefik settings and middlewares: %w", err)
	}
	gitServer := gitserver.New(st, box, domains.Endpoints, log)
	gitServer.CADir = filepath.Join(cfg.DataDir, "gitserver-ca")
	gitServer.TrustPEM = func() string {
		// The dashboard (webhook target) while its certificate is self-signed.
		base := domains.Base()
		if base == "" {
			return ""
		}
		return certMgr.SelfSignedBundle(base)
	}
	if err := gitServer.Load(ctx); err != nil {
		return fmt.Errorf("load the built-in Git server settings: %w", err)
	}
	var workloads *workload.Manager // set below; routes need certificates too
	// serviceEntrypoints are the Traefik entrypoints of public service ports
	// (every interface, in development too: the dashboard is reached over
	// the network at its sslip.io name).
	serviceEntrypoints := func() map[string]string {
		out := map[string]string{}
		if workloads == nil {
			return out
		}
		for _, r := range workloads.PublicRoutes(context.Background()) {
			addr := ":" + fmt.Sprint(r.Port)
			if r.Protocol == "udp" {
				addr += "/udp"
			}
			out[r.Entrypoint()] = addr
		}
		return out
	}
	sysCfg := func(ep domain.Endpoints) system.Config {
		return system.Config{
			GitServer:       gitServer.Config(),
			TraefikSettings: traefikExtras.Settings(),
			ControllerURL:   controllerURL, HTTPAddr: cfg.PublicHTTP, HTTPSAddr: cfg.PublicHTTPS,
			AdminAddr: cfg.TraefikAdmin, TraefikToken: traefikToken, DatabaseEntrypoints: mergeEntrypoints(map[string]string{"valkey": cfg.PublicValkey, "postgres": cfg.PublicPostgres}, serviceEntrypoints()),
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
	// Public databases open their engine's port on the controller and edges.
	var dbMgr *dbs.Manager // set below
	dbPorts := map[string]string{dbs.EngineValkey: portOf(cfg.PublicValkey), dbs.EnginePostgres: portOf(cfg.PublicPostgres)}
	publicPorts := func() []mesh.PublicPort {
		var out []mesh.PublicPort
		if dbMgr != nil {
			for _, e := range dbMgr.PublicEngines(context.Background()) {
				if p := dbPorts[e]; p != "" {
					out = append(out, mesh.PublicPort{Port: p, Protocol: "tcp", Kind: "database"})
				}
			}
		}
		if workloads != nil {
			for _, r := range workloads.PublicRoutes(context.Background()) {
				out = append(out, mesh.PublicPort{Port: fmt.Sprint(r.Port), Protocol: r.Protocol, Sources: r.Allow, Kind: "service"})
			}
		}
		return out
	}
	meshMgr := mesh.NewManager(st, gw, bus, log, detector.PublicIP, mesh.Options{Firewall: cfg.Firewall, ControllerPorts: ctlPorts, IsEdge: isEdge, PublicPorts: publicPorts})
	gw.AddHooks(meshMgr.Hooks())
	go meshMgr.Run(ctx)
	certHosts := func(ep domain.Endpoints) []string {
		if ep.BaseDomain == "" {
			return nil
		}
		hosts := []string{ep.BaseDomain, domain.RegistryHost(ep.BaseDomain)}
		if h := gitServer.Host(); h != "" {
			hosts = append(hosts, h)
		}
		if workloads != nil {
			for _, r := range workloads.Routes(context.Background(), ep.BaseDomain) {
				hosts = append(hosts, r.Host)
			}
		}
		if dbMgr != nil {
			hosts = append(hosts, dbMgr.PublicHosts(context.Background(), ep.BaseDomain)...)
		}
		return hosts
	}
	workloads = workload.NewManager(st, gw, registry, meshMgr.NetworkReady, bus, log)
	if lo, hi, ok := portRange(cfg.PublicPorts); ok {
		workloads.PublicPortRange = [2]int{lo, hi}
	} else {
		return fmt.Errorf("--public-ports %q: want LOW-HIGH within 1024–65535", cfg.PublicPorts)
	}
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
	// On a private network the registry's certificate is self-signed: nodes
	// are handed it with each pull and Docker trusts it for that registry.
	workloads.RegistryCA = func(ref string) string {
		ep := domains.Endpoints()
		if ep.BaseDomain == "" || !strings.HasPrefix(ref, ep.RegistryHost+"/") {
			return ""
		}
		return platformCA(domain.RegistryHost(ep.BaseDomain))
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
		SelfSignedCA:  platformCA,
	})
	dashboardURL := func() string { return domains.Endpoints().DashboardURL }
	gitConns := gitconn.New(st, box, dashboardURL, log)
	buildMgr.Connections, buildMgr.DashboardURL = gitConns, dashboardURL
	gitremote.SelfSignedCA = certMgr.SelfSignedPEM
	go buildMgr.Run(ctx)
	// Task resource samples ride on agent heartbeats (§9.1).
	metricStore := metrics.New(cfg.VictoriaMetricsURL, log)
	go metricStore.Run(ctx)
	// Managed Valkey databases (Phase 12).
	dbMgr = dbs.New(st, gw, workloads, registry, box, bus, log)
	dbMgr.Metrics = metricStore
	dbMgr.BaseDomain = domains.Base
	dbMgr.PostgresImages, err = postgresImages(cfg.PostgresImage)
	if err != nil {
		return err
	}
	dbMgr.OnChange = disco.Kick
	dbMgr.OnNetworkChange = func() {
		certMgr.SetHosts(certHosts(domains.Endpoints()))
		meshMgr.Changed(context.Background())
	}
	dbMgr.DNS = func(nodeID, project, env string) ([]string, []string) {
		nn, err := st.NodeNetwork(context.Background(), nodeID)
		if err != nil || !meshMgr.IsMember(nodeID) {
			return nil, nil
		}
		search := []string{env + "." + project + "." + discovery.Zone, project + "." + discovery.Zone, discovery.Zone}
		if project == "" { // standalone
			search = []string{"db." + discovery.Zone, discovery.Zone}
		}
		return []string{mesh.Subnet(nn.SubnetIndex).Addr().Next().String()}, search
	}
	// The managed PostgreSQL images come with the release and are loaded
	// into the built-in registry the first time a database needs one.
	dbMgr.ResolveImage = workloads.ResolveImage
	dbMgr.RegistryCA = workloads.RegistryCA
	seeder := &sysimage.Seeder{
		Registry: &dockerregistry.Browser{URL: "http://" + system.RegistryAddr, Issuer: regIssuer},
		Source:   upgrade.Source{Base: cfg.ReleaseURL},
		Version:  version.Version,
		Dir:      filepath.Join(cfg.DownloadsDir, "images"),
		Log:      log,
	}
	dbMgr.ImageReady = seeder.Ensure
	gw.AddHooks(dbMgr.Hooks())
	disco.Databases = dbMgr
	workloads.ExtraUsage = dbMgr.Usage
	go dbMgr.Run(ctx)
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
	pools.Pin = func() string { return certMgr.Pin(domains.Endpoints().BaseDomain) }
	workloads.NodePool = pools.PoolOf
	s3Mgr := s3.New(st, box)
	dbMgr.S3 = func(ctx context.Context, ref string) (dbs.S3Access, error) {
		c, err := s3Mgr.Credentials(ctx, ref)
		return dbs.S3Access{URL: c.URL, Region: c.Region, AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, PathStyle: c.PathStyle}, err
	}
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
	gitServer.Conns, gitServer.Exec, gitServer.Sys = gitConns, execs, sysMgr
	gitServer.OnChange = func() {
		sysMgr.SetConfig(sysCfg(domains.Endpoints()))
		certMgr.SetHosts(certHosts(domains.Endpoints()))
	}
	go gitServer.Run(ctx)
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
		workloads.ImageAvailable = regMaint.ImageAvailable
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
	workload.Endpoints = func(sv store.Service, spec workload.Spec, custom []store.Domain, routing []store.PortRouting) []string {
		scheme, port, base := "https", httpsPort, domains.Base()
		if base == "" {
			scheme, port = "http", devPort(httpPort)
		}
		out := workload.ServiceEndpoints(sv, spec, base, scheme, port, routing)
		for _, d := range custom {
			if d.RedirectTo != "" {
				continue // not an address of the service
			}
			u := scheme + "://" + d.Host
			if port != "" {
				u += ":" + port
			}
			out = append(out, u+d.Path)
		}
		for _, r := range routing {
			for _, p := range spec.Ports {
				if p.Name == r.PortName && r.PublicPort > 0 && p.Protocol != "http" {
					out = append(out, p.Protocol+"://"+workload.PublicAddress(base, r.PublicPort))
				}
			}
		}
		return out
	}
	domains.OnChange(func(ep domain.Endpoints) {
		log.Info("base domain changed", "domain", ep.BaseDomain, "dashboard", ep.DashboardURL)
		sysMgr.SetConfig(sysCfg(ep)) // the registry's token realm follows the domain
		certMgr.SetHosts(certHosts(ep))
		gitServer.Kick() // its address follows the domain
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
		Edges: pools.EdgeNodes, Settings: traefikExtras.Settings, DatabaseEntrypoints: map[string]string{"valkey": ":6379", "postgres": ":5432"},
		ServiceEntrypoints: serviceEntrypoints}, log)
	gw.AddHooks(edges.Hooks())
	go edges.Run(ctx)
	// The system specs were first rendered before the workload manager
	// existed: render them again with the public ports' entrypoints before
	// any agent connects, or Traefik would restart without them.
	sysMgr.SetConfig(sysCfg(domains.Endpoints()))
	// A public port added or removed: Traefik replicas get new entrypoints
	// (they restart) and the host firewalls follow.
	workloads.OnPublicPorts = func() {
		sysMgr.SetConfig(sysCfg(domains.Endpoints()))
		edges.Refresh(ctx)
		meshMgr.Changed(context.Background())
	}
	traefikProvider := &traefik.Provider{
		Token: traefikToken, TokenHeader: system.TraefikTokenHeader, ControllerURL: controllerURL,
		RegistryURL: "http://" + system.RegistryAddr, BaseDomain: domains.Base, HTTPSPort: httpsPort,
		ServiceRoutes: func() []traefik.ServiceRoute {
			var out []traefik.ServiceRoute
			for _, r := range workloads.Routes(context.Background(), domains.Base()) {
				chain, own := traefikExtras.Chain(r.ServiceID)
				out = append(out, traefik.ServiceRoute{Name: r.Name, Host: r.Host, Servers: r.Servers, Middlewares: chain, OwnRetry: own, HealthPath: r.HealthPath,
					Path: r.Path, StripPrefix: r.StripPrefix, RedirectTo: r.RedirectTo})
			}
			return out
		},
		Middlewares:       traefikExtras.Definitions,
		Settings:          traefikExtras.Settings,
		GitHost:           gitServer.Host,
		GitServerURL:      "http://" + system.GitServerAddr,
		MeshControllerURL: meshControllerURL,
		Custom:            traefikExtras.Custom,
		TCPRoutes: func() []traefik.TCPRoute {
			var out []traefik.TCPRoute
			for _, r := range dbMgr.PublicRoutes(context.Background(), domains.Base()) {
				out = append(out, traefik.TCPRoute{Name: r.Name, Entrypoint: r.Entrypoint, Host: r.Host, Servers: r.Servers, Allow: r.Allow})
			}
			for _, r := range workloads.PublicRoutes(context.Background()) {
				if r.Protocol == "tcp" {
					out = append(out, traefik.TCPRoute{Name: r.Name, Entrypoint: r.Entrypoint(), Servers: r.Servers, Allow: r.Allow, Plain: true})
				}
			}
			return out
		},
		UDPRoutes: func() []traefik.UDPRoute {
			var out []traefik.UDPRoute
			for _, r := range workloads.PublicRoutes(context.Background()) {
				if r.Protocol == "udp" {
					out = append(out, traefik.UDPRoute{Name: r.Name, Entrypoint: r.Entrypoint(), Servers: r.Servers})
				}
			}
			return out
		},
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
		DownloadsDir: cfg.DownloadsDir, Workloads: workloads, Databases: dbMgr, Logs: logStore, Exec: execs, Jobs: jobMgr, Health: healthMon,
		RegistryBrowser:  regBrowser,
		RegistryMaint:    regMaint,
		Upstreams:        upstreams,
		RegistryHosts:    func() []string { return []string{registryHost(), domains.Endpoints().RegistryHost} },
		Builds:           buildMgr,
		GitConnections:   gitConns,
		GitServer:        gitServer,
		Metrics:          metricStore,
		Autoscaler:       autoscaler,
		Alerts:           alertMgr,
		SecurityGroups:   cfg.SecurityGroups,
		OnSecurityChange: disco.Kick,
		FirewallStats:    fwStats,
		Quotas:           quotas,
		Shell:            shells,
		Pools:            pools,
		Edges:            edges,
		Upgrades:         upgrades,
		S3:               s3Mgr,
		AgentRollout:     agentRollout,
		Discovery:        disco,
		Traefik:          traefikProvider,
		TraefikExtras:    traefikExtras,
		OnTraefikSettings: func() {
			sysMgr.SetConfig(sysCfg(domains.Endpoints()))
			edges.Refresh(ctx)
		},
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

	go publishControllerStats(ctx, bus, metricStore)
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

// publishControllerStats streams the controller's own stats every 2s and
// stores them every 10s for the Overview's history.
func publishControllerStats(ctx context.Context, bus *events.Bus, ms *metrics.Store) {
	start := time.Now()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var m runtime.MemStats
	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runtime.ReadMemStats(&m)
			if n%5 == 0 {
				_ = ms.RecordController(ctx, m.HeapAlloc, runtime.NumGoroutine())
			}
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

// initBaseDomain stores the first base domain: --base-domain, or
// <address>.sslip.io (§5.0.2) — the public IP, or in dev mode the address
// the machine is reached at. An existing domain is never replaced, except
// that an sslip.io/nip.io one follows a changed address.
func initBaseDomain(ctx context.Context, cfg config.Controller, domains *domain.Service, det *domain.Detector, log *slog.Logger) error {
	address := func() (string, error) { // in dev mode, the local address (see the detector)
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return det.PublicIP(dctx)
	}
	if cur := domains.Base(); cur != "" {
		// After a restore on a new host, an sslip.io/nip.io domain still names the old IP.
		if svc, ok := domain.WildcardService(cur); ok {
			ip, err := address()
			if want := domain.Wildcard(ip, svc); err == nil && want != cur {
				log.Warn("public IP changed: moving the base domain", "from", cur, "to", want)
				_, err = domains.Set(ctx, want)
				return err
			}
		}
		return nil
	}
	base := cfg.BaseDomain
	if base == "off" { // no base domain: services on <name>.localhost over HTTP (tests)
		return nil
	}
	if base == "" {
		ip, err := address()
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

// devAddress is where a development or test controller is reached: the
// agent advertise address when it is an IP, else this host's address on its
// default route, else loopback. sslip.io resolves private addresses too, so
// <address>.sslip.io names work from the same network.
func devAddress(cfg config.Controller) string {
	if host, _, err := net.SplitHostPort(cfg.AgentAdvertise); err == nil {
		if a, err := netip.ParseAddr(host); err == nil && a.Is4() && !a.IsUnspecified() && !a.IsLoopback() {
			return a.String()
		}
	}
	// A UDP "connection" sends nothing; it only picks the outgoing interface.
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() != nil && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	return "127.0.0.1"
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

// postgresImages is the release's PostgreSQL image per major version, with
// overrides from --postgres-image ("18=img,17=img").
func postgresImages(override string) (map[string]string, error) {
	images := maps.Clone(system.PostgresImages)
	for _, kv := range strings.Split(override, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		v, img, ok := strings.Cut(kv, "=")
		if _, known := images[v]; !ok || !known || img == "" {
			return nil, fmt.Errorf("--postgres-image: %q is not VERSION=IMAGE for a version of %s", kv, strings.Join(slices.Sorted(maps.Keys(images)), ", "))
		}
		images[v] = img
	}
	return images, nil
}

// mergeEntrypoints combines Traefik entrypoint maps.
func mergeEntrypoints(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// portRange parses "LOW-HIGH".
func portRange(s string) (int, int, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(a)
	hi, err2 := strconv.Atoi(b)
	return lo, hi, err1 == nil && err2 == nil && lo >= 1024 && hi <= 65535 && lo <= hi
}
