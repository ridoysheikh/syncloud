package dbs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
)

// Restoring from a WAL-G archive another PostgreSQL server wrote (outside
// SynCloud). The data has none of SynCloud's roles, so the bootstrap
// recovers and promotes it itself, then signs the superuser, replication
// and app users in with this cluster's own passwords (walg-bootstrap,
// walg-adopt). From then on it is an ordinary SynCloud cluster: its own
// passwords, its own archive, and the app user owns its app database.

// PgArchive is a WAL-G archive written by a server outside SynCloud.
type PgArchive struct {
	// Endpoint is a registered S3 endpoint (by name or ID).
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	// Prefix is the archive's WALG_S3_PREFIX path inside the bucket.
	Prefix string `json:"prefix,omitempty"`
	// Key decrypts a libsodium-encrypted archive (the server's
	// WALG_LIBSODIUM_KEY); empty for an archive that is not encrypted.
	Key string `json:"key,omitempty"`
	// KeyTransform is how Key is written, as WALG_LIBSODIUM_KEY_TRANSFORM:
	// none (the default), hex or base64.
	KeyTransform string `json:"keyTransform,omitempty"`
	// Database is the app database inside the archive: the app user
	// becomes its owner. Default: the new cluster's name.
	Database string `json:"database,omitempty"`
}

// PgArchiveInfo is what an archive holds, before restoring from it.
type PgArchiveInfo struct {
	Location string         `json:"location"` // s3://bucket/prefix
	Backups  []PgBaseBackup `json:"backups"`
	Window   *RestoreWindow `json:"window,omitempty"`
	// Version is the PostgreSQL major version of the newest base backup.
	Version string `json:"version,omitempty"`
	// Encrypted: the newest base backup is encrypted (a key is needed);
	// nil when it cannot be told (brotli or uncompressed backups).
	Encrypted *bool `json:"encrypted,omitempty"`
}

var keyTransforms = []string{"none", "hex", "base64"}

func (a *PgArchive) normalize() error {
	if a.Endpoint == "" {
		return errors.New("restore.archive.endpoint: choose an S3 endpoint")
	}
	if !bucketRE.MatchString(a.Bucket) {
		return errors.New("restore.archive.bucket: a bucket name (3–63 lowercase letters, digits, . and -)")
	}
	a.Prefix = strings.Trim(a.Prefix, "/")
	if a.Prefix != "" && (!s3PrefixRE.MatchString(a.Prefix) || strings.Contains(a.Prefix, "..")) {
		return errors.New("restore.archive.prefix: letters, digits and !_.*'()/-")
	}
	a.Key = strings.TrimSpace(a.Key)
	if a.KeyTransform == "" {
		a.KeyTransform = "none"
	}
	if !slices.Contains(keyTransforms, a.KeyTransform) {
		return errors.New("restore.archive.keyTransform must be none, hex or base64")
	}
	if a.Key != "" {
		// As WAL-G reads it: 32 bytes once decoded, or at least 25 as is.
		var n int
		switch a.KeyTransform {
		case "hex":
			b, err := hex.DecodeString(a.Key)
			if err != nil {
				return errors.New("restore.archive.key is not hex")
			}
			n = len(b)
		case "base64":
			b, err := base64.StdEncoding.DecodeString(a.Key)
			if err != nil {
				return errors.New("restore.archive.key is not base64")
			}
			n = len(b)
		default:
			n = 32
			if len(a.Key) < 25 {
				return errors.New("restore.archive.key: WAL-G keys are at least 25 characters")
			}
		}
		if n != 32 {
			return fmt.Errorf("restore.archive.key: %d bytes once decoded; WAL-G libsodium keys are 32", n)
		}
	}
	a.Database = strings.TrimSpace(a.Database)
	if len(a.Database) > 63 || strings.ContainsRune(a.Database, 0) {
		return errors.New("restore.archive.database: a PostgreSQL database name (up to 63 bytes)")
	}
	if a.Database == "template0" || a.Database == "template1" {
		return errors.New("restore.archive.database: a template database cannot be the app database")
	}
	return nil
}

func (a PgArchive) location() string { return "s3://" + a.Bucket + "/" + a.Prefix }

