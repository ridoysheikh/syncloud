package dbs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/store"
)

// WAL-G backups and point-in-time recovery (Phase 13c). Members archive
// every WAL segment to S3 as it fills (or after 60 s); one-shot tasks take
// base backups on a schedule; a restore bootstraps a new cluster from a
// base backup and replays the archive up to a moment.

// BackupPrefix is the task ID prefix of base backup runs.
const BackupPrefix = "dbk_"

// socketDir is where Postgres also listens on a Unix socket inside the
// member's volume, so a backup task sharing the volume can connect
// without the network.
const socketDir = "/data/run"

// PgBackupSpec turns on continuous archiving and scheduled base backups.
type PgBackupSpec struct {
	// Endpoint is a registered S3 endpoint (by name or ID); its
	// credentials stay sealed there.
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	// Prefix inside the bucket; default syncloud-pg/<database ID>.
	Prefix string `json:"prefix,omitempty"`
	// EveryHours between base backups (1–168, default 24).
	EveryHours int `json:"everyHours"`
	// RetainFull base backups are kept (1–100, default 7)...
	RetainFull int `json:"retainFull"`
	// ...and every backup of the last RetainDays days (0–365, default 7),
	// which is how far back a restore can go.
	RetainDays int `json:"retainDays"`
}

var (
	bucketRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3PrefixRE  = regexp.MustCompile(`^[A-Za-z0-9!_.*'()/-]{1,200}$`)
	errNoBackup = errors.New("backups are not configured for this database")
)

func (b *PgBackupSpec) normalize() error {
	if b.Endpoint == "" {
		return errors.New("postgres.backup.endpoint: choose an S3 endpoint")
	}
	if !bucketRE.MatchString(b.Bucket) {
		return errors.New("postgres.backup.bucket: a bucket name (3–63 lowercase letters, digits, . and -)")
	}
	b.Prefix = strings.Trim(b.Prefix, "/")
	if b.Prefix != "" && (!s3PrefixRE.MatchString(b.Prefix) || strings.Contains(b.Prefix, "..")) {
		return errors.New("postgres.backup.prefix: letters, digits and !_.*'()/-")
	}
	if b.EveryHours == 0 {
		b.EveryHours = 24
	}
	if b.RetainFull == 0 {
		b.RetainFull = 7
	}
	if b.RetainDays == 0 {
		b.RetainDays = 7
	}
	if b.EveryHours < 1 || b.EveryHours > 168 {
		return errors.New("postgres.backup.everyHours must be between 1 and 168")
	}
	if b.RetainFull < 1 || b.RetainFull > 100 {
		return errors.New("postgres.backup.retainFull must be between 1 and 100")
	}
	if b.RetainDays < 0 || b.RetainDays > 365 {
		return errors.New("postgres.backup.retainDays must be between 0 and 365")
	}
	return nil
}

// backupPrefix is where a cluster's archive lives in its bucket.
func backupPrefix(d store.Database, b *PgBackupSpec) string {
	if b.Prefix != "" {
		return b.Prefix
	}
	return "syncloud-pg/" + d.ID
}

// PgRestore asks for a new cluster restored from another one's archive.
type PgRestore struct {
	// From is the source database (name). It is never changed.
	From string `json:"from"`
	// Backup is a base backup name; default the newest one that finished
	// before TargetTime (or the newest).
	Backup string `json:"backup,omitempty"`
	// TargetTime is the moment to recover to; nil replays the whole
	// archive (a clone of the latest state).
	TargetTime *time.Time `json:"targetTime,omitempty"`
}

// RestoreState records how a cluster was created from an archive.
type RestoreState struct {
	Source     string     `json:"source"`     // the source's ID
	SourceName string     `json:"sourceName"` // and name, as it was
	Endpoint   string     `json:"endpoint"`
	Bucket     string     `json:"bucket"`
	Prefix     string     `json:"prefix"`
	Backup     string     `json:"backup"`
	TargetTime *time.Time `json:"targetTime,omitempty"`
}

// S3Access is an S3 endpoint's address and credentials.
type S3Access struct {
	URL, Region, AccessKeyID, SecretAccessKey string
	PathStyle                                 bool
}

