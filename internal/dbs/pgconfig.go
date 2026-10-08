package dbs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// PostgreSQL configuration (§13c2): optional add-on extensions, curated
// parameters and replication settings. They are Patroni's dynamic
// configuration, not the containers': the controller keeps them in sync
// through PATCH /config and restarts members that have settings pending,
// one at a time, so changing them never recreates members.

// ── add-ons ─────────────────────────────────────────────────────────────────

// PgAddon is an optional extension shipped in the image. Enabled add-ons
// can be installed in databases; those with a library are preloaded.
type PgAddon struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Preload     string   `json:"preload,omitempty"`    // shared_preload_libraries entry
	Extensions  []string `json:"extensions,omitempty"` // CREATE EXTENSION names it unlocks
}

// PgAddons are the add-ons a cluster can enable.
var PgAddons = []PgAddon{
	{Name: "timescaledb", Title: "TimescaleDB", Description: "Time-series hypertables, compression and continuous aggregates (Apache edition).", Preload: "timescaledb", Extensions: []string{"timescaledb"}},
	{Name: "pg_duckdb", Title: "pg_duckdb", Description: "DuckDB's columnar engine for analytics queries, Parquet and S3.", Preload: "pg_duckdb", Extensions: []string{"pg_duckdb"}},
	{Name: "pg_cron", Title: "pg_cron", Description: "Cron-style jobs scheduled inside the database.", Preload: "pg_cron", Extensions: []string{"pg_cron"}},
	{Name: "vector", Title: "pgvector", Description: "Vector similarity search (embeddings) with HNSW and IVFFlat indexes.", Extensions: []string{"vector"}},
	{Name: "postgis", Title: "PostGIS", Description: "Geographic objects, spatial indexes and functions.", Extensions: []string{"postgis", "postgis_topology", "postgis_raster", "postgis_tiger_geocoder"}},
	{Name: "pg_partman", Title: "pg_partman", Description: "Time- and serial-based partition management.", Extensions: []string{"pg_partman"}},
	{Name: "hypopg", Title: "HypoPG", Description: "Hypothetical indexes for EXPLAIN.", Extensions: []string{"hypopg"}},
}

func pgAddon(name string) (PgAddon, bool) {
	for _, a := range PgAddons {
		if a.Name == name {
			return a, true
		}
	}
	return PgAddon{}, false
}

// addonOf is the add-on that unlocks an extension ("" for PostgreSQL's own).
func addonOf(ext string) string {
	for _, a := range PgAddons {
		if slices.Contains(a.Extensions, ext) {
			return a.Name
		}
	}
	return ""
}

// enabledAddons are a spec's add-ons; nil (clusters from before §13c2)
// means every one, so nothing changes under them.
func enabledAddons(p PostgresSpec) []string {
	if p.Extensions == nil {
		out := make([]string, 0, len(PgAddons))
		for _, a := range PgAddons {
			out = append(out, a.Name)
		}
		return out
	}
	return p.Extensions
}

// preloadLibraries are the libraries a spec loads: PostgreSQL's own
// statement statistics, then the enabled add-ons'.
func preloadLibraries(p PostgresSpec) []string {
	libs := []string{"pg_stat_statements"}
	for _, name := range enabledAddons(p) {
		if a, ok := pgAddon(name); ok && a.Preload != "" {
			libs = append(libs, a.Preload)
		}
	}
	return libs
}

// ── replication ─────────────────────────────────────────────────────────────

// PgReplicationSpec is how members replicate and fail over.
type PgReplicationSpec struct {
	// Mode: async; sync (commits wait for a replica, async while none is
	// left); strict (writes stop rather than run without one).
	Mode string `json:"mode"`
	// SyncReplicas is how many replicas confirm each commit (sync modes).
	SyncReplicas int `json:"syncReplicas"`
	// MaxLagOnFailover (MiB): a replica further behind is not promoted.
	MaxLagOnFailover int `json:"maxLagOnFailover"`
	// FailoverTTL (s) is the leader lease: a leader silent this long is
	// replaced.
	FailoverTTL int `json:"failoverTtl"`
	// Slots keep the WAL a replica still needs.
	Slots bool `json:"slots"`
	// HotStandbyFeedback stops the primary from vacuuming rows that
	// replica queries still read.
	HotStandbyFeedback bool `json:"hotStandbyFeedback"`
	// WalKeepSize (MiB) of WAL kept for replicas without slots.
	WalKeepSize int `json:"walKeepSize"`
	// MaxSlotWalKeepSize (MiB) caps the WAL a slot holds (0 = unlimited).
	MaxSlotWalKeepSize int `json:"maxSlotWalKeepSize"`
}

const (
	ReplAsync  = "async"
	ReplSync   = "sync"
	ReplStrict = "strict"
)

