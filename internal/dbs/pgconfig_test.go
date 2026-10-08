package dbs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/store"
)

func pgSpec(p PostgresSpec, replicas int) Spec {
	s := Spec{Replicas: Range{Min: replicas, Max: replicas}, Postgres: &p}
	return s
}

func TestPgConfigDefaults(t *testing.T) {
	s := pgSpec(PostgresSpec{Extensions: []string{}}, 0)
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	r := s.Postgres.Replication
	if r.Mode != ReplAsync || r.SyncReplicas != 1 || r.FailoverTTL != 30 || !r.Slots || r.WalKeepSize != 256 {
		t.Fatalf("replication defaults: %+v", r)
	}
	if got := preloadLibraries(*s.Postgres); strings.Join(got, ",") != "pg_stat_statements" {
		t.Fatalf("a plain cluster preloads %v", got)
	}
	// Clusters from before add-ons keep every one.
	legacy := PostgresSpec{}
	if got := preloadLibraries(legacy); strings.Join(got, ",") != "pg_stat_statements,timescaledb,pg_duckdb,pg_cron" {
		t.Fatalf("a legacy cluster preloads %v", got)
	}
}

func TestPgConfigReplication(t *testing.T) {
	s := pgSpec(PostgresSpec{Synchronous: true}, 1)
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Postgres.Replication.Mode != ReplSync {
		t.Fatalf("synchronous: true maps to %q", s.Postgres.Replication.Mode)
	}
	for _, c := range []struct {
		r        PgReplicationSpec
		replicas int
		err      string
	}{
		{PgReplicationSpec{Mode: ReplStrict}, 0, "at least one replica"},
		{PgReplicationSpec{Mode: "quorum"}, 1, "async, sync or strict"},
		{PgReplicationSpec{Mode: ReplSync, SyncReplicas: 3}, 2, "syncReplicas"},
		{PgReplicationSpec{FailoverTTL: 5}, 0, "failoverTtl"},
	} {
		r := c.r
		s := pgSpec(PostgresSpec{Replication: &r}, c.replicas)
		if err := s.Normalize(); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%+v: got %v, want %q", c.r, err, c.err)
		}
	}
	r := PgReplicationSpec{Mode: ReplStrict, SyncReplicas: 2, FailoverTTL: 45, MaxLagOnFailover: 16, MaxSlotWalKeepSize: 1024, HotStandbyFeedback: true}
	s = pgSpec(PostgresSpec{Replication: &r, Extensions: []string{}}, 2)
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	d := store.Database{Name: "orders", Version: "18"}
	cfg := pgDynamicConfig(d, s, State{MemoryMiB: 1024})
	if cfg["ttl"] != 45 || cfg["loop_wait"] != 15 || cfg["retry_timeout"] != 15 {
		t.Fatalf("timing: %v %v %v", cfg["ttl"], cfg["loop_wait"], cfg["retry_timeout"])
	}
	if cfg["synchronous_mode"] != true || cfg["synchronous_mode_strict"] != true || cfg["synchronous_node_count"] != 2 || cfg["maximum_lag_on_failover"] != 16<<20 {
		t.Fatalf("sync settings: %v", cfg)
	}
	params := cfg["postgresql"].(map[string]any)["parameters"].(map[string]any)
	if params["max_slot_wal_keep_size"] != "1024MB" || params["hot_standby_feedback"] != "on" {
		t.Fatalf("replication parameters: %v", params)
	}
}

func TestPgConfigParameters(t *testing.T) {
	ok := map[string]string{"work_mem": "64MB", "jit": "false", "statement_timeout": "30s", "random_page_cost": "1.1", "log_statement": "ddl", "timezone": "Europe/Berlin", "log_min_duration_statement": "-1"}
	s := pgSpec(PostgresSpec{Parameters: ok, Extensions: []string{}}, 0)
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Postgres.Parameters["jit"] != "off" {
		t.Fatalf("bool canonical form: %q", s.Postgres.Parameters["jit"])
	}
	params := pgParameters(store.Database{Name: "orders"}, s, State{MemoryMiB: 1024})
	if params["work_mem"] != "64MB" || params["shared_buffers"] != "256MB" {
		t.Fatalf("user values override tuning, the rest stays: %v %v", params["work_mem"], params["shared_buffers"])
	}
	if _, ok := params["cron.database_name"]; ok {
		t.Fatal("pg_cron settings without the add-on")
	}
	for k, v := range map[string]string{
		"archive_command":       "x",
		"max_connections":       "100",
		"wal_keep_size":         "1GB",
		"fsync":                 "off",
		"work_mem":              "10",
		"shared_buffers":        "1",
		"jit":                   "maybe",
		"log_statement":         "some",
		"statement_timeout":     "soon",
		"timezone":              "x; DROP",
		"cron.max_running_jobs": "5",
	} {
		s := pgSpec(PostgresSpec{Parameters: map[string]string{k: v}, Extensions: []string{}}, 0)
		if err := s.Normalize(); err == nil {
			t.Errorf("%s=%s was accepted", k, v)
		}
	}
	s = pgSpec(PostgresSpec{Parameters: map[string]string{"cron.max_running_jobs": "5"}, Extensions: []string{"pg_cron"}}, 0)
	if err := s.Normalize(); err != nil {
		t.Fatalf("an add-on parameter with its add-on: %v", err)
	}
	s = pgSpec(PostgresSpec{Extensions: []string{"vector", "nope"}}, 0)
	if err := s.Normalize(); err == nil || !strings.Contains(err.Error(), "not an add-on") {
		t.Fatalf("unknown add-on: %v", err)
	}
	s = pgSpec(PostgresSpec{Extensions: []string{"vector", "timescaledb", "vector"}}, 0)
	if err := s.Normalize(); err != nil || strings.Join(s.Postgres.Extensions, ",") != "timescaledb,vector" {
		t.Fatalf("add-ons sorted and unique: %v %v", s.Postgres.Extensions, err)
	}
}

func TestPgConfigBootstrapFrozen(t *testing.T) {
	d := store.Database{ID: "db_x", Name: "orders", Version: "18"}
	s := pgSpec(PostgresSpec{Extensions: []string{}}, 0)
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	st := State{MemoryMiB: 512}
	b, _ := json.Marshal(pgDynamicConfig(d, s, st))
	st.BootstrapDCS = string(b)
	mb := store.DatabaseMember{ID: "dbm_1", Kind: KindData}
	before := patroniConfig(d, s, st, Secrets{}, mb, nil)
	s.Postgres.Parameters = map[string]string{"work_mem": "32MB"}
	st.MemoryMiB = 2048
	if after := patroniConfig(d, s, st, Secrets{}, mb, nil); string(after) != string(before) {
		t.Fatal("a configuration change altered the member's Patroni file (it would recreate members)")
	}
	if configHash(pgDynamicConfig(d, s, st)) == configHash(pgDynamicConfig(d, s, State{MemoryMiB: 512})) {
		t.Fatal("the dynamic configuration ignores memory")
	}
}

func TestWithUnit(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"16384", "8kB"}: "128MB", {"4096", "kB"}: "4MB", {"1000", "kB"}: "1000kB", {"30000", "ms"}: "30000ms", {"-1", "ms"}: "-1", {"on", ""}: "on",
	} {
		if got := withUnit(in[0], in[1]); got != want {
			t.Errorf("withUnit(%v) = %s, want %s", in, got, want)
		}
	}
}