func newWalgKey() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// s3Env is what WAL-G needs to reach an endpoint.
func s3Env(a S3Access) map[string]string {
	region := a.Region
	if region == "" {
		region = "us-east-1"
	}
	return map[string]string{
		"AWS_ENDPOINT": a.URL, "AWS_REGION": region, "AWS_S3_FORCE_PATH_STYLE": strconv.FormatBool(a.PathStyle),
		"AWS_ACCESS_KEY_ID": a.AccessKeyID, "AWS_SECRET_ACCESS_KEY": a.SecretAccessKey,
	}
}

// walgEnv is the WAL-G environment of a cluster's containers (nil when it
// neither archives nor restores).
func (m *Manager) walgEnv(ctx context.Context, d store.Database, spec Spec, st State, sec Secrets) (map[string]string, error) {
	var b *PgBackupSpec
	if spec.Postgres != nil {
		b = spec.Postgres.Backup
	}
	if b == nil && st.Restore == nil {
		return nil, nil
	}
	if m.S3 == nil {
		return nil, errors.New("S3 endpoints are not available")
	}
	env := map[string]string{"WALG_COMPRESSION_METHOD": "zstd", "WALG_LIBSODIUM_KEY_TRANSFORM": "hex"}
	if b != nil {
		a, err := m.S3(ctx, b.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("backup endpoint %s: %w", b.Endpoint, err)
		}
		for k, v := range s3Env(a) {
			env[k] = v
		}
		env["WALG_S3_PREFIX"] = "s3://" + b.Bucket + "/" + backupPrefix(d, b)
		env["WALG_LIBSODIUM_KEY"] = sec.WalgKey
	}
	if r := st.Restore; r != nil {
		a, err := m.S3(ctx, r.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("restore endpoint %s: %w", r.Endpoint, err)
		}
		if b == nil || b.Endpoint != r.Endpoint {
			for k, v := range s3Env(a) {
				env[k] = v
			}
		}
		env["WALG_RESTORE_S3_PREFIX"] = "s3://" + r.Bucket + "/" + r.Prefix
		env["WALG_RESTORE_LIBSODIUM_KEY"] = sec.RestoreWalgKey
		env["WALG_RESTORE_BACKUP"] = r.Backup
	}
	return env, nil
}

// archiveConfig adds archiving, the archive's restore command and WAL-G
// replica cloning to a member's local Patroni settings, and the restore
// bootstrap to a restored cluster's first member.
func archiveConfig(cfg map[string]any, spec Spec, st State) {
	pgc := cfg["postgresql"].(map[string]any)
	params := pgc["parameters"].(map[string]any)
	params["unix_socket_directories"] = "/tmp," + socketDir
	if spec.Postgres != nil && spec.Postgres.Backup != nil {
		params["archive_command"] = "wal-g wal-push %p"
		params["archive_timeout"] = "60s"
		// Bounded: with the archive unreachable, a member still starts
		// from its own WAL and streaming instead of waiting on S3 forever.
		pgc["recovery_conf"] = map[string]string{"restore_command": "timeout 30 wal-g wal-fetch %f %p"}
		pgc["create_replica_methods"] = []string{"walg", "basebackup"}
		pgc["walg"] = map[string]any{"command": "/usr/local/bin/walg-replica", "no_leader": false, "keep_data": false}
		pgc["basebackup"] = map[string]any{"checkpoint": "fast"}
	} else {
		params["archive_command"] = "/bin/true"
	}
	if r := st.Restore; r != nil {
		rc := map[string]string{
			"restore_command":          "/usr/local/bin/walg-restore-wal %f %p",
			"recovery_target_action":   "promote",
			"recovery_target_timeline": "latest",
		}
		if r.TargetTime != nil {
			rc["recovery_target_time"] = r.TargetTime.UTC().Format("2006-01-02 15:04:05.000000+00")
		}
		bs := cfg["bootstrap"].(map[string]any)
		bs["method"] = "walg"
		bs["walg"] = map[string]any{"command": "/usr/local/bin/walg-bootstrap", "keep_existing_recovery_conf": false, "recovery_conf": rc}
	}
}