func defaultReplication() *PgReplicationSpec {
	return &PgReplicationSpec{Mode: ReplAsync, SyncReplicas: 1, MaxLagOnFailover: 1, FailoverTTL: 30, Slots: true, WalKeepSize: 256}
}

func (r *PgReplicationSpec) normalize(replicas Range) error {
	if r.Mode == "" {
		r.Mode = ReplAsync
	}
	if !slices.Contains([]string{ReplAsync, ReplSync, ReplStrict}, r.Mode) {
		return errors.New("postgres.replication.mode must be async, sync or strict")
	}
	if r.Mode != ReplAsync && replicas.Min < 1 {
		return errors.New("synchronous replication needs at least one replica (replicas.min ≥ 1)")
	}
	if r.SyncReplicas == 0 {
		r.SyncReplicas = 1
	}
	if r.SyncReplicas < 1 || r.SyncReplicas > max(1, replicas.Max) {
		return fmt.Errorf("postgres.replication.syncReplicas must be between 1 and the replicas (%d)", max(1, replicas.Max))
	}
	if r.MaxLagOnFailover == 0 {
		r.MaxLagOnFailover = 1
	}
	if r.MaxLagOnFailover < 1 || r.MaxLagOnFailover > 1<<20 {
		return errors.New("postgres.replication.maxLagOnFailover must be between 1 and 1048576 MiB")
	}
	if r.FailoverTTL == 0 {
		r.FailoverTTL = 30
	}
	if r.FailoverTTL < 15 || r.FailoverTTL > 300 {
		return errors.New("postgres.replication.failoverTtl must be between 15 and 300 seconds")
	}
	if r.WalKeepSize < 0 || r.WalKeepSize > 1<<20 || r.MaxSlotWalKeepSize < 0 || r.MaxSlotWalKeepSize > 1<<24 {
		return errors.New("postgres.replication WAL sizes must be between 0 and 1 TiB")
	}
	return nil
}

// ── parameters ──────────────────────────────────────────────────────────────

