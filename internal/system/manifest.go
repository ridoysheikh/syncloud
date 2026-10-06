// Package system runs the platform's own components as system tasks on the
// controller node (D20, §5.0): Traefik, VictoriaMetrics, VictoriaLogs, and
// (next) the registry and BuildKit. Versions are pinned per SynCloud release.
package system

import (
	"encoding/json"
	"fmt"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

// Release manifest: tested image versions for this SynCloud release.
const (
	ImageTraefik         = "traefik:v3.7.13"
	ImageVictoriaMetrics = "victoriametrics/victoria-metrics:v1.153.0"
	ImageVictoriaLogs    = "victoriametrics/victoria-logs:v1.53.0"
	ImageRegistry        = "registry:3.1.2"
)

// RegistryAddr is where Traefik (host network) reaches the registry.
const RegistryAddr = "127.0.0.1:5000"

// Network is the Docker bridge network system components share.
const Network = "syncloud-system"

// TaskIDPrefix marks system tasks; unknown ones found on the node are removed.
const TaskIDPrefix = "sys-"

type Config struct {
	// ControllerURL is how Traefik (host network) reaches the controller API.
	ControllerURL string
	// HTTPAddr and HTTPSAddr are Traefik's public entrypoints (":80"/":443"; high ports in dev).
	HTTPAddr  string
	HTTPSAddr string
	// AdminAddr serves Traefik's ping and Prometheus metrics (loopback only).
	AdminAddr string
	// TraefikToken authenticates Traefik to the controller's config endpoint.
	TraefikToken string
	// RegistryRealm is the token endpoint URL Docker clients are sent to.
	RegistryRealm string
	// RegistryTokenCert is the absolute path of the token issuer certificate.
	RegistryTokenCert string
	// RegistryReadOnly rejects pushes while garbage collection runs; pulls
	// keep working.
	RegistryReadOnly bool
}

// Component describes one system task for the dashboard.
type Component struct {
	TaskID      string
	Name        string
	Description string
	spec        func(Config) *agentv1.TaskSpec
}

var Components = []Component{
	{
		TaskID: "sys-traefik", Name: "Traefik", Description: "Edge proxy: routes and TLS from the controller (§5.7)",
		spec: traefikSpec,
	},
	{
		TaskID: "sys-registry", Name: "Registry", Description: "Private Docker registry with token auth (§5.9)",
		spec: registrySpec,
	},
	{
		TaskID: "sys-victoriametrics", Name: "VictoriaMetrics", Description: "Metrics store (§9.1)",
		spec: func(Config) *agentv1.TaskSpec {
			return &agentv1.TaskSpec{
				TaskId: "sys-victoriametrics", Name: "syncloud-victoriametrics", Image: ImageVictoriaMetrics,
				Command:     []string{"-storageDataPath=/storage", "-retentionPeriod=15d", "-httpListenAddr=:8428"},
				Ports:       []*agentv1.PortBinding{{HostIp: "127.0.0.1", HostPort: 8428, ContainerPort: 8428}},
				Mounts:      []*agentv1.Mount{{Type: agentv1.Mount_TYPE_VOLUME, Source: "syncloud-victoriametrics", Target: "/storage"}},
				NetworkMode: Network, System: true,
			}
		},
	},
	{
		TaskID: "sys-victorialogs", Name: "VictoriaLogs", Description: "Central log store (§9.2)",
		spec: func(Config) *agentv1.TaskSpec {
			return &agentv1.TaskSpec{
				TaskId: "sys-victorialogs", Name: "syncloud-victorialogs", Image: ImageVictoriaLogs,
				Command:     []string{"-storageDataPath=/vlogs", "-retentionPeriod=7d", "-httpListenAddr=:9428"},
				Ports:       []*agentv1.PortBinding{{HostIp: "127.0.0.1", HostPort: 9428, ContainerPort: 9428}},
				Mounts:      []*agentv1.Mount{{Type: agentv1.Mount_TYPE_VOLUME, Source: "syncloud-victorialogs", Target: "/vlogs"}},
				NetworkMode: Network, System: true,
			}
		},
	},
}

// Traefik runs in host networking so it can bind the public ports directly and
// reach the controller on loopback and containers on any local network.
func traefikSpec(c Config) *agentv1.TaskSpec {
	return &agentv1.TaskSpec{
		TaskId: "sys-traefik", Name: "syncloud-traefik", Image: ImageTraefik,
		Command: []string{
			"--global.checkNewVersion=false",
			"--global.sendAnonymousUsage=false",
			"--entrypoints.web.address=" + c.HTTPAddr,
			"--entrypoints.websecure.address=" + c.HTTPSAddr,
			"--entrypoints.websecure.http.tls=true",
			"--entrypoints.admin.address=" + c.AdminAddr,
			"--ping=true",
			"--ping.entrypoint=admin",
			"--metrics.prometheus=true",
			"--metrics.prometheus.entrypoint=admin",
			"--metrics.prometheus.addRoutersLabels=true",
			"--metrics.prometheus.addServicesLabels=true",
			"--providers.http.endpoint=" + c.ControllerURL + "/internal/traefik/config",
			"--providers.http.pollInterval=2s",
			fmt.Sprintf("--providers.http.headers.%s=%s", TraefikTokenHeader, c.TraefikToken),
			"--accesslog=true",
			"--accesslog.format=json",
			"--log.level=INFO",
		},
		NetworkMode: "host",
		System:      true,
	}
}

// registrySpec runs the registry in host networking on loopback, like
// Traefik, so its notification webhook reaches the controller's loopback API.
func registrySpec(c Config) *agentv1.TaskSpec {
	notify, _ := json.Marshal([]map[string]any{{
		"name": "syncloud", "url": c.ControllerURL + RegistryEventsPath,
		"headers": map[string][]string{TraefikTokenHeader: {c.TraefikToken}},
		"timeout": "3s", "threshold": 5, "backoff": "10s",
	}})
	spec := &agentv1.TaskSpec{
		TaskId: "sys-registry", Name: "syncloud-registry", Image: ImageRegistry,
		Env: map[string]string{
			"REGISTRY_HTTP_ADDR":                 RegistryAddr,
			"REGISTRY_HTTP_DEBUG_ADDR":           "127.0.0.1:5001",
			"REGISTRY_NOTIFICATIONS_ENDPOINTS":   string(notify),
			"REGISTRY_STORAGE_DELETE_ENABLED":    "true",
			"REGISTRY_AUTH_TOKEN_REALM":          c.RegistryRealm,
			"REGISTRY_AUTH_TOKEN_SERVICE":        "syncloud-registry",
			"REGISTRY_AUTH_TOKEN_ISSUER":         "syncloud",
			"REGISTRY_AUTH_TOKEN_ROOTCERTBUNDLE": "/etc/syncloud/registry-token.crt",
			"REGISTRY_LOG_LEVEL":                 "info",
			"OTEL_TRACES_EXPORTER":               "none",
		},
		Mounts: []*agentv1.Mount{
			{Type: agentv1.Mount_TYPE_VOLUME, Source: "syncloud-registry", Target: "/var/lib/registry"},
			{Type: agentv1.Mount_TYPE_BIND, Source: c.RegistryTokenCert, Target: "/etc/syncloud/registry-token.crt", ReadOnly: true},
		},
		NetworkMode: "host", System: true,
	}
	if c.RegistryReadOnly {
		spec.Env["REGISTRY_STORAGE_MAINTENANCE_READONLY"] = `{"enabled": true}`
	}
	return spec
}

// RegistryEventsPath receives the registry's notifications (authenticated
// with TraefikToken in TraefikTokenHeader).
const RegistryEventsPath = "/internal/registry/events"

// TraefikTokenHeader carries Config.TraefikToken on config polls.
const TraefikTokenHeader = "X-Syncloud-Token"

// Specs returns every system task spec for this release.
func Specs(c Config) []*agentv1.TaskSpec {
	out := make([]*agentv1.TaskSpec, 0, len(Components))
	for _, comp := range Components {
		out = append(out, comp.spec(c))
	}
	return out
}