// ── base backups ────────────────────────────────────────────────────────────

// backupTaskID ties a backup run to its database.
func backupTaskID(d store.Database) string {
	return BackupPrefix + strings.TrimPrefix(d.ID, "db_") + "_" + newWalgKey()[:10]
}

func backupDatabaseID(taskID string) string {
	rest := strings.TrimPrefix(taskID, BackupPrefix)
	if i := strings.LastIndexByte(rest, '_'); i > 0 {
		return "db_" + rest[:i]
	}
	return ""
}

// backupStale is how long a run may take before it counts as failed.
const backupStale = 12 * time.Hour

// backupRetry is the wait after a failed run.
const backupRetry = time.Hour

// backupTick starts due base backups and gives up on lost runs.
func (m *Manager) backupTick(ctx context.Context, d store.Database) {
	spec, err := parseSpec(d.Spec)
	if err != nil || spec.Postgres == nil || spec.Postgres.Backup == nil || d.Deleting {
		return
	}
	st := parseState(d.State)
	if !st.Bootstrapped {
		return
	}
	runs, err := m.st.DatabaseBackups(ctx, d.ID, 5)
	if err != nil {
		return
	}
	now := m.now().UTC()
	var lastOK, lastFail time.Time
	for _, r := range runs {
		switch r.State {
		case "running":
			if now.Sub(r.StartedAt) > backupStale {
				m.finishBackup(ctx, d, r.ID, "failed", "the run did not report back within 12 hours")
			}
			return // one at a time
		case "ok":
			if lastOK.IsZero() {
				lastOK = r.StartedAt
			}
		case "failed":
			if lastFail.IsZero() {
				lastFail = r.StartedAt
			}
		}
	}
	every := time.Duration(spec.Postgres.Backup.EveryHours) * time.Hour
	switch {
	case !lastOK.IsZero() && now.Sub(lastOK) < every:
	case !lastFail.IsZero() && lastFail.After(lastOK) && now.Sub(lastFail) < backupRetry:
	default:
		if _, err := m.startBackup(ctx, d, "schedule"); err != nil && !errors.Is(err, ErrUnavailable) {
			m.log.Warn("start backup", "database", d.Name, "err", err)
		}
	}
}

// StartBackup takes a base backup now.
func (m *Manager) StartBackup(ctx context.Context, d store.Database) (store.DatabaseBackup, error) {
	spec, _ := parseSpec(d.Spec)
	if spec.Postgres == nil || spec.Postgres.Backup == nil {
		return store.DatabaseBackup{}, ErrInvalid{errNoBackup}
	}
	runs, err := m.st.DatabaseBackups(ctx, d.ID, 1)
	if err != nil {
		return store.DatabaseBackup{}, err
	}
	if len(runs) > 0 && runs[0].State == "running" {
		return store.DatabaseBackup{}, ErrInvalid{errors.New("a backup is already running")}
	}
	return m.startBackup(ctx, d, "manual")
}

