package dbs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"syncloud/internal/store"
)

func TestBackupSpec(t *testing.T) {
	b := PgBackupSpec{Endpoint: "minio", Bucket: "pg-backups", Prefix: "/team/a/"}
	if err := b.normalize(); err != nil {
		t.Fatal(err)
	}
	if b.EveryHours != 24 || b.RetainFull != 7 || b.RetainDays != 7 || b.Prefix != "team/a" {
		t.Errorf("defaults: %+v", b)
	}
	for _, bad := range []PgBackupSpec{
		{Bucket: "x-bucket"},
		{Endpoint: "e", Bucket: "Bad_Bucket"},
		{Endpoint: "e", Bucket: "ok-bucket", Prefix: "../x"},
		{Endpoint: "e", Bucket: "ok-bucket", EveryHours: 200},
	} {
		if err := bad.normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	d := store.Database{ID: "db_abc"}
	if p := backupPrefix(d, &PgBackupSpec{}); p != "syncloud-pg/db_abc" {
		t.Error(p)
	}
	id := backupTaskID(d)
	if !strings.HasPrefix(id, BackupPrefix+"abc_") || backupDatabaseID(id) != "db_abc" {
		t.Errorf("task ID %q -> %q", id, backupDatabaseID(id))
	}
}

func TestArchiveConfig(t *testing.T) {
	d := store.Database{ID: "db_abc", Name: "orders"}
	mb := store.DatabaseMember{Kind: KindData, Ordinal: 0}
	off := Spec{Postgres: &PostgresSpec{MaxConnections: 100}}
	on := Spec{Postgres: &PostgresSpec{MaxConnections: 100, Backup: &PgBackupSpec{Endpoint: "e", Bucket: "b"}}}
	cfg := func(s Spec, st State) map[string]any {
		var c map[string]any
		_ = json.Unmarshal(patroniConfig(d, s, st, Secrets{}, mb, nil), &c)
		return c
	}
	pg := func(c map[string]any) map[string]any { return c["postgresql"].(map[string]any) }

	c := cfg(off, State{MemoryMiB: 512})
	if p := pg(c)["parameters"].(map[string]any); p["archive_command"] != "/bin/true" || p["unix_socket_directories"] != "/tmp,/data/run" {
		t.Errorf("off: %v", p)
	}
	if _, ok := pg(c)["create_replica_methods"]; ok {
		t.Error("WAL-G replicas without backups")
	}
	c = cfg(on, State{MemoryMiB: 512})
	if p := pg(c)["parameters"].(map[string]any); p["archive_command"] != "wal-g wal-push %p" || p["archive_timeout"] != "60s" {
		t.Errorf("on: %v", p)
	}
	if rc := pg(c)["recovery_conf"].(map[string]any); rc["restore_command"] != "timeout 30 wal-g wal-fetch %f %p" {
		t.Errorf("recovery_conf: %v", rc)
	}
	if m := pg(c)["create_replica_methods"].([]any); len(m) != 2 || m[0] != "walg" {
		t.Errorf("replica methods: %v", m)
	}
	if _, ok := c["bootstrap"].(map[string]any)["method"]; ok {
		t.Error("restore bootstrap without a restore")
	}
	target := time.Date(2026, 10, 7, 12, 30, 5, 0, time.UTC)
	c = cfg(on, State{MemoryMiB: 512, Restore: &RestoreState{Backup: "base_1", TargetTime: &target}})
	bs := c["bootstrap"].(map[string]any)
	rc := bs["walg"].(map[string]any)["recovery_conf"].(map[string]any)
	if bs["method"] != "walg" || rc["recovery_target_time"] != "2026-10-07 12:30:05.000000+00" || rc["recovery_target_action"] != "promote" ||
		rc["restore_command"] != "/usr/local/bin/walg-restore-wal %f %p" {
		t.Errorf("restore bootstrap: %v", bs)
	}
}

func TestWalgEnv(t *testing.T) {
	m := &Manager{S3: func(_ context.Context, ref string) (S3Access, error) {
		return S3Access{URL: "http://" + ref + ":9000", AccessKeyID: "ak-" + ref, SecretAccessKey: "sk", PathStyle: true}, nil
	}}
	d := store.Database{ID: "db_new"}
	spec := Spec{Postgres: &PostgresSpec{Backup: &PgBackupSpec{Endpoint: "minio", Bucket: "pg"}}}
	sec := Secrets{WalgKey: "aa", RestoreWalgKey: "bb"}
	env, err := m.walgEnv(context.Background(), d, spec, State{}, sec)
	if err != nil {
		t.Fatal(err)
	}
	if env["WALG_S3_PREFIX"] != "s3://pg/syncloud-pg/db_new" || env["WALG_LIBSODIUM_KEY"] != "aa" || env["AWS_ENDPOINT"] != "http://minio:9000" ||
		env["AWS_S3_FORCE_PATH_STYLE"] != "true" || env["AWS_REGION"] != "us-east-1" || env["WALG_RESTORE_S3_PREFIX"] != "" {
		t.Errorf("env: %v", env)
	}
	st := State{Restore: &RestoreState{Endpoint: "minio", Bucket: "pg", Prefix: "syncloud-pg/db_old", Backup: "base_1"}}
	env, _ = m.walgEnv(context.Background(), d, spec, st, sec)
	if env["WALG_RESTORE_S3_PREFIX"] != "s3://pg/syncloud-pg/db_old" || env["WALG_RESTORE_LIBSODIUM_KEY"] != "bb" || env["WALG_RESTORE_BACKUP"] != "base_1" {
		t.Errorf("restore env: %v", env)
	}
	if env, _ := m.walgEnv(context.Background(), d, Spec{Postgres: &PostgresSpec{}}, State{}, sec); env != nil {
		t.Errorf("no backups, no env: %v", env)
	}
	if PgDatabase(store.Database{Name: "orders-copy", State: `{"database":"orders"}`}) != "orders" {
		t.Error("a restored cluster keeps the source's database name")
	}
}
