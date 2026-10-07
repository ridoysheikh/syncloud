package dbs

import "sort"

// EngineInfo describes a database engine for the API and the wizard. The
// operator core (members, placement, network, public endpoints, events) is
// shared; each engine brings its task specs, probe, autoscaling signals and
// tools.
type EngineInfo struct {
	Name           string   `json:"name"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Available      bool     `json:"available"` // false: planned
	Versions       []string `json:"versions"`
	DefaultVersion string   `json:"defaultVersion,omitempty"`
	Port           int      `json:"port"`
	// Scheme and TLSScheme build connection URLs (internal and public).
	Scheme    string `json:"scheme"`
	TLSScheme string `json:"tlsScheme"`
	// Entrypoint is the Traefik entrypoint of its public endpoints.
	Entrypoint string `json:"-"`
	// Features the dashboard and CLI offer: explorer, console, failover,
	// memoryAutoscaling, replicaAutoscaling.
	Features []string `json:"features"`
	// PostgreSQL: the optional add-ons and the parameters users may set
	// (§13c2), for the wizard.
	Addons     []PgAddon `json:"addons,omitempty"`
	Parameters []PgParam `json:"parameters,omitempty"`
}

// Engine names.
const (
	EngineValkey   = "valkey"
	EnginePostgres = "postgres"
)

// Engines lists the engines, available ones first.
var Engines = []EngineInfo{
	{
		Name: EngineValkey, Title: "Valkey", Available: true,
		Description: "Redis-compatible in-memory store (BSD). Sentinel failover, read replicas, online memory autoscaling.",
		Versions:    versions(), DefaultVersion: DefaultVersion, Port: Port, Scheme: "redis", TLSScheme: "rediss",
		Entrypoint: "valkey", Features: []string{"explorer", "console", "failover", "memoryAutoscaling", "replicaAutoscaling"},
	},
	{
		Name: EnginePostgres, Title: "PostgreSQL", Available: true,
		Description: "PostgreSQL 18 or 17 with Patroni failover, streaming replicas and WAL-G backups; optional pgvector, TimescaleDB (Apache), pg_duckdb, PostGIS, pg_partman and pg_cron.",
		Versions:    pgVersions, DefaultVersion: pgDefaultVersion, Port: PostgresPort, Scheme: "postgresql", TLSScheme: "postgresql",
		Entrypoint: "postgres", Features: []string{"failover"},
		Addons: PgAddons, Parameters: PgParams,
	},
}

// EngineByName returns an engine (ok false for unknown ones).
func EngineByName(name string) (EngineInfo, bool) {
	for _, e := range Engines {
		if e.Name == name {
			return e, true
		}
	}
	return EngineInfo{}, false
}

func versions() []string {
	var v []string
	for k := range Images {
		v = append(v, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(v)))
	return v
}