// startBackup runs wal-g backup-push on a streaming replica's node (the
// primary's when there is none), reading that member's volume.
func (m *Manager) startBackup(ctx context.Context, d store.Database, trigger string) (store.DatabaseBackup, error) {
	spec, _ := parseSpec(d.Spec)
	st := parseState(d.State)
	sec, err := m.secrets(d)
	if err != nil {
		return store.DatabaseBackup{}, err
	}
	members, err := m.st.DatabaseMembers(ctx, d.ID)
	if err != nil {
		return store.DatabaseBackup{}, err
	}
	var src *store.DatabaseMember
	m.mu.Lock()
	for i, mb := range members {
		if mb.Kind != KindData || mb.State != store.TaskRunning || !m.connected(mb.NodeID) {
			continue
		}
		l := m.live[mb.ID]
		if mb.Ordinal != st.Primary && l != nil && l.LinkUp {
			src = &members[i] // a streaming replica: the primary does not pay for the copy
			break
		}
		if mb.Ordinal == st.Primary && src == nil {
			src = &members[i]
		}
	}
	m.mu.Unlock()
	if src == nil {
		return store.DatabaseBackup{}, ErrUnavailable
	}
	env, err := m.walgEnv(ctx, d, spec, st, sec)
	if err != nil {
		return store.DatabaseBackup{}, err
	}
	b := spec.Postgres.Backup
	now := m.now().UTC()
	run := store.DatabaseBackup{ID: backupTaskID(d), DatabaseID: d.ID, NodeID: src.NodeID, Member: memberName(*src), Trigger: trigger, StartedAt: now}
	// The task shares the member's network namespace (its address,
	// firewall rules and resolver) and its volume (read-only); it signs in
	// over the socket in the volume.
	env["PGHOST"], env["PGUSER"], env["PGDATABASE"] = socketDir, pgSuperuser, "postgres"
	env["PGDATA"] = "/data/pgdata"
	env["RETAIN_FULL"] = strconv.Itoa(b.RetainFull)
	script := `set -eu
echo "base backup of $PGDATA"
wal-g backup-push "$PGDATA"
echo "retention: keep $RETAIN_FULL full backups` + map[bool]string{true: ` and everything after $RETAIN_AFTER"
wal-g delete retain FULL "$RETAIN_FULL" --after "$RETAIN_AFTER" --confirm`, false: `"
wal-g delete retain FULL "$RETAIN_FULL" --confirm`}[b.RetainDays > 0] + `
echo done`
	if b.RetainDays > 0 {
		env["RETAIN_AFTER"] = now.AddDate(0, 0, -b.RetainDays).Format(time.RFC3339)
	}
	ts := &agentv1.TaskSpec{
		TaskId: run.ID, Image: m.pgImage(d.Version), NetworkMode: "task:" + src.ID,
		Restart: agentv1.RestartPolicy_RESTART_POLICY_NO,
		Mounts:  []*agentv1.Mount{{Type: agentv1.Mount_TYPE_VOLUME, Source: Volume(d, src.Kind, src.Ordinal), Target: "/data", ReadOnly: true}},
		Command: []string{"sh", "-c", script}, Env: env,
		MemoryLimitBytes: 512 << 20,
		Labels: map[string]string{
			"syncloud.project": d.Project, "syncloud.environment": d.Environment, "syncloud.service": d.Name,
			"syncloud.service_id": d.ID, "syncloud.database": d.Name, "syncloud.db_member": "backup",
		},
	}
	ts.Name = fmt.Sprintf("db-%s-backup-%d", d.Name, now.Unix())
	if err := m.st.AddDatabaseBackup(ctx, run); err != nil {
		return run, err
	}
	if err := m.gw.Send(src.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: ts}}}); err != nil {
		m.finishBackup(ctx, d, run.ID, "failed", "could not reach the node: "+err.Error())
		return run, err
	}
	m.event(ctx, d.ID, "backup", "", "started on "+run.Member, "", trigger)
	m.log.Info("base backup started", "database", d.Name, "member", run.Member, "trigger", trigger)
	return run, nil
}

func (m *Manager) finishBackup(ctx context.Context, d store.Database, id, state, reason string) {
	ok, err := m.st.FinishDatabaseBackup(ctx, id, state, reason, m.now().UTC())
	if err != nil || !ok {
		return
	}
	to := "done"
	if state != "ok" {
		to = "failed"
	}
	m.event(ctx, d.ID, "backup", "", to, reason, "")
	m.log.Info("base backup finished", "database", d.Name, "state", state, "reason", reason)
}

// onBackupStatus follows a backup task to its end, then removes it.
func (m *Manager) onBackupStatus(node store.Node, s *agentv1.TaskStatus) {
	ctx := context.Background()
	id := s.GetTaskId()
	d, err := m.st.DatabaseByID(ctx, backupDatabaseID(id))
	if err != nil {
		if s.GetState() != agentv1.TaskState_TASK_STATE_REMOVED {
			m.stopTask(node.ID, id) // its database is gone
		}
		return
	}
	switch s.GetState() {
	case agentv1.TaskState_TASK_STATE_EXITED:
		if s.GetExitCode() == 0 {
			m.finishBackup(ctx, d, id, "ok", "")
		} else {
			m.finishBackup(ctx, d, id, "failed", fmt.Sprintf("wal-g exited with code %d (see the database's logs)", s.GetExitCode()))
		}
		m.stopTask(node.ID, id)
	case agentv1.TaskState_TASK_STATE_FAILED:
		m.finishBackup(ctx, d, id, "failed", s.GetError())
		m.stopTask(node.ID, id)
	case agentv1.TaskState_TASK_STATE_REMOVED:
		m.finishBackup(ctx, d, id, "failed", "the task was removed before it finished")
	}
}