// InspectArchive lists an outside archive's base backups, its restore
// window, its PostgreSQL version and whether it is encrypted.
func (m *Manager) InspectArchive(ctx context.Context, a PgArchive) (PgArchiveInfo, error) {
	if err := a.normalize(); err != nil {
		return PgArchiveInfo{}, ErrInvalid{err}
	}
	out := PgArchiveInfo{Location: a.location(), Backups: []PgBaseBackup{}}
	backups, lastWAL, err := m.listBackups(ctx, a.Endpoint, a.Bucket, a.Prefix)
	if err != nil {
		return out, ErrInvalid{fmt.Errorf("the archive at %s cannot be read: %w", out.Location, err)}
	}
	if len(backups) == 0 {
		return out, nil
	}
	out.Backups, out.Window = backups, restoreWindow(backups, lastWAL)
	out.Version = pgMajor(backups[0].PgVersion)
	if enc, ok, err := m.archiveEncrypted(ctx, a, backups[0].Name); err == nil && ok {
		out.Encrypted = &enc
	}
	return out, nil
}

// planArchiveRestore checks a restore from an outside archive and picks the
// base backup to start from. It returns the archive's major version.
func (m *Manager) planArchiveRestore(ctx context.Context, r PgRestore) (RestoreState, string, error) {
	a := r.Archive
	info, err := m.InspectArchive(ctx, *a)
	if err != nil {
		return RestoreState{}, "", err
	}
	if len(info.Backups) == 0 {
		return RestoreState{}, "", ErrInvalid{fmt.Errorf("no WAL-G base backup in %s (expected %s/basebackups_005/)", info.Location, a.Prefix)}
	}
	rs := RestoreState{SourceName: info.Location, Endpoint: a.Endpoint, Bucket: a.Bucket, Prefix: a.Prefix, External: true, KeyTransform: a.KeyTransform}
	if err := pickBackup(&rs, r, info.Backups, info.Window, info.Location); err != nil {
		return rs, "", err
	}
	var version string
	for _, b := range info.Backups {
		if b.Name == rs.Backup {
			version = pgMajor(b.PgVersion)
			if enc, ok, err := m.archiveEncrypted(ctx, *a, b.Name); err == nil && ok {
				switch {
				case enc && a.Key == "":
					return rs, "", ErrInvalid{fmt.Errorf("backup %s is encrypted: give the source server's WALG_LIBSODIUM_KEY", b.Name)}
				case !enc && a.Key != "":
					return rs, "", ErrInvalid{fmt.Errorf("backup %s is not encrypted: leave the key empty", b.Name)}
				}
			}
		}
	}
	if version == "" {
		return rs, "", ErrInvalid{fmt.Errorf("backup %s does not record its PostgreSQL version (WAL-G before 0.2.10?)", rs.Backup)}
	}
	return rs, version, nil
}

// pickBackup chooses the base backup a restore starts from: the one asked
// for, or the newest that finished before the target time.
func pickBackup(rs *RestoreState, r PgRestore, backups []PgBaseBackup, w *RestoreWindow, source string) error {
	if r.TargetTime != nil {
		t := r.TargetTime.UTC()
		if w != nil && (t.Before(w.From) || t.After(w.To)) {
			return ErrInvalid{fmt.Errorf("the target time must be within the restore window, %s to %s",
				w.From.Format(time.RFC3339), w.To.Format(time.RFC3339))}
		}
		rs.TargetTime = &t
	}
	for _, bb := range backups { // newest first
		if r.Backup != "" && bb.Name != r.Backup {
			continue
		}
		if rs.TargetTime != nil && bb.FinishTime.After(*rs.TargetTime) {
			if r.Backup != "" {
				return ErrInvalid{fmt.Errorf("backup %s finished after the target time", bb.Name)}
			}
			continue
		}
		rs.Backup = bb.Name
		return nil
	}
	if r.Backup != "" {
		return ErrInvalid{fmt.Errorf("no backup %q in the archive of %s", r.Backup, source)}
	}
	return ErrInvalid{errors.New("no base backup finished before the target time")}
}

// restoreWindow spans from the oldest base backup's end to the newest
// archived WAL (or the newest backup).
func restoreWindow(backups []PgBaseBackup, lastWAL *time.Time) *RestoreWindow {
	if len(backups) == 0 {
		return nil
	}
	w := &RestoreWindow{From: backups[len(backups)-1].FinishTime, To: backups[0].FinishTime}
	if lastWAL != nil && lastWAL.After(w.To) {
		w.To = *lastWAL
	}
	return w
}

// pgMajor is the major version of a server_version_num (170006 → "17").
func pgMajor(num int) string {
	if num < 100000 {
		return ""
	}
	return strconv.Itoa(num / 10000)
}

// Compression formats by their first bytes; an encrypted part starts with
// libsodium's random stream header instead.
var compressionMagic = map[string][]byte{
	".zst": {0x28, 0xb5, 0x2f, 0xfd}, ".lz4": {0x04, 0x22, 0x4d, 0x18}, ".lzma": {0x5d, 0x00, 0x00},
}

