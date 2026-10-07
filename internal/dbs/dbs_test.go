package dbs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"syncloud/internal/secgroup"
	"syncloud/internal/store"
)

func TestSpecNormalize(t *testing.T) {
	s := Spec{}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Memory != (Range{256, 256}) || s.Persistence != "aof" || s.EvictionPolicy != "noeviction" || s.CPU != 0.1 || s.Autoscaling.CPUTarget != 60 {
		t.Errorf("defaults: %+v", s)
	}
	for _, bad := range []Spec{
		{Memory: Range{16, 64}}, {Memory: Range{512, 256}}, {Replicas: Range{2, 1}}, {Replicas: Range{0, 9}},
		{Persistence: "both"}, {EvictionPolicy: "lru"}, {Nodes: []string{"Bad"}}, {Autoscaling: Autoscaling{CPUTarget: 99}},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if (Spec{Replicas: Range{0, 0}}).HasSentinels() || !(Spec{Replicas: Range{0, 2}}).HasSentinels() {
		t.Error("sentinels follow the replica maximum")
	}
}

func TestNamesAndInfo(t *testing.T) {
	d := store.Database{ID: "db_abc", EnvironmentID: "env_1", Name: "cache", Environment: "production", Project: "shop"}
	if h := Host(d); h != "cache.production.shop.syncloud.internal" {
		t.Error(h)
	}
	if h := ReadHost(d); h != "cache-ro.production.shop.syncloud.internal" {
		t.Error(h)
	}
	if n, ok := ordinalOf("m2.cache.production.shop.syncloud.internal", d); !ok || n != 2 {
		t.Errorf("ordinalOf: %d %v", n, ok)
	}
	if _, ok := ordinalOf("m2.other.production.shop.syncloud.internal", d); ok {
		t.Error("another database's member matched")
	}
	if v := Volume(d, KindSentinel, 1); v != "syncloud-db-abc-s1" {
		t.Error(v)
	}
	sa := store.Database{ID: "db_xyz", Name: "sessions"}
	if Host(sa) != "sessions.db.syncloud.internal" || ReadHost(sa) != "sessions-ro.db.syncloud.internal" || memberHost(sa, KindData, 1) != "m1.sessions.db.syncloud.internal" {
		t.Errorf("standalone names: %s %s", Host(sa), ReadHost(sa))
	}
	if n, ok := ordinalOf("m1.sessions.db.syncloud.internal", sa); !ok || n != 1 {
		t.Errorf("standalone ordinalOf: %d %v", n, ok)
	}
	if PublicHost(sa, "example.com") != "sessions.db.example.com" || PublicReadHost(sa, "example.com") != "sessions-ro.db.example.com" {
		t.Error("public names")
	}
	if reservedName("engines") == nil || reservedName("cache-ro") == nil || reservedName("cache") != nil {
		t.Error("reserved names")
	}
	i := parseInfo("# Server\r\nrole:master\r\nused_memory:1024\r\ndb0:keys=3,expires=0,avg_ttl=0\r\ndb2:keys=4,expires=1\r\n")
	if i["role"] != "master" || i.Int("used_memory") != 1024 || i.Keys() != 7 {
		t.Errorf("info: %v keys %d", i, i.Keys())
	}
}

func TestTaskSpecIsStable(t *testing.T) {
	d := store.Database{ID: "db_abc", EnvironmentID: "env_1", Name: "cache", Environment: "production", Project: "shop", Version: "8.1"}
	spec := Spec{Replicas: Range{1, 3}}
	if err := spec.Normalize(); err != nil {
		t.Fatal(err)
	}
	sec := Secrets{Password: "p", AdminPassword: "a"}
	m := store.DatabaseMember{ID: "dbm_1", Kind: KindData, Ordinal: 1}
	st := State{MemoryMiB: 256, Replicas: 1, LimitMiB: 1024}
	a := taskSpec(d, spec, st, sec, m, []string{"10.91.1.1"}, nil)
	// Memory, replicas and the primary change online: no new container.
	st2 := st
	st2.MemoryMiB, st2.Replicas, st2.Primary = 512, 3, 1
	if specHash(a) != specHash(taskSpec(d, spec, st2, sec, m, []string{"10.91.1.1"}, nil)) {
		t.Error("online changes alter the container spec")
	}
	st3 := st
	st3.LimitMiB = 4096
	if specHash(a) == specHash(taskSpec(d, spec, st3, sec, m, []string{"10.91.1.1"}, nil)) {
		t.Error("a new memory limit does not recreate the member")
	}
	if a.Env["SELF"] != "m1.cache.production.shop.syncloud.internal" || !strings.Contains(a.Env["SENTINELS"], "s2.cache") {
		t.Errorf("env: %v", a.Env)
	}
	if a.Mounts[0].Source != "syncloud-db-abc-m1" || a.MemoryLimitBytes != int64(containerLimit(1024))<<20 {
		t.Errorf("mounts %v limit %d", a.Mounts, a.MemoryLimitBytes)
	}
	single := Spec{}
	_ = single.Normalize()
	if s := taskSpec(d, single, st, sec, m, nil, nil); s.Env["SENTINELS"] != "" {
		t.Error("a database without replicas runs no sentinels")
	}
}

func TestNetwork(t *testing.T) {
	// Defaults: a project database's own environment; nothing standalone.
	if a := (store.Database{EnvironmentID: "env_1", Project: "shop", Environment: "prod"}).ParseNetwork().Access; len(a) != 1 || a[0] != "environment:shop/prod" {
		t.Errorf("project default: %v", a)
	}
	if a := (store.Database{}).ParseNetwork().Access; len(a) != 0 {
		t.Errorf("standalone default: %v", a)
	}
	n := Network{Access: []string{"project:shop", " project:shop", "environment:billing/prod", "10.0.0.7"}, Public: store.DatabasePublic{Enabled: true}}
	if err := NormalizeNetwork(&n, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(n.Access, " ") != "project:shop environment:billing/prod 10.0.0.7/32" {
		t.Errorf("access: %v", n.Access)
	}
	if strings.Join(n.Public.Allow, " ") != "0.0.0.0/0 ::/0" {
		t.Errorf("public allow defaults to anywhere: %v", n.Public.Allow)
	}
	n = Network{Public: store.DatabasePublic{Enabled: true, Allow: []string{"203.0.113.9", "2001:db8::/32"}}}
	if err := NormalizeNetwork(&n, nil); err != nil || strings.Join(n.Public.Allow, " ") != "203.0.113.9/32 2001:db8::/32" {
		t.Errorf("allow: %v %v", n.Public.Allow, err)
	}
	for _, bad := range []Network{
		{Access: []string{"environment:prod"}}, {Access: []string{"environment:self"}}, {Access: []string{"self"}},
		{Access: []string{"nonsense:x"}}, {Public: store.DatabasePublic{Allow: []string{"example.com"}}},
	} {
		if err := NormalizeNetwork(&bad, nil); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	missing := Network{Access: []string{"project:ghost"}}
	if err := NormalizeNetwork(&missing, func(secgroup.Peer) bool { return false }); err == nil {
		t.Error("accepted a missing project")
	}
}

func TestPostgresSpecAndConfig(t *testing.T) {
	s := Spec{}
	s.ForEngine(EnginePostgres)
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Memory != (Range{512, 512}) || s.CPU != 0.5 || s.Postgres.MaxConnections != 64 || s.Persistence != "" || s.HasSentinels() {
		t.Errorf("defaults: %+v %+v", s, s.Postgres)
	}
	bad := Spec{Postgres: &PostgresSpec{Synchronous: true}}
	if err := bad.Normalize(); err == nil {
		t.Error("synchronous without a replica was accepted")
	}
	v := Spec{Postgres: &PostgresSpec{}}
	v.ForEngine(EngineValkey)
	if v.Postgres != nil {
		t.Error("a Valkey spec kept a postgres section")
	}

	d := store.Database{ID: "db_abc", Name: "order-db", Engine: EnginePostgres}
	sec := Secrets{Password: "p", AdminPassword: "a", ReplicationPassword: "r", RestPassword: "rest", EtcdPassword: "e"}
	st := State{MemoryMiB: 1024, LimitMiB: 1024, Replicas: 1}
	mb := store.DatabaseMember{ID: "dbm_1", Kind: KindData, Ordinal: 1}
	var cfg map[string]any
	if err := json.Unmarshal(patroniConfig(d, s, st, sec, mb, []string{"e0.etcd.syncloud.internal:2379"}), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["scope"] != "db_abc" || cfg["namespace"] != "/syncloud/pg/" || cfg["name"] != "m1" {
		t.Errorf("identity: %v", cfg)
	}
	etcd := cfg["etcd3"].(map[string]any)
	if etcd["username"] != "pg_abc" || etcd["password"] != "e" {
		t.Errorf("etcd: %v", etcd)
	}
	pgc := cfg["postgresql"].(map[string]any)
	if pgc["connect_address"] != "m1.order-db.db.syncloud.internal:5432" {
		t.Errorf("connect address: %v", pgc["connect_address"])
	}
	params := cfg["bootstrap"].(map[string]any)["dcs"].(map[string]any)["postgresql"].(map[string]any)["parameters"].(map[string]any)
	if params["shared_buffers"] != "256MB" || params["effective_cache_size"] != "768MB" || params["cron.database_name"] != "order_db" ||
		!strings.Contains(params["shared_preload_libraries"].(string), "timescaledb") {
		t.Errorf("parameters: %v", params)
	}
	ts := pgTaskSpec("img", d, s, st, sec, mb, nil, nil, nil)
	if ts.Command[0] != "patroni" || ts.Env["PATRONI_CONFIG_B64"] == "" || ts.MemoryLimitBytes != 1024<<20 || ts.Mounts[0].Source != "syncloud-db-abc-m1" {
		t.Errorf("task spec: %+v", ts)
	}
}

func TestPgHealth(t *testing.T) {
	d := store.Database{CreatedAt: time.Now().Add(-time.Hour)}
	st := State{Replicas: 1, Bootstrapped: true}
	lead := Member{Kind: KindData, State: store.TaskRunning, Role: "primary", LinkUp: true}
	rep := Member{Kind: KindData, State: store.TaskRunning, Role: "replica", LinkUp: true}
	if h := pgHealth(d, st, []Member{lead, rep}, d.CreatedAt); h != "healthy" {
		t.Error(h)
	}
	rep.LinkUp = false
	if h := pgHealth(d, st, []Member{lead, rep}, d.CreatedAt); h != "degraded" {
		t.Error(h)
	}
	if h := pgHealth(d, st, []Member{rep}, d.CreatedAt); h != "down" {
		t.Error(h)
	}
	if h := pgHealth(d, State{Replicas: 1}, []Member{rep}, time.Now()); h != "starting" {
		t.Error(h)
	}
}