func (m *Manager) stopTask(nodeID, taskID string) {
	_ = m.gw.Send(nodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{TaskId: taskID, TimeoutSeconds: 10, Remove: true}}})
}

// ── status ──────────────────────────────────────────────────────────────────

// PgBaseBackup is one base backup in the archive.
type PgBaseBackup struct {
	Name             string    `json:"name"`
	StartTime        time.Time `json:"startTime"`
	FinishTime       time.Time `json:"finishTime"`
	StartLSN         string    `json:"startLsn"`
	FinishLSN        string    `json:"finishLsn"`
	CompressedSize   int64     `json:"compressedSize"`
	UncompressedSize int64     `json:"uncompressedSize"`
	Permanent        bool      `json:"permanent"`
}

// PgArchiver is the primary's pg_stat_archiver.
type PgArchiver struct {
	ArchivedCount int64      `json:"archivedCount"`
	LastArchived  string     `json:"lastArchived"`
	LastAt        *time.Time `json:"lastArchivedAt"`
	FailedCount   int64      `json:"failedCount"`
	LastFailed    string     `json:"lastFailed"`
	LastFailedAt  *time.Time `json:"lastFailedAt"`
}

// PgBackups is the backup status of a cluster.
type PgBackups struct {
	Configured bool           `json:"configured"`
	Config     *PgBackupSpec  `json:"config,omitempty"`
	Location   string         `json:"location,omitempty"` // s3://bucket/prefix
	Backups    []PgBaseBackup `json:"backups"`
	Runs       []BackupRun    `json:"runs"`
	Archiver   *PgArchiver    `json:"archiver,omitempty"`
	Window     *RestoreWindow `json:"window,omitempty"`
	Error      string         `json:"error,omitempty"` // the archive could not be read
	Restore    *RestoreState  `json:"restoredFrom,omitempty"`
	NextRun    *time.Time     `json:"nextRun,omitempty"`
}

