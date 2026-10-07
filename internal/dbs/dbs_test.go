package dbs

import (
	"strings"
	"testing"

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
	d := store.Database{ID: "db_abc", Name: "cache", Environment: "production", Project: "shop"}
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
	i := parseInfo("# Server\r\nrole:master\r\nused_memory:1024\r\ndb0:keys=3,expires=0,avg_ttl=0\r\ndb2:keys=4,expires=1\r\n")
	if i["role"] != "master" || i.Int("used_memory") != 1024 || i.Keys() != 7 {
		t.Errorf("info: %v keys %d", i, i.Keys())
	}
}

func TestTaskSpecIsStable(t *testing.T) {
	d := store.Database{ID: "db_abc", Name: "cache", Environment: "production", Project: "shop", Version: "8.1"}
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
