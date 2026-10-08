// Package config loads controller settings from flags and SYNCLOUD_* environment variables.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ridoysheikh/syncloud/internal/version"
)

type Controller struct {
	// DataDir holds the SQLite database and all controller state.
	DataDir string
	// Listen is the address the API and dashboard listen on.
	Listen string
	// AgentListen is where the agent gateway (gRPC, mTLS) listens.
	AgentListen string
	// AgentAdvertise is the gateway address given to joining nodes. In production
	// this is the controller's WireGuard IP (10.90.0.1:7443, §8).
	AgentAdvertise string
	// PublicHTTP and PublicHTTPS are Traefik's public entrypoints
	// (":80"/":443"; dev serves HTTP on 127.0.0.1:8080, HTTPS still on :443).
	PublicHTTP  string
	PublicHTTPS string
	// PublicValkey is Traefik's entrypoint for public Valkey databases
	// (TLS, routed by SNI; Phase 12e).
	PublicValkey string
	// PublicPostgres is Traefik's entrypoint for public PostgreSQL databases
	// (STARTTLS, routed by SNI; Phase 13).
	PublicPostgres string
	// PublicPorts is the range public TCP/UDP service ports are assigned
	// from ("20000-20999"; Phase 15c).
	PublicPorts string
	// PublicDBPorts is the range dedicated public database ports are
	// assigned from (LOW-HIGH), apart from PublicPorts.
	PublicDBPorts string
	// PostgresImage overrides PostgreSQL images per major version
	// ("18=img,17=img").
	PostgresImage string
	// TraefikAdmin serves Traefik's ping and metrics on loopback.
	TraefikAdmin string
	// SystemTasks runs the platform components on ctl-0 (D20). Off in tests.
	SystemTasks bool
	// BaseDomain is set on first start when no base domain is stored (§5.0.2).
	// Without it the controller uses <public-ip>.sslip.io, or in dev mode
	// <its address>.sslip.io (the agent advertise IP or the default-route address).
	BaseDomain string
	// PublicIP overrides public IP detection.
	PublicIP string
	// ACME issues real certificates (default on outside dev mode).
	ACME          bool
	ACMEDirectory string
	ACMEEmail     string
	// ACMECAFile is an extra CA bundle trusted for the ACME server (e.g. Pebble in tests).
	ACMECAFile string
	// ControllerSchedulable lets the controller node run services (D3).
	ControllerSchedulable bool
	// RegistryPullHost overrides the registry host nodes pull "@registry/…"
	// images from (default: registry.<base-domain>).
	RegistryPullHost string
	// RegistryInsecure makes builds push over plain HTTP (default: dev only).
	RegistryInsecure bool
	// BuildNode pins Git builds to one node by name (default: any node).
	BuildNode string
	// VictoriaLogsURL is where container logs are stored (the system task).
	VictoriaLogsURL string
	// VictoriaMetricsURL stores metrics such as uptime checks (the system task).
	VictoriaMetricsURL string
	// Firewall manages each node's host firewall (§8.3).
	Firewall bool
	// SecurityGroups isolates containers with security groups (§8.3).
	SecurityGroups bool
	// CentralProbes checks every task from the controller (§5.6).
	CentralProbes bool
	// DownloadsDir holds agent and CLI binaries served to joining nodes.
	DownloadsDir string
	// ShellImage and ShellSynctl configure Cloud Shell (§7.1).
	ShellImage  string
	ShellSynctl string
	// ReleaseURL and ReleaseChannel are where upgrades come from (§5.0.1);
	// UpgradeSettle is how long a new controller must stay healthy.
	ReleaseURL     string
	ReleaseChannel string
	UpgradeSettle  time.Duration
	// Dev enables development behavior (verbose logs, relaxed origin checks for the Vite dev server).
	Dev bool
}

func (c Controller) DBPath() string { return filepath.Join(c.DataDir, "syncloud.db") }