// BackupRun is a base backup task as the API shows it.
type BackupRun struct {
	ID         string     `json:"id"`
	Member     string     `json:"member"`
	Trigger    string     `json:"trigger"`
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// RestoreWindow is the span a restore can target: from the oldest base
// backup's end to the newest archived WAL.
type RestoreWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

func s3Client(a S3Access) (*minio.Client, error) {
	u, err := url.Parse(a.URL)
	if err != nil {
		return nil, err
	}
	lookup := minio.BucketLookupAuto
	if a.PathStyle {
		lookup = minio.BucketLookupPath
	}
	return minio.New(u.Host, &minio.Options{
		Creds: credentials.NewStaticV4(a.AccessKeyID, a.SecretAccessKey, ""), Secure: u.Scheme == "https", Region: a.Region, BucketLookup: lookup,
	})
}

// sentinel is the part of WAL-G's *_backup_stop_sentinel.json we show.
type sentinel struct {
	StartTime        time.Time `json:"StartTime"`
	FinishTime       time.Time `json:"FinishTime"`
	LSN              uint64    `json:"LSN"`
	FinishLSN        uint64    `json:"FinishLSN"`
	CompressedSize   int64     `json:"CompressedSize"`
	UncompressedSize int64     `json:"UncompressedSize"`
	IsPermanent      bool      `json:"IsPermanent"`
}

func lsn(v uint64) string { return fmt.Sprintf("%X/%X", v>>32, v&0xffffffff) }

// listBackups reads the base backups in an archive from S3.
func (m *Manager) listBackups(ctx context.Context, endpoint, bucket, prefix string) ([]PgBaseBackup, *time.Time, error) {
	if m.S3 == nil {
		return nil, nil, errors.New("S3 endpoints are not available")
	}
	a, err := m.S3(ctx, endpoint)
	if err != nil {
		return nil, nil, err
	}
	cl, err := s3Client(a)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out []PgBaseBackup
	base := strings.Trim(prefix, "/") + "/basebackups_005/"
	for o := range cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: base}) {
		if o.Err != nil {
			return nil, nil, o.Err
		}
		name, ok := strings.CutSuffix(strings.TrimPrefix(o.Key, base), "_backup_stop_sentinel.json")
		if !ok || strings.Contains(name, "/") {
			continue
		}
		obj, err := cl.GetObject(ctx, bucket, o.Key, minio.GetObjectOptions{})
		if err != nil {
			return nil, nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(obj, 1<<20))
		obj.Close()
		if err != nil {
			return nil, nil, err
		}
		var s sentinel
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		out = append(out, PgBaseBackup{Name: name, StartTime: s.StartTime.UTC(), FinishTime: s.FinishTime.UTC(), StartLSN: lsn(s.LSN), FinishLSN: lsn(s.FinishLSN),
			CompressedSize: s.CompressedSize, UncompressedSize: s.UncompressedSize, Permanent: s.IsPermanent})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FinishTime.After(out[j].FinishTime) })
	// The newest WAL segment: names sort by timeline and position, so the
	// last key listed is the newest.
	var lastWAL *time.Time
	walPrefix := strings.Trim(prefix, "/") + "/wal_005/"
	var lastKey string
	for o := range cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: walPrefix}) {
		if o.Err != nil {
			break
		}
		if o.Key > lastKey && !strings.Contains(o.Key, ".history") && !strings.Contains(o.Key, ".partial") {
			lastKey = o.Key
			t := o.LastModified.UTC()
			lastWAL = &t
		}
	}
	return out, lastWAL, nil
}