// archiveEncrypted tells whether a base backup's data is encrypted, from
// the first bytes of its first part. ok is false when the format has no
// signature to tell by (brotli, or no compression).
func (m *Manager) archiveEncrypted(ctx context.Context, a PgArchive, backup string) (enc, ok bool, err error) {
	acc, err := m.S3(ctx, a.Endpoint)
	if err != nil {
		return false, false, err
	}
	cl, err := s3Client(acc)
	if err != nil {
		return false, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	dir := strings.Trim(a.Prefix+"/basebackups_005/"+backup, "/") + "/tar_partitions/"
	var key string
	for o := range cl.ListObjects(ctx, a.Bucket, minio.ListObjectsOptions{Prefix: dir}) {
		if o.Err != nil {
			return false, false, o.Err
		}
		if k := strings.TrimPrefix(o.Key, dir); strings.HasPrefix(k, "part_") && (key == "" || o.Key < key) {
			key = o.Key
		}
	}
	var magic []byte
	for ext, b := range compressionMagic {
		if strings.HasSuffix(key, ext) {
			magic = b
		}
	}
	if magic == nil {
		return false, false, nil
	}
	opts := minio.GetObjectOptions{}
	_ = opts.SetRange(0, int64(len(magic))-1)
	obj, err := cl.GetObject(ctx, a.Bucket, key, opts)
	if err != nil {
		return false, false, err
	}
	defer obj.Close()
	head, err := io.ReadAll(io.LimitReader(obj, int64(len(magic))))
	if err != nil {
		return false, false, err
	}
	return !bytes.Equal(head, magic), true, nil
}

// adoptSQL signs SynCloud's roles into restored data that has none of them:
// one statement per line, for a single-user backend (walg-adopt).
func adoptSQL(sec Secrets) string {
	role := func(name, opts, password string) string {
		set := pgx.Identifier{name}.Sanitize() + " WITH " + opts + " PASSWORD " + quoteLiteral(password)
		return fmt.Sprintf("DO $$BEGIN IF EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = %s) THEN ALTER ROLE %s; ELSE CREATE ROLE %s; END IF; END$$\n",
			quoteLiteral(name), set, set)
	}
	return role(pgSuperuser, "SUPERUSER LOGIN", sec.AdminPassword) +
		role(pgReplication, "NOSUPERUSER REPLICATION LOGIN", sec.ReplicationPassword) +
		role(pgAppUser, "NOSUPERUSER LOGIN", sec.Password)
}

// adoptDatabase hands the app database of a cluster restored from an
// outside archive to the app user: the database, its schemas, tables,
// views, sequences, routines and types (not those of extensions). An object
// that cannot be changed is left as it is, with a warning in the log.
func adoptDatabase(ctx context.Context, ip string, sec Secrets, db string) error {
	c, err := pgConnect(ctx, ip, sec, db)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	app := pgx.Identifier{pgAppUser}.Sanitize()
	if _, err := c.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{db}.Sanitize()+` OWNER TO `+app); err != nil {
		return err
	}
	_, err = c.Exec(ctx, strings.ReplaceAll(adoptObjects, "%APP%", app))
	return err
}

const adoptObjects = `DO $$
DECLARE
	r record;
	app oid := '%APP%'::regrole;
BEGIN
	FOR r IN
		SELECT format('ALTER SCHEMA %I OWNER TO %APP%', n.nspname) AS q FROM pg_namespace n
		WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND n.nspowner <> app
		  AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid = 'pg_namespace'::regclass AND d.objid = n.oid AND d.deptype = 'e')
	UNION ALL
		SELECT format('ALTER %s %s OWNER TO %APP%', CASE c.relkind WHEN 'S' THEN 'SEQUENCE' WHEN 'v' THEN 'VIEW'
			WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'f' THEN 'FOREIGN TABLE' ELSE 'TABLE' END, c.oid::regclass)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S') AND c.relowner <> app
		  AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype IN ('e', 'a', 'i'))
	UNION ALL
		SELECT format('ALTER ROUTINE %s OWNER TO %APP%', p.oid::regprocedure) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND p.proowner <> app
		  AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
	UNION ALL
		SELECT format('ALTER %s %s OWNER TO %APP%', CASE t.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, t.oid::regtype)
		FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace LEFT JOIN pg_class c ON c.oid = t.typrelid
		WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND t.typowner <> app
		  AND (t.typtype IN ('d', 'e', 'r') OR (t.typtype = 'c' AND c.relkind = 'c'))
		  AND NOT EXISTS (SELECT FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
	LOOP
		BEGIN
			EXECUTE r.q;
		EXCEPTION WHEN others THEN
			RAISE WARNING '%: %', r.q, SQLERRM;
		END;
	END LOOP;
END$$`