// LoadController parses args (without the program name). Environment variables
// provide defaults; flags override them.
func LoadController(args []string) (Controller, error) {
	fs := flag.NewFlagSet("syncloud-controller", flag.ContinueOnError)
	c := Controller{}
	fs.StringVar(&c.DataDir, "data-dir", env("SYNCLOUD_DATA_DIR", "/var/lib/syncloud"), "directory for controller state")
	fs.StringVar(&c.Listen, "listen", env("SYNCLOUD_LISTEN", "127.0.0.1:7070"), "API and dashboard listen address")
	fs.StringVar(&c.AgentListen, "agent-listen", env("SYNCLOUD_AGENT_LISTEN", "127.0.0.1:7443"), "agent gateway listen address")
	fs.StringVar(&c.AgentAdvertise, "agent-advertise", env("SYNCLOUD_AGENT_ADVERTISE", ""), "agent gateway address for nodes (default: --agent-listen)")
	fs.BoolVar(&c.Dev, "dev", env("SYNCLOUD_DEV", "") == "1", "development mode")
	fs.StringVar(&c.PublicHTTP, "public-http", env("SYNCLOUD_PUBLIC_HTTP", ""), "Traefik HTTP entrypoint (default :80, dev 127.0.0.1:8080)")
	fs.StringVar(&c.PublicHTTPS, "public-https", env("SYNCLOUD_PUBLIC_HTTPS", ""), "Traefik HTTPS entrypoint (default :443)")
	fs.StringVar(&c.PublicValkey, "public-valkey", env("SYNCLOUD_PUBLIC_VALKEY", ""), "Traefik entrypoint of public Valkey databases (default :6379, dev 127.0.0.1:16379)")
	fs.StringVar(&c.PublicPostgres, "public-postgres", env("SYNCLOUD_PUBLIC_POSTGRES", ""), "Traefik entrypoint of public PostgreSQL databases (default :5432, dev 127.0.0.1:15432)")
	fs.StringVar(&c.PublicPorts, "public-ports", env("SYNCLOUD_PUBLIC_PORTS", "20000-20999"), "the range public TCP/UDP service ports are assigned from (LOW-HIGH)")
	fs.StringVar(&c.PublicDBPorts, "public-db-ports", env("SYNCLOUD_PUBLIC_DB_PORTS", "21000-21999"), "the range public database ports are assigned from (LOW-HIGH; apart from --public-ports)")
	fs.StringVar(&c.PostgresImage, "postgres-image", env("SYNCLOUD_POSTGRES_IMAGE", ""), "PostgreSQL images per major version, as VERSION=IMAGE[,VERSION=IMAGE] (default: the release's pinned images)")
	fs.StringVar(&c.TraefikAdmin, "traefik-admin", env("SYNCLOUD_TRAEFIK_ADMIN", "127.0.0.1:8082"), "Traefik ping/metrics address (loopback)")
	fs.BoolVar(&c.SystemTasks, "system-tasks", env("SYNCLOUD_SYSTEM_TASKS", "1") == "1", "run platform components (Traefik, metrics, logs) on the local node")
	fs.StringVar(&c.BaseDomain, "base-domain", env("SYNCLOUD_BASE_DOMAIN", ""), "initial base domain (default <public-ip>.sslip.io; in dev mode <local address>.sslip.io; off = none)")
	fs.StringVar(&c.PublicIP, "public-ip", env("SYNCLOUD_PUBLIC_IP", ""), "public IPv4 address (default: detect)")
	acme := fs.String("acme", env("SYNCLOUD_ACME", ""), "issue certificates with ACME: 1 or 0 (default 1, dev 0)")
	fs.StringVar(&c.ACMEDirectory, "acme-directory", env("SYNCLOUD_ACME_DIRECTORY", "https://acme-v02.api.letsencrypt.org/directory"), "ACME directory URL")
	fs.StringVar(&c.ACMEEmail, "acme-email", env("SYNCLOUD_ACME_EMAIL", ""), "ACME account contact email (optional)")
	fs.StringVar(&c.ACMECAFile, "acme-ca-file", env("SYNCLOUD_ACME_CA_FILE", ""), "extra CA bundle to trust for the ACME server")
	ctlSched := fs.String("controller-schedulable", env("SYNCLOUD_CONTROLLER_SCHEDULABLE", ""), "run services on the controller node: 1 or 0 (default 0, dev 1)")
	fs.StringVar(&c.RegistryPullHost, "registry-pull-host", env("SYNCLOUD_REGISTRY_PULL_HOST", ""), "registry host nodes pull @registry images from (default registry.<base-domain>)")
	regInsecure := fs.String("registry-insecure", env("SYNCLOUD_REGISTRY_INSECURE", ""), "builds push to the registry over HTTP: 1 or 0 (default 0, dev 1)")
	fs.StringVar(&c.BuildNode, "build-node", env("SYNCLOUD_BUILD_NODE", ""), "node name to run Git builds on (default: any node)")
	fs.StringVar(&c.VictoriaMetricsURL, "victoriametrics-url", env("SYNCLOUD_VICTORIAMETRICS_URL", "http://127.0.0.1:8428"), "VictoriaMetrics for metrics")
	fs.StringVar(&c.VictoriaLogsURL, "victorialogs-url", env("SYNCLOUD_VICTORIALOGS_URL", "http://127.0.0.1:9428"), "VictoriaLogs for container logs")
	fs.BoolVar(&c.Firewall, "firewall", env("SYNCLOUD_FIREWALL", "1") == "1", "manage the host firewall on every node (default on)")
	fs.BoolVar(&c.CentralProbes, "central-probes", env("SYNCLOUD_CENTRAL_PROBES", "1") == "1", "probe every task from the controller and route around unreachable ones (default on)")
	fs.BoolVar(&c.SecurityGroups, "security-groups", env("SYNCLOUD_SECURITY_GROUPS", "1") == "1", "isolate containers with security groups (default on)")
	fs.StringVar(&c.ShellImage, "shell-image", env("SYNCLOUD_SHELL_IMAGE", "alpine:3.22"), "Cloud Shell container image")
	fs.StringVar(&c.ShellSynctl, "shell-synctl", env("SYNCLOUD_SHELL_SYNCTL", ""), "synctl binary mounted into Cloud Shell (default: the one in --downloads-dir)")
	fs.StringVar(&c.ReleaseURL, "release-url", env("SYNCLOUD_RELEASE_URL", version.Repository), "where upgrades are downloaded from (https://, http:// or file://)")
	fs.StringVar(&c.ReleaseChannel, "release-channel", env("SYNCLOUD_RELEASE_CHANNEL", "stable"), "release channel: stable or beta")
	fs.DurationVar(&c.UpgradeSettle, "upgrade-settle", 2*time.Minute, "how long an upgraded controller must stay healthy before the upgrade counts")
	fs.StringVar(&c.DownloadsDir, "downloads-dir", env("SYNCLOUD_DOWNLOADS_DIR", "/usr/local/lib/syncloud/downloads"), "agent/CLI binaries served at /downloads/")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.PublicHTTP == "" {
		c.PublicHTTP = map[bool]string{true: "127.0.0.1:8080", false: ":80"}[c.Dev]
	}
	if c.PublicHTTPS == "" {
		// Dev mode too: the dashboard is https://<base domain>, without a
		// port. (Plain HTTP only redirects there; dev keeps it off port 80,
		// which development machines often use already.)
		c.PublicHTTPS = ":443"
	}
	if c.PublicPostgres == "" {
		c.PublicPostgres = map[bool]string{true: "127.0.0.1:15432", false: ":5432"}[c.Dev]
	}
	if c.PublicValkey == "" {
		c.PublicValkey = map[bool]string{true: "127.0.0.1:16379", false: ":6379"}[c.Dev]
	}
	c.ControllerSchedulable = *ctlSched == "1" || (*ctlSched == "" && c.Dev)
	c.RegistryInsecure = *regInsecure == "1" || (*regInsecure == "" && c.Dev)
	switch *acme {
	case "":
		c.ACME = !c.Dev
	case "1", "true":
		c.ACME = true
	case "0", "false":
	default:
		return c, fmt.Errorf("--acme must be 1 or 0")
	}
	if c.AgentAdvertise == "" {
		c.AgentAdvertise = c.AgentListen
	}
	if c.DataDir == "" {
		return c, fmt.Errorf("data-dir must not be empty")
	}
	// Absolute, because files under it are bind-mounted into system containers.
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	c.DataDir = abs
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