// archiver reads pg_stat_archiver from the primary.
func (m *Manager) archiver(ctx context.Context, d store.Database) (*PgArchiver, error) {
	var a PgArchiver
	err := m.pgAdmin(ctx, d, "postgres", func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT archived_count, coalesce(last_archived_wal, ''), last_archived_time, failed_count,
			coalesce(last_failed_wal, ''), last_failed_time FROM pg_stat_archiver`).
			Scan(&a.ArchivedCount, &a.LastArchived, &a.LastAt, &a.FailedCount, &a.LastFailed, &a.LastFailedAt)
	})
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Backups reports a cluster's backups, runs, archiving and restore window.
func (m *Manager) Backups(ctx context.Context, d store.Database) (PgBackups, error) {
	out := PgBackups{Backups: []PgBaseBackup{}, Runs: []BackupRun{}}
	if d.Engine != EnginePostgres {
		return out, ErrInvalid{errors.New(d.Name + " is not a PostgreSQL database")}
	}
	spec, _ := parseSpec(d.Spec)
	st := parseState(d.State)
	out.Restore = st.Restore
	runs, err := m.st.DatabaseBackups(ctx, d.ID, 50)
	if err != nil {
		return out, err
	}
	for _, r := range runs {
		out.Runs = append(out.Runs, BackupRun{ID: r.ID, Member: r.Member, Trigger: r.Trigger, State: r.State, Error: r.Error, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt})
	}
	if spec.Postgres == nil || spec.Postgres.Backup == nil {
		return out, nil
	}
	b := spec.Postgres.Backup
	out.Configured, out.Config = true, b
	prefix := backupPrefix(d, b)
	out.Location = "s3://" + b.Bucket + "/" + prefix
	for _, r := range runs {
		if r.State == "ok" {
			next := r.StartedAt.Add(time.Duration(b.EveryHours) * time.Hour)
			out.NextRun = &next
			break
		}
	}
	backups, lastWAL, err := m.listBackups(ctx, b.Endpoint, b.Bucket, prefix)
	if err != nil {
		out.Error = err.Error()
	} else if backups != nil {
		out.Backups = backups
	}
	if a, err := m.archiver(ctx, d); err == nil {
		out.Archiver = a
		if a.LastAt != nil && (lastWAL == nil || a.LastAt.After(*lastWAL)) {
			lastWAL = a.LastAt
		}
	}
	if len(out.Backups) > 0 {
		oldest := out.Backups[len(out.Backups)-1].FinishTime
		to := out.Backups[0].FinishTime
		if lastWAL != nil && lastWAL.After(to) {
			to = *lastWAL
		}
		out.Window = &RestoreWindow{From: oldest, To: to}
	}
	return out, nil
}

// planRestore checks a restore request against the source's archive and
// picks the base backup to start from.
func (m *Manager) planRestore(ctx context.Context, r PgRestore) (store.Database, RestoreState, error) {
	src, err := m.st.DatabaseByName(ctx, r.From)
	if err != nil {
		return src, RestoreState{}, ErrInvalid{fmt.Errorf("no database %q to restore from", r.From)}
	}
	if src.Engine != EnginePostgres {
		return src, RestoreState{}, ErrInvalid{fmt.Errorf("%s is not a PostgreSQL database", src.Name)}
	}
	spec, _ := parseSpec(src.Spec)
	if spec.Postgres == nil || spec.Postgres.Backup == nil {
		return src, RestoreState{}, ErrInvalid{fmt.Errorf("%s has no backups configured", src.Name)}
	}
	status, err := m.Backups(ctx, src)
	if err != nil {
		return src, RestoreState{}, err
	}
	if status.Error != "" {
		return src, RestoreState{}, ErrInvalid{fmt.Errorf("the archive of %s cannot be read: %s", src.Name, status.Error)}
	}
	if len(status.Backups) == 0 {
		return src, RestoreState{}, ErrInvalid{fmt.Errorf("%s has no base backup yet; take one first", src.Name)}
	}
	b := spec.Postgres.Backup
	rs := RestoreState{Source: src.ID, SourceName: src.Name, Endpoint: b.Endpoint, Bucket: b.Bucket, Prefix: backupPrefix(src, b)}
	if r.TargetTime != nil {
		t := r.TargetTime.UTC()
		if w := status.Window; w != nil && (t.Before(w.From) || t.After(w.To)) {
			return src, rs, ErrInvalid{fmt.Errorf("the target time must be within the restore window, %s to %s",
				w.From.Format(time.RFC3339), w.To.Format(time.RFC3339))}
		}
		rs.TargetTime = &t
	}
	for _, bb := range status.Backups { // newest first
		if r.Backup != "" && bb.Name != r.Backup {
			continue
		}
		if rs.TargetTime != nil && bb.FinishTime.After(*rs.TargetTime) {
			if r.Backup != "" {
				return src, rs, ErrInvalid{fmt.Errorf("backup %s finished after the target time", bb.Name)}
			}
			continue
		}
		rs.Backup = bb.Name
		break
	}
	if rs.Backup == "" {
		if r.Backup != "" {
			return src, rs, ErrInvalid{fmt.Errorf("no backup %q in the archive of %s", r.Backup, src.Name)}
		}
		return src, rs, ErrInvalid{errors.New("no base backup finished before the target time")}
	}
	return src, rs, nil
}

// checkBackupEndpoint makes sure the backup endpoint exists.
func (m *Manager) checkBackupEndpoint(ctx context.Context, spec Spec) error {
	if spec.Postgres == nil || spec.Postgres.Backup == nil {
		return nil
	}
	if m.S3 == nil {
		return ErrInvalid{errors.New("S3 endpoints are not available")}
	}
	if _, err := m.S3(ctx, spec.Postgres.Backup.Endpoint); err != nil {
		return ErrInvalid{fmt.Errorf("postgres.backup.endpoint: no S3 endpoint %q", spec.Postgres.Backup.Endpoint)}
	}
	return nil
}

func backupSummary(s Spec) string {
	if s.Postgres == nil || s.Postgres.Backup == nil {
		return "off"
	}
	b := s.Postgres.Backup
	return fmt.Sprintf("s3://%s/%s every %dh, keep %d full and %d days", b.Bucket, b.Prefix, b.EveryHours, b.RetainFull, b.RetainDays)
}