// PgParam is a setting users may change. Values are what PostgreSQL
// accepts: memory with kB/MB/GB/TB, durations with ms/s/min/h/d.
type PgParam struct {
	Name        string   `json:"name"`
	Group       string   `json:"group"`
	Type        string   `json:"type"` // int, real, bool, enum, memory, time, string
	Min         float64  `json:"min,omitempty"`
	Max         float64  `json:"max,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Restart     bool     `json:"restart,omitempty"` // takes a restart (otherwise a reload)
	Addon       string   `json:"addon,omitempty"`   // only with this add-on enabled
	Description string   `json:"description"`
}

// Memory limits are in kB, durations in ms; -1 is "off" where PostgreSQL
// allows it.
var PgParams = []PgParam{
	{Name: "shared_buffers", Group: "Memory", Type: "memory", Min: 128, Max: 1 << 30, Restart: true, Description: "Shared page cache (default: a quarter of the memory)."},
	{Name: "effective_cache_size", Group: "Memory", Type: "memory", Min: 8, Max: 1 << 31, Description: "Planner's estimate of the OS and Postgres caches (default: three quarters of the memory)."},
	{Name: "work_mem", Group: "Memory", Type: "memory", Min: 64, Max: 1 << 31, Description: "Memory per sort or hash step before spilling to disk."},
	{Name: "maintenance_work_mem", Group: "Memory", Type: "memory", Min: 1024, Max: 1 << 31, Description: "Memory for VACUUM, CREATE INDEX and ALTER TABLE."},
	{Name: "temp_buffers", Group: "Memory", Type: "memory", Min: 800, Max: 1 << 30, Description: "Per-session buffers for temporary tables."},
	{Name: "huge_pages", Group: "Memory", Type: "enum", Enum: []string{"try", "on", "off"}, Restart: true, Description: "Use huge pages for shared memory."},
	{Name: "max_worker_processes", Group: "Parallelism", Type: "int", Min: 8, Max: 256, Restart: true, Description: "Background worker processes (parallel queries, add-ons)."},
	{Name: "max_parallel_workers", Group: "Parallelism", Type: "int", Min: 0, Max: 256, Description: "Workers available to parallel queries."},
	{Name: "max_parallel_workers_per_gather", Group: "Parallelism", Type: "int", Min: 0, Max: 64, Description: "Workers one query step may use."},
	{Name: "max_parallel_maintenance_workers", Group: "Parallelism", Type: "int", Min: 0, Max: 64, Description: "Workers one CREATE INDEX or VACUUM may use."},
	{Name: "random_page_cost", Group: "Planner", Type: "real", Min: 0, Max: 100, Description: "Cost of a random page read (1.1 suits SSDs)."},
	{Name: "seq_page_cost", Group: "Planner", Type: "real", Min: 0, Max: 100, Description: "Cost of a sequential page read."},
	{Name: "effective_io_concurrency", Group: "Planner", Type: "int", Min: 0, Max: 1000, Description: "Concurrent disk reads the storage handles well."},
	{Name: "default_statistics_target", Group: "Planner", Type: "int", Min: 1, Max: 10000, Description: "Detail of ANALYZE statistics."},
	{Name: "jit", Group: "Planner", Type: "bool", Description: "Compile expensive queries with LLVM."},
	{Name: "checkpoint_timeout", Group: "WAL", Type: "time", Min: 30_000, Max: 86_400_000, Description: "Longest time between checkpoints."},
	{Name: "checkpoint_completion_target", Group: "WAL", Type: "real", Min: 0, Max: 1, Description: "Fraction of the interval a checkpoint spreads its writes over."},
	{Name: "max_wal_size", Group: "WAL", Type: "memory", Min: 2048, Max: 1 << 31, Description: "WAL that triggers a checkpoint."},
	{Name: "min_wal_size", Group: "WAL", Type: "memory", Min: 2048, Max: 1 << 31, Description: "WAL kept for reuse."},
	{Name: "wal_compression", Group: "WAL", Type: "enum", Enum: []string{"off", "on", "pglz", "lz4", "zstd"}, Description: "Compress full-page images in the WAL."},
	{Name: "wal_buffers", Group: "WAL", Type: "memory", Min: 64, Max: 1 << 21, Restart: true, Description: "WAL in shared memory not yet written."},
	{Name: "synchronous_commit", Group: "WAL", Type: "enum", Enum: []string{"on", "remote_apply", "remote_write", "local", "off"}, Description: "How far a commit waits (with synchronous replication: for the replicas)."},
	{Name: "statement_timeout", Group: "Sessions", Type: "time", Min: 0, Max: 2_147_483_647, Description: "Cancel statements running longer (0 = never)."},
	{Name: "lock_timeout", Group: "Sessions", Type: "time", Min: 0, Max: 2_147_483_647, Description: "Give up waiting for a lock after this (0 = never)."},
	{Name: "idle_in_transaction_session_timeout", Group: "Sessions", Type: "time", Min: 0, Max: 2_147_483_647, Description: "End sessions idle inside a transaction (0 = never)."},
	{Name: "idle_session_timeout", Group: "Sessions", Type: "time", Min: 0, Max: 2_147_483_647, Description: "End idle sessions (0 = never)."},
	{Name: "default_transaction_isolation", Group: "Sessions", Type: "enum", Enum: []string{"read committed", "repeatable read", "serializable"}, Description: "Isolation level of new transactions."},
	{Name: "timezone", Group: "Sessions", Type: "string", Description: "Time zone for displaying timestamps (e.g. UTC, Europe/Berlin)."},
	{Name: "max_locks_per_transaction", Group: "Sessions", Type: "int", Min: 10, Max: 10000, Restart: true, Description: "Lock table size per transaction (many partitions need more)."},
	{Name: "max_prepared_transactions", Group: "Sessions", Type: "int", Min: 0, Max: 10000, Restart: true, Description: "Two-phase commit transactions (0 = off)."},
	{Name: "autovacuum", Group: "Autovacuum", Type: "bool", Description: "Run the autovacuum daemon."},
	{Name: "autovacuum_max_workers", Group: "Autovacuum", Type: "int", Min: 1, Max: 64, Restart: true, Description: "Concurrent autovacuum workers."},
	{Name: "autovacuum_naptime", Group: "Autovacuum", Type: "time", Min: 1000, Max: 86_400_000, Description: "Pause between autovacuum rounds."},
	{Name: "autovacuum_vacuum_scale_factor", Group: "Autovacuum", Type: "real", Min: 0, Max: 100, Description: "Fraction of a table changed before it is vacuumed."},
	{Name: "autovacuum_analyze_scale_factor", Group: "Autovacuum", Type: "real", Min: 0, Max: 100, Description: "Fraction of a table changed before it is analyzed."},
	{Name: "autovacuum_vacuum_cost_limit", Group: "Autovacuum", Type: "int", Min: -1, Max: 10000, Description: "Work per autovacuum round before it pauses (-1 = vacuum_cost_limit)."},
	{Name: "log_min_duration_statement", Group: "Logging", Type: "time", Min: -1, Max: 2_147_483_647, Description: "Log statements slower than this (-1 = off)."},
	{Name: "log_statement", Group: "Logging", Type: "enum", Enum: []string{"none", "ddl", "mod", "all"}, Description: "Which statements to log."},
	{Name: "log_connections", Group: "Logging", Type: "bool", Description: "Log each connection."},
	{Name: "log_disconnections", Group: "Logging", Type: "bool", Description: "Log each session's end and duration."},
	{Name: "log_lock_waits", Group: "Logging", Type: "bool", Description: "Log waits longer than the deadlock timeout."},
	{Name: "log_temp_files", Group: "Logging", Type: "memory", Min: -1, Max: 1 << 31, Description: "Log temporary files at least this large (-1 = off)."},
	{Name: "log_autovacuum_min_duration", Group: "Logging", Type: "time", Min: -1, Max: 2_147_483_647, Description: "Log autovacuum runs longer than this (-1 = off)."},
	{Name: "track_io_timing", Group: "Statistics", Type: "bool", Description: "Time disk reads and writes (EXPLAIN, pg_stat_statements)."},
	{Name: "track_functions", Group: "Statistics", Type: "enum", Enum: []string{"none", "pl", "all"}, Description: "Track function calls."},
	{Name: "pg_stat_statements.max", Group: "Statistics", Type: "int", Min: 100, Max: 1_000_000, Restart: true, Description: "Statements pg_stat_statements tracks."},
	{Name: "pg_stat_statements.track", Group: "Statistics", Type: "enum", Enum: []string{"top", "all", "none"}, Description: "Track top-level statements, nested ones too, or none."},
	{Name: "max_standby_streaming_delay", Group: "Replicas", Type: "time", Min: -1, Max: 2_147_483_647, Description: "How long replica queries may hold back replay (-1 = forever)."},
	{Name: "max_standby_archive_delay", Group: "Replicas", Type: "time", Min: -1, Max: 2_147_483_647, Description: "The same while replaying archived WAL."},
	{Name: "cron.max_running_jobs", Group: "Add-ons", Type: "int", Min: 1, Max: 1000, Restart: true, Addon: "pg_cron", Description: "pg_cron jobs running at once."},
	{Name: "timescaledb.max_background_workers", Group: "Add-ons", Type: "int", Min: 1, Max: 1000, Restart: true, Addon: "timescaledb", Description: "TimescaleDB background workers (compression, aggregates)."},
	{Name: "duckdb.max_memory", Group: "Add-ons", Type: "memory", Min: 1024, Max: 1 << 31, Addon: "pg_duckdb", Description: "Memory DuckDB may use per query."},
	{Name: "duckdb.threads", Group: "Add-ons", Type: "int", Min: -1, Max: 256, Addon: "pg_duckdb", Description: "Threads DuckDB uses per query (-1 = one per core)."},
}

// pgPlatformParams are owned by the platform; users cannot set them.
var pgPlatformParams = []string{
	"wal_level", "hot_standby", "max_wal_senders", "max_replication_slots", "wal_log_hints", "archive_mode", "archive_command",
	"archive_timeout", "restore_command", "shared_preload_libraries", "listen_addresses", "port", "unix_socket_directories",
	"data_directory", "hba_file", "ident_file", "ssl", "password_encryption", "cron.database_name", "max_connections",
	"wal_keep_size", "max_slot_wal_keep_size", "hot_standby_feedback", "synchronous_standby_names",
}

func pgParam(name string) (PgParam, bool) {
	for _, p := range PgParams {
		if p.Name == name {
			return p, true
		}
	}
	return PgParam{}, false
}

var (
	memRe  = regexp.MustCompile(`^(-?\d+)\s*(kB|MB|GB|TB)?$`)
	timeRe = regexp.MustCompile(`^(-?\d+)\s*(us|ms|s|min|h|d)?$`)
	strRe  = regexp.MustCompile(`^[A-Za-z0-9_/+\-:.]{1,64}$`)
)

// checkParamValue validates v for p; it returns the canonical form.
func checkParamValue(p PgParam, v string) (string, error) {
	v = strings.TrimSpace(v)
	bad := func(want string) (string, error) {
		return "", fmt.Errorf("parameter %s: %q is not %s", p.Name, v, want)
	}
	inRange := func(x float64, unit string) (string, error) {
		if x < p.Min || x > p.Max {
			return "", fmt.Errorf("parameter %s must be between %s and %s%s", p.Name, fmtNum(p.Min), fmtNum(p.Max), unit)
		}
		return v, nil
	}
	switch p.Type {
	case "int":
		x, err := strconv.Atoi(v)
		if err != nil {
			return bad("an integer")
		}
		return inRange(float64(x), "")
	case "real":
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return bad("a number")
		}
		return inRange(x, "")
	case "bool":
		switch strings.ToLower(v) {
		case "on", "true", "yes", "1":
			return "on", nil
		case "off", "false", "no", "0":
			return "off", nil
		}
		return bad("on or off")
	case "enum":
		if !slices.Contains(p.Enum, v) {
			return bad("one of " + strings.Join(p.Enum, ", "))
		}
		return v, nil
	case "memory":
		mm := memRe.FindStringSubmatch(v)
		if mm == nil {
			return bad("a size such as 64MB")
		}
		x, _ := strconv.ParseFloat(mm[1], 64)
		mult := map[string]float64{"": 1, "kB": 1, "MB": 1 << 10, "GB": 1 << 20, "TB": 1 << 30}[mm[2]]
		if x == -1 && mm[2] == "" {
			mult = 1
		}
		if _, err := inRange(x*mult, " kB"); err != nil {
			return "", err
		}
		return mm[1] + mm[2], nil
	case "time":
		mm := timeRe.FindStringSubmatch(v)
		if mm == nil {
			return bad("a duration such as 30s")
		}
		x, _ := strconv.ParseFloat(mm[1], 64)
		mult := map[string]float64{"": 1, "us": 0.001, "ms": 1, "s": 1000, "min": 60_000, "h": 3_600_000, "d": 86_400_000}[mm[2]]
		if _, err := inRange(x*mult, " ms"); err != nil {
			return "", err
		}
		return mm[1] + mm[2], nil
	case "string":
		if !strRe.MatchString(v) {
			return bad("a simple value")
		}
		return v, nil
	}
	return bad("valid")
}

func fmtNum(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

// normalizeParams checks user parameters against the catalog.
func normalizeParams(ps map[string]string, addons []string) error {
	for k, v := range ps {
		if slices.Contains(pgPlatformParams, k) {
			hint := ""
			switch k {
			case "max_connections":
				hint = " (use postgres.maxConnections)"
			case "wal_keep_size", "max_slot_wal_keep_size", "hot_standby_feedback", "synchronous_standby_names":
				hint = " (use postgres.replication)"
			case "shared_preload_libraries":
				hint = " (enable add-ons with postgres.extensions)"
			}
			return fmt.Errorf("parameter %s is set by the platform%s", k, hint)
		}
		p, ok := pgParam(k)
		if !ok {
			return fmt.Errorf("parameter %s is not one that can be changed", k)
		}
		if p.Addon != "" && !slices.Contains(addons, p.Addon) {
			return fmt.Errorf("parameter %s needs the %s add-on", k, p.Addon)
		}
		c, err := checkParamValue(p, v)
		if err != nil {
			return err
		}
		ps[k] = c
	}
	return nil
}

// normalizePgConfig validates and defaults extensions, parameters and
// replication (called from normalizePostgres).
func (s *Spec) normalizePgConfig() error {
	p := s.Postgres
	if p.Extensions != nil {
		seen := map[string]bool{}
		out := []string{}
		for _, e := range p.Extensions {
			if _, ok := pgAddon(e); !ok {
				names := make([]string, 0, len(PgAddons))
				for _, a := range PgAddons {
					names = append(names, a.Name)
				}
				return fmt.Errorf("postgres.extensions: %q is not an add-on (%s)", e, strings.Join(names, ", "))
			}
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
		sort.Strings(out)
		p.Extensions = out
	}
	if len(p.Parameters) == 0 {
		p.Parameters = nil
	} else if err := normalizeParams(p.Parameters, enabledAddons(*p)); err != nil {
		return err
	}
	if p.Replication == nil {
		p.Replication = defaultReplication()
		if p.Synchronous {
			p.Replication.Mode = ReplSync
		}
	}
	if err := p.Replication.normalize(s.Replicas); err != nil {
		return err
	}
	p.Synchronous = p.Replication.Mode != ReplAsync
	return nil
}

// ── dynamic configuration ───────────────────────────────────────────────────

// pgParameters are a cluster's PostgreSQL settings: the platform's, the
// memory-derived tuning, then the user's.
func pgParameters(d store.Database, spec Spec, st State) map[string]any {
	p := spec.Postgres
	r := p.Replication
	if r == nil {
		r = defaultReplication()
	}
	params := map[string]any{
		"wal_level": "replica", "hot_standby": "on", "max_wal_senders": 10, "max_replication_slots": 10,
		"wal_keep_size": fmt.Sprintf("%dMB", r.WalKeepSize), "wal_log_hints": "on", "archive_mode": "on", "archive_command": "/bin/true",
		"shared_preload_libraries": strings.Join(preloadLibraries(*p), ","),
		"password_encryption":      "scram-sha-256",
		"hot_standby_feedback":     pgBool(r.HotStandbyFeedback),
		"max_slot_wal_keep_size":   "-1",
	}
	if r.MaxSlotWalKeepSize > 0 {
		params["max_slot_wal_keep_size"] = fmt.Sprintf("%dMB", r.MaxSlotWalKeepSize)
	}
	addons := enabledAddons(*p)
	if slices.Contains(addons, "pg_cron") {
		params["cron.database_name"] = PgDatabase(d)
	}
	if slices.Contains(addons, "timescaledb") {
		params["timescaledb.telemetry_level"] = "off"
	}
	for k, v := range pgTuning(st.MemoryMiB, p.MaxConnections) {
		params[k] = v
	}
	for k, v := range p.Parameters {
		params[k] = v
	}
	return params
}

// pgDynamicConfig is Patroni's dynamic (DCS) configuration for a cluster.
func pgDynamicConfig(d store.Database, spec Spec, st State) map[string]any {
	r := spec.Postgres.Replication
	if r == nil {
		r = defaultReplication()
	}
	// Patroni requires ttl ≥ loop_wait + 2 × retry_timeout.
	loop := max(3, r.FailoverTTL/3)
	return map[string]any{
		"ttl": r.FailoverTTL, "loop_wait": loop, "retry_timeout": loop,
		"maximum_lag_on_failover": r.MaxLagOnFailover << 20,
		"synchronous_mode":        r.Mode != ReplAsync,
		"synchronous_mode_strict": r.Mode == ReplStrict,
		"synchronous_node_count":  r.SyncReplicas,
		"postgresql": map[string]any{
			"use_pg_rewind": true, "use_slots": r.Slots, "parameters": pgParameters(d, spec, st),
		},
	}
}

func pgBool(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func configHash(cfg map[string]any) string {
	b, _ := json.Marshal(cfg) // maps marshal with sorted keys
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// patroniRequest calls a member's Patroni REST API with the admin login.
func patroniRequest(ctx context.Context, method, ip, path string, sec Secrets, body any, out any, timeout time.Duration) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://%s%s", addr(ip, patroniPort), path), rd)
	req.SetBasicAuth(adminUser, sec.RestPassword)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("patroni %s %s: %s %s", method, path, res.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.Unmarshal(msg, out)
	}
	return nil
}

// syncPgConfig brings the cluster's dynamic configuration to the spec:
// parameters the spec no longer sets are removed (sent as null).
func (m *Manager) syncPgConfig(ctx context.Context, d store.Database, spec Spec, st *State, sec Secrets, leaderIP string) error {
	want := pgDynamicConfig(d, spec, *st)
	h := configHash(want)
	if st.DCSHash == h {
		return nil
	}
	var cur map[string]any
	if err := patroniRequest(ctx, "GET", leaderIP, "/config", sec, nil, &cur, 5*time.Second); err != nil {
		return err
	}
	patch := maps.Clone(want)
	wantPG := want["postgresql"].(map[string]any)
	wantParams := wantPG["parameters"].(map[string]any)
	pg := maps.Clone(wantPG)
	params := maps.Clone(wantParams)
	if c, ok := cur["postgresql"].(map[string]any); ok {
		if cp, ok := c["parameters"].(map[string]any); ok {
			for k := range cp {
				if _, keep := wantParams[k]; !keep {
					params[k] = nil
				}
			}
		}
	}
	pg["parameters"] = params
	patch["postgresql"] = pg
	if err := patroniRequest(ctx, "PATCH", leaderIP, "/config", sec, patch, nil, 10*time.Second); err != nil {
		return err
	}
	first := st.DCSHash == ""
	st.DCSHash = h
	if err := m.st.SetDatabaseState(ctx, d.ID, encode(*st)); err != nil {
		return err
	}
	if !first {
		m.event(ctx, d.ID, "configured", "", configSummary(spec), "applied to the cluster's dynamic configuration", "operator")
	}
	return nil
}

// configSummary describes the settings for events.
func configSummary(spec Spec) string {
	p := spec.Postgres
	r := p.Replication
	if r == nil {
		r = defaultReplication()
	}
	ext := "none"
	if a := enabledAddons(*p); len(a) > 0 {
		ext = strings.Join(a, ", ")
	}
	return fmt.Sprintf("replication %s, add-ons %s, %d parameter(s)", r.Mode, ext, len(p.Parameters))
}

// restartPending restarts, one at a time, the members whose settings need
// a restart: replicas first, the leader once every replica streams.
func (m *Manager) restartPending(d store.Database, sec Secrets, members []store.DatabaseMember, status map[string]patroniStatus, leaderOrd int) {
	m.mu.Lock()
	if m.pgRestarting == nil {
		m.pgRestarting = map[string]bool{}
	}
	busy := m.pgRestarting[d.ID]
	m.mu.Unlock()
	if busy {
		return
	}
	var pick *store.DatabaseMember
	replicasOK := true
	for i := range members {
		mb := &members[i]
		s, ok := status[mb.ID]
		if !ok {
			if mb.Kind == KindData {
				replicasOK = false
			}
			continue
		}
		if mb.Ordinal != leaderOrd {
			if s.State != "running" || s.Xlog.ReceivedLocation == 0 {
				replicasOK = false
			}
			if s.Pending && s.State == "running" && pick == nil {
				pick = mb
			}
		}
	}
	if pick == nil && replicasOK {
		for i := range members {
			if s, ok := status[members[i].ID]; ok && members[i].Ordinal == leaderOrd && s.Pending && s.State == "running" {
				pick = &members[i]
			}
		}
	}
	if pick == nil {
		return
	}
	mb := *pick
	m.mu.Lock()
	m.pgRestarting[d.ID] = true
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.pgRestarting, d.ID)
			m.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		role := "replica"
		if mb.Ordinal == leaderOrd {
			role = "leader"
		}
		err := patroniRequest(ctx, "POST", mb.IP, "/restart", sec, map[string]any{"restart_pending": true}, nil, 110*time.Second)
		if err != nil {
			m.log.Warn("restart a member for pending settings", "database", d.Name, "member", memberName(mb), "err", err)
			return
		}
		m.event(ctx, d.ID, "restarted", "", memberName(mb), "the "+role+" restarted to apply settings that need a restart", "operator")
		// Let it catch up before the next one.
		time.Sleep(10 * time.Second)
	}()
}

// ── live settings and replication status ────────────────────────────────────

// PgSetting is a parameter's catalog entry and its live value.
type PgSetting struct {
	PgParam
	Value      string `json:"value"`           // as the server reports it, with its unit
	Source     string `json:"source"`          // default, configuration file, …
	Configured string `json:"configured"`      // the spec's value ("" = the default)
	Pending    bool   `json:"pendingRestart"`  // changed, waiting for a restart
	Available  bool   `json:"available"`       // its add-on is enabled
	Default    string `json:"platformDefault"` // the platform's value, if it sets one
}

// PgSettings lists the catalog with the leader's values.
func (m *Manager) PgSettings(ctx context.Context, d store.Database) ([]PgSetting, error) {
	spec, _ := parseSpec(d.Spec)
	st := parseState(d.State)
	base := pgParameters(d, Spec{Postgres: &PostgresSpec{MaxConnections: spec.Postgres.MaxConnections, Extensions: spec.Postgres.Extensions, Replication: spec.Postgres.Replication}}, st)
	live := map[string][3]string{}
	pending := map[string]bool{}
	err := m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		names := make([]string, 0, len(PgParams))
		for _, p := range PgParams {
			names = append(names, p.Name)
		}
		rows, err := c.Query(ctx, `SELECT name, setting, coalesce(unit, ''), source, pending_restart FROM pg_settings WHERE name = ANY($1)`, names)
		if err != nil {
			return err
		}
		for rows.Next() {
			var n, v, u, src string
			var pend bool
			if err := rows.Scan(&n, &v, &u, &src, &pend); err != nil {
				return err
			}
			live[n] = [3]string{v, u, src}
			pending[n] = pend
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	addons := enabledAddons(*spec.Postgres)
	out := make([]PgSetting, 0, len(PgParams))
	for _, p := range PgParams {
		s := PgSetting{PgParam: p, Configured: spec.Postgres.Parameters[p.Name], Pending: pending[p.Name]}
		s.Available = p.Addon == "" || slices.Contains(addons, p.Addon)
		if l, ok := live[p.Name]; ok {
			s.Value, s.Source = withUnit(l[0], l[1]), l[2]
		}
		if v, ok := base[p.Name]; ok {
			s.Default = fmt.Sprint(v)
		}
		out = append(out, s)
	}
	return out, nil
}

// withUnit renders pg_settings' setting and unit ("16384", "8kB" → "128MB").
func withUnit(v, unit string) string {
	if unit == "" {
		return v
	}
	x, err := strconv.ParseFloat(v, 64)
	if err != nil || x < 0 {
		return v
	}
	mult := map[string]float64{"kB": 1, "8kB": 8, "16kB": 16, "MB": 1024}
	if k, ok := mult[unit]; ok {
		kb := x * k
		for _, u := range []struct {
			n string
			f float64
		}{{"TB", 1 << 30}, {"GB", 1 << 20}, {"MB", 1 << 10}} {
			if kb >= u.f && kb == float64(int64(kb/u.f))*u.f {
				return fmtNum(kb/u.f) + u.n
			}
		}
		return fmtNum(kb) + "kB"
	}
	return v + unit
}

// PgReplica is a replica as the primary sees it (pg_stat_replication).
type PgReplica struct {
	Name        string  `json:"name"`
	ClientAddr  string  `json:"clientAddr"`
	State       string  `json:"state"`
	SyncState   string  `json:"syncState"`
	SentLSN     string  `json:"sentLsn"`
	ReplayLSN   string  `json:"replayLsn"`
	LagBytes    int64   `json:"lagBytes"`
	WriteLagMs  float64 `json:"writeLagMs"`
	FlushLagMs  float64 `json:"flushLagMs"`
	ReplayLagMs float64 `json:"replayLagMs"`
}

// PgSlot is a replication slot.
type PgSlot struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Active    bool   `json:"active"`
	WalStatus string `json:"walStatus"`
	HeldBytes int64  `json:"heldBytes"`
}

// PgClusterMember is a member as Patroni reports it.
type PgClusterMember struct {
	Name           string `json:"name"`
	Role           string `json:"role"`
	State          string `json:"state"`
	Timeline       int    `json:"timeline"`
	LagBytes       any    `json:"lag,omitempty"` // bytes, or "unknown"
	PendingRestart bool   `json:"pendingRestart"`
}

// PgReplication is a cluster's replication status.
type PgReplication struct {
	Settings   PgReplicationSpec `json:"settings"`
	Replicas   []PgReplica       `json:"replicas"`
	Slots      []PgSlot          `json:"slots"`
	Members    []PgClusterMember `json:"members"`
	CurrentLSN string            `json:"currentLsn"`
	Timeline   int               `json:"timeline"`
}

// PgReplicationStatus reads pg_stat_replication, the slots and Patroni's
// view of the members.
func (m *Manager) PgReplicationStatus(ctx context.Context, d store.Database) (PgReplication, error) {
	spec, _ := parseSpec(d.Spec)
	out := PgReplication{Replicas: []PgReplica{}, Slots: []PgSlot{}, Members: []PgClusterMember{}}
	if spec.Postgres.Replication != nil {
		out.Settings = *spec.Postgres.Replication
	}
	err := m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		if err := c.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text, (SELECT timeline_id FROM pg_control_checkpoint())`).Scan(&out.CurrentLSN, &out.Timeline); err != nil {
			return err
		}
		rows, err := c.Query(ctx, `
SELECT coalesce(application_name, ''), coalesce(host(client_addr), ''), coalesce(state, ''), coalesce(sync_state, ''),
       coalesce(sent_lsn::text, ''), coalesce(replay_lsn::text, ''),
       coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn), 0)::bigint,
       coalesce(extract(epoch FROM write_lag) * 1000, 0), coalesce(extract(epoch FROM flush_lag) * 1000, 0),
       coalesce(extract(epoch FROM replay_lag) * 1000, 0)
FROM pg_stat_replication ORDER BY application_name`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r PgReplica
			if err := rows.Scan(&r.Name, &r.ClientAddr, &r.State, &r.SyncState, &r.SentLSN, &r.ReplayLSN, &r.LagBytes, &r.WriteLagMs, &r.FlushLagMs, &r.ReplayLagMs); err != nil {
				return err
			}
			out.Replicas = append(out.Replicas, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = c.Query(ctx, `
SELECT slot_name, slot_type, active, coalesce(wal_status, ''),
       coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0)::bigint
FROM pg_replication_slots ORDER BY slot_name`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s PgSlot
			if err := rows.Scan(&s.Name, &s.Type, &s.Active, &s.WalStatus, &s.HeldBytes); err != nil {
				return err
			}
			out.Slots = append(out.Slots, s)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	ip, err := m.pgLeader(ctx, d)
	if err != nil {
		return out, nil
	}
	sec, err := m.secrets(d)
	if err != nil {
		return out, nil
	}
	var cl struct {
		Members []struct {
			Name     string `json:"name"`
			Role     string `json:"role"`
			State    string `json:"state"`
			Timeline int    `json:"timeline"`
			Lag      any    `json:"lag"`
			Pending  bool   `json:"pending_restart"`
		} `json:"members"`
	}
	if err := patroniRequest(ctx, "GET", ip, "/cluster", sec, nil, &cl, 4*time.Second); err == nil {
		for _, x := range cl.Members {
			out.Members = append(out.Members, PgClusterMember{Name: x.Name, Role: x.Role, State: x.State, Timeline: x.Timeline, LagBytes: x.Lag, PendingRestart: x.Pending})
		}
	}
	return out, nil
}

// pgInstalledAddons lists, per add-on, the databases it is installed in.
func (m *Manager) pgInstalledAddons(ctx context.Context, d store.Database) (map[string][]string, error) {
	var dbs []string
	err := m.pgAdmin(ctx, d, "", func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname`)
		if err != nil {
			return err
		}
		dbs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, db := range dbs {
		err := m.pgAdmin(ctx, d, db, func(c *pgx.Conn) error {
			rows, err := c.Query(ctx, `SELECT extname FROM pg_extension`)
			if err != nil {
				return err
			}
			exts, err := pgx.CollectRows(rows, pgx.RowTo[string])
			for _, e := range exts {
				if a := addonOf(e); a != "" && !slices.Contains(out[a], db) {
					out[a] = append(out[a], db)
				}
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkAddonsRemoval refuses to disable an add-on still installed somewhere.
func (m *Manager) checkAddonsRemoval(ctx context.Context, d store.Database, old, spec PostgresSpec) error {
	var removed []string
	for _, a := range enabledAddons(old) {
		if !slices.Contains(enabledAddons(spec), a) {
			removed = append(removed, a)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	inst, err := m.pgInstalledAddons(ctx, d)
	if err != nil {
		return ErrInvalid{fmt.Errorf("cannot check where %s is installed (the cluster must be running): %v", strings.Join(removed, ", "), err)}
	}
	for _, a := range removed {
		if dbs := inst[a]; len(dbs) > 0 {
			return ErrInvalid{fmt.Errorf("%s is installed in %s: drop the extension there first", a, strings.Join(dbs, ", "))}
		}
	}
	return nil
}
