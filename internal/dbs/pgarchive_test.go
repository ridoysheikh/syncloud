package dbs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestArchiveNormalize(t *testing.T) {
	a := PgArchive{Endpoint: "aws", Bucket: "old-server", Prefix: "/walg/main/", Database: " shop "}
	if err := a.normalize(); err != nil {
		t.Fatal(err)
	}
	if a.Prefix != "walg/main" || a.KeyTransform != "none" || a.Database != "shop" || a.location() != "s3://old-server/walg/main" {
		t.Errorf("normalized: %+v", a)
	}
	hexKey := strings.Repeat("ab", 32)
	for _, ok := range []PgArchive{
		{Endpoint: "e", Bucket: "bkt", Key: hexKey, KeyTransform: "hex"},
		{Endpoint: "e", Bucket: "bkt", Key: base64.StdEncoding.EncodeToString(make([]byte, 32)), KeyTransform: "base64"},
		{Endpoint: "e", Bucket: "bkt", Key: "a plain passphrase of 25+ chars"},
	} {
		if err := ok.normalize(); err != nil {
			t.Errorf("refused %+v: %v", ok, err)
		}
	}
	for _, bad := range []PgArchive{
		{Bucket: "bkt"},
		{Endpoint: "e", Bucket: "Bad_Bucket"},
		{Endpoint: "e", Bucket: "bkt", Prefix: "../x"},
		{Endpoint: "e", Bucket: "bkt", Key: hexKey[:62], KeyTransform: "hex"},
		{Endpoint: "e", Bucket: "bkt", Key: "zz", KeyTransform: "hex"},
		{Endpoint: "e", Bucket: "bkt", Key: "short"},
		{Endpoint: "e", Bucket: "bkt", Key: hexKey, KeyTransform: "rot13"},
		{Endpoint: "e", Bucket: "bkt", Database: "template1"},
	} {
		if err := bad.normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestPickBackup(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 12, h, 0, 0, 0, time.UTC) }
	backups := []PgBaseBackup{{Name: "base_new", FinishTime: at(12)}, {Name: "base_old", FinishTime: at(6)}} // newest first
	w := restoreWindow(backups, &[]time.Time{at(15)}[0])
	if !w.From.Equal(at(6)) || !w.To.Equal(at(15)) {
		t.Fatalf("window %+v", w)
	}
	pick := func(r PgRestore) (string, error) {
		var rs RestoreState
		err := pickBackup(&rs, r, backups, w, "src")
		return rs.Backup, err
	}
	if b, _ := pick(PgRestore{}); b != "base_new" {
		t.Errorf("latest: %s", b)
	}
	t9 := at(9)
	if b, _ := pick(PgRestore{TargetTime: &t9}); b != "base_old" {
		t.Errorf("before 9:00: %s", b)
	}
	if _, err := pick(PgRestore{TargetTime: &t9, Backup: "base_new"}); err == nil {
		t.Error("a backup that finished after the target")
	}
	t20 := at(20)
	if _, err := pick(PgRestore{TargetTime: &t20}); err == nil {
		t.Error("a target outside the window")
	}
	if _, err := pick(PgRestore{Backup: "base_x"}); err == nil {
		t.Error("an unknown backup")
	}
	if pgMajor(170006) != "17" || pgMajor(180001) != "18" || pgMajor(0) != "" {
		t.Error("pgMajor")
	}
}

func TestExternalRestoreConfig(t *testing.T) {
	d := store.Database{ID: "db_abc", Name: "shop"}
	mb := store.DatabaseMember{Kind: KindData}
	spec := Spec{Postgres: &PostgresSpec{MaxConnections: 100, Backup: &PgBackupSpec{Endpoint: "minio", Bucket: "pg"}}}
	target := time.Date(2026, 10, 7, 12, 30, 5, 0, time.UTC)
	st := State{MemoryMiB: 512, Database: "shop", Restore: &RestoreState{External: true, KeyTransform: "none",
		Endpoint: "aws", Bucket: "old", Prefix: "walg", Backup: "base_1", TargetTime: &target}}

	var c map[string]any
	_ = json.Unmarshal(patroniConfig(d, spec, st, Secrets{}, mb, nil), &c)
	bs := c["bootstrap"].(map[string]any)
	walg := bs["walg"].(map[string]any)
	if bs["method"] != "walg" || walg["command"] != "/usr/local/bin/walg-bootstrap" {
		t.Errorf("bootstrap: %v", bs)
	}
	if _, ok := walg["recovery_conf"]; ok {
		t.Error("the bootstrap script recovers outside archives itself")
	}

	m := &Manager{S3: func(_ context.Context, ref string) (S3Access, error) {
		return S3Access{URL: "http://" + ref + ":9000", AccessKeyID: "ak-" + ref, SecretAccessKey: "sk"}, nil
	}}
	sec := Secrets{WalgKey: "aa", RestoreWalgKey: "", AdminPassword: "adm", ReplicationPassword: "rep", Password: "app'pw"}
	env, err := m.walgEnv(context.Background(), d, spec, st, sec)
	if err != nil {
		t.Fatal(err)
	}
	// Archiving keeps its own endpoint; the restore reads the other one.
	if env["AWS_ENDPOINT"] != "http://minio:9000" || env["AWS_ACCESS_KEY_ID"] != "ak-minio" ||
		env["WALG_RESTORE_AWS_ENDPOINT"] != "http://aws:9000" || env["WALG_RESTORE_AWS_ACCESS_KEY_ID"] != "ak-aws" {
		t.Errorf("endpoints: %v", env)
	}
	if env["WALG_RESTORE_S3_PREFIX"] != "s3://old/walg" || env["WALG_RESTORE_EXTERNAL"] != "1" || env["WALG_RESTORE_LIBSODIUM_KEY_TRANSFORM"] != "none" ||
		env["WALG_RESTORE_TARGET_TIME"] != "2026-10-07 12:30:05.000000+00" || env["WALG_RESTORE_LIBSODIUM_KEY"] != "" {
		t.Errorf("restore env: %v", env)
	}
	sql, _ := base64.StdEncoding.DecodeString(env["SYNCLOUD_ADOPT_SQL_B64"])
	lines := strings.Split(strings.TrimSuffix(string(sql), "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `ALTER ROLE "syncloud_admin" WITH SUPERUSER LOGIN PASSWORD 'adm'`) ||
		!strings.Contains(lines[1], `CREATE ROLE "replicator" WITH NOSUPERUSER REPLICATION LOGIN PASSWORD 'rep'`) ||
		!strings.Contains(lines[2], `PASSWORD 'app''pw'`) {
		t.Errorf("adopt SQL:\n%s", sql)
	}

	// A SynCloud source keeps its hex key and Patroni's recovery.
	st.Restore.External = false
	env, _ = m.walgEnv(context.Background(), d, spec, st, sec)
	if env["WALG_RESTORE_LIBSODIUM_KEY_TRANSFORM"] != "hex" || env["WALG_RESTORE_EXTERNAL"] != "" || env["SYNCLOUD_ADOPT_SQL_B64"] != "" {
		t.Errorf("internal restore env: %v", env)
	}
}
