// Package config loads controller settings from flags and SYNCLOUD_* environment variables.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	// (":80"/":443"; dev defaults to 127.0.0.1:8080/8443).
	PublicHTTP  string
	PublicHTTPS string
	// TraefikAdmin serves Traefik's ping and metrics on loopback.
	TraefikAdmin string
	// SystemTasks runs the platform components on ctl-0 (D20). Off in tests.
	SystemTasks bool
	// BaseDomain is set on first start when no base domain is stored (§5.0.2).
	// Without it, a non-dev controller uses <public-ip>.sslip.io.
	BaseDomain string
	// PublicIP overrides public IP detection.
	PublicIP string
	// ACME issues real certificates (default on outside dev mode).
	ACME          bool
	ACMEDirectory string
	ACMEEmail     string
	// ACMECAFile is an extra CA bundle trusted for the ACME server (e.g. Pebble in tests).
	ACMECAFile string
	// Firewall manages each node's host firewall (§8.3).
	Firewall bool
	// DownloadsDir holds agent and CLI binaries served to joining nodes.
	DownloadsDir string
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
	fs.StringVar(&c.PublicHTTPS, "public-https", env("SYNCLOUD_PUBLIC_HTTPS", ""), "Traefik HTTPS entrypoint (default :443, dev 127.0.0.1:8443)")
	fs.StringVar(&c.TraefikAdmin, "traefik-admin", env("SYNCLOUD_TRAEFIK_ADMIN", "127.0.0.1:8082"), "Traefik ping/metrics address (loopback)")
	fs.BoolVar(&c.SystemTasks, "system-tasks", env("SYNCLOUD_SYSTEM_TASKS", "1") == "1", "run platform components (Traefik, metrics, logs) on the local node")
	fs.StringVar(&c.BaseDomain, "base-domain", env("SYNCLOUD_BASE_DOMAIN", ""), "initial base domain (default <public-ip>.sslip.io outside dev mode)")
	fs.StringVar(&c.PublicIP, "public-ip", env("SYNCLOUD_PUBLIC_IP", ""), "public IPv4 address (default: detect)")
	acme := fs.String("acme", env("SYNCLOUD_ACME", ""), "issue certificates with ACME: 1 or 0 (default 1, dev 0)")
	fs.StringVar(&c.ACMEDirectory, "acme-directory", env("SYNCLOUD_ACME_DIRECTORY", "https://acme-v02.api.letsencrypt.org/directory"), "ACME directory URL")
	fs.StringVar(&c.ACMEEmail, "acme-email", env("SYNCLOUD_ACME_EMAIL", ""), "ACME account contact email (optional)")
	fs.StringVar(&c.ACMECAFile, "acme-ca-file", env("SYNCLOUD_ACME_CA_FILE", ""), "extra CA bundle to trust for the ACME server")
	fs.BoolVar(&c.Firewall, "firewall", env("SYNCLOUD_FIREWALL", "1") == "1", "manage the host firewall on every node (default on)")
	fs.StringVar(&c.DownloadsDir, "downloads-dir", env("SYNCLOUD_DOWNLOADS_DIR", "/usr/local/lib/syncloud/downloads"), "agent/CLI binaries served at /downloads/")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.PublicHTTP == "" {
		c.PublicHTTP = map[bool]string{true: "127.0.0.1:8080", false: ":80"}[c.Dev]
	}
	if c.PublicHTTPS == "" {
		c.PublicHTTPS = map[bool]string{true: "127.0.0.1:8443", false: ":443"}[c.Dev]
	}
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
