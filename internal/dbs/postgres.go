package dbs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

// PostgreSQL (Phase 13): every data member runs Patroni, which manages
// Postgres, elects the leader through the platform etcd and fails over on
// its own. The controller places members, publishes endpoints that follow
// the leader, and creates the app user and database.

const (
	PostgresPort  = 5432
	patroniPort   = 8008
	pgSuperuser   = "syncloud_admin"
	pgReplication = "replicator"
	pgAppUser     = "app"
	// pgDefaultVersion is the major version new databases get.
	pgDefaultVersion = "18"
	pgNamespace      = "/syncloud/pg/"
	minPgMemoryMiB   = 256
	defaultPgMemory  = 512
)

// pgVersions are the offered major versions, newest first.
var pgVersions = []string{"18", "17"}

// pgImage is the image of a major version's members.
func (m *Manager) pgImage(version string) string {
	return m.PostgresImages[version]
}

// PreloadLibraries are loaded by every member (changing them restarts Postgres).
var PreloadLibraries = []string{"pg_stat_statements", "timescaledb", "pg_cron", "pg_duckdb"}

// PostgresSpec holds the PostgreSQL-only settings.
type PostgresSpec struct {
	// Synchronous makes commits wait for one replica (no data loss on
	// failover; needs a replica).
	Synchronous bool `json:"synchronous"`
	// MaxConnections defaults from memory.
	MaxConnections int `json:"maxConnections"`
	// Backup turns on WAL archiving and base backups to S3 (Phase 13c).
	Backup *PgBackupSpec `json:"backup,omitempty"`
}

func (s *Spec) normalizePostgres() error {
	if s.Memory.Min == 0 {
		s.Memory.Min = defaultPgMemory
	}
	if s.Memory.Max == 0 {
		s.Memory.Max = s.Memory.Min
	}
	if s.Memory.Min < minPgMemoryMiB || s.Memory.Max > maxMemoryMiB || s.Memory.Min > s.Memory.Max {
		return fmt.Errorf("memory must satisfy %d ≤ min ≤ max ≤ %d MiB", minPgMemoryMiB, maxMemoryMiB)
	}
	if s.Replicas.Min < 0 || s.Replicas.Max > maxReplicas || s.Replicas.Min > s.Replicas.Max {
		return fmt.Errorf("replicas must satisfy 0 ≤ min ≤ max ≤ %d", maxReplicas)
	}
	if s.CPU == 0 {
		s.CPU = 0.5
	}
	if s.CPU < 0.05 || s.CPU > 64 {
		return errors.New("cpu must be between 0.05 and 64 cores")
	}
	s.Persistence, s.EvictionPolicy = "", "" // Valkey's
	p := s.Postgres
	if p.Synchronous && s.Replicas.Min < 1 {
		return errors.New("synchronous replication needs at least one replica (replicas.min ≥ 1)")
	}
	if p.MaxConnections == 0 {
		p.MaxConnections = defaultMaxConnections(s.Memory.Min)
	}
	if p.MaxConnections < 20 || p.MaxConnections > 5000 {
		return errors.New("postgres.maxConnections must be between 20 and 5000")
	}
	if p.Backup != nil {
		if err := p.Backup.normalize(); err != nil {
			return err
		}
	}
	if s.Autoscaling.CPUTarget == 0 {
		s.Autoscaling.CPUTarget = 60
	}
	if s.Autoscaling.CPUTarget < 5 || s.Autoscaling.CPUTarget > 95 {
		return errors.New("autoscaling.cpuTarget must be between 5 and 95 (%)")
	}
	return nil
}

// defaultMaxConnections leaves memory for shared buffers and work memory:
// about one connection per 8 MiB, between 50 and 500.
func defaultMaxConnections(memMiB int) int { return min(500, max(50, memMiB/8)) }

// PgDatabase is the app database's name: the cluster name as an
// identifier, or the source's for a restored cluster.
func PgDatabase(d store.Database) string {
	if st := parseState(d.State); st.Database != "" {
		return st.Database
	}
	return strings.ReplaceAll(d.Name, "-", "_")
}

// pgTuning derives memory settings from the container size.
func pgTuning(memMiB, maxConn int) map[string]string {
	work := max(4, memMiB/(maxConn*4))
	return map[string]string{
		"shared_buffers":       fmt.Sprintf("%dMB", max(32, memMiB/4)),
		"effective_cache_size": fmt.Sprintf("%dMB", max(64, memMiB*3/4)),
		"work_mem":             fmt.Sprintf("%dMB", work),
		"maintenance_work_mem": fmt.Sprintf("%dMB", max(16, min(2048, memMiB/16))),
		"max_connections":      fmt.Sprint(maxConn),
	}
}

func etcdUser(d store.Database) string { return "pg_" + strings.TrimPrefix(d.ID, "db_") }

// patroniConfig is a member's Patroni configuration (JSON is valid YAML).
func patroniConfig(d store.Database, spec Spec, st State, sec Secrets, mb store.DatabaseMember, etcdHosts []string) []byte {
	self := memberHost(d, mb.Kind, mb.Ordinal)
	params := map[string]any{
		"wal_level": "replica", "hot_standby": "on", "max_wal_senders": 10, "max_replication_slots": 10,
		"wal_keep_size": "256MB", "wal_log_hints": "on", "archive_mode": "on", "archive_command": "/bin/true",
		"shared_preload_libraries":    strings.Join(PreloadLibraries, ","),
		"cron.database_name":          PgDatabase(d),
		"timescaledb.telemetry_level": "off",
		"password_encryption":         "scram-sha-256",
	}
	for k, v := range pgTuning(st.MemoryMiB, spec.Postgres.MaxConnections) {
		params[k] = v
	}
	cfg := map[string]any{
		"scope": d.ID, "namespace": pgNamespace, "name": memberName(mb),
		"restapi": map[string]any{
			"listen": fmt.Sprintf("0.0.0.0:%d", patroniPort), "connect_address": fmt.Sprintf("%s:%d", self, patroniPort),
			"authentication": map[string]string{"username": adminUser, "password": sec.RestPassword},
		},
		"etcd3": map[string]any{"hosts": etcdHosts, "protocol": "http", "username": etcdUser(d), "password": sec.EtcdPassword},
		"bootstrap": map[string]any{
			"dcs": map[string]any{
				"ttl": 30, "loop_wait": 10, "retry_timeout": 10, "maximum_lag_on_failover": 1 << 20,
				"synchronous_mode": spec.Postgres.Synchronous,
				"postgresql":       map[string]any{"use_pg_rewind": true, "use_slots": true, "parameters": params},
			},
			"initdb": []any{map[string]string{"encoding": "UTF8"}, map[string]string{"locale": "C.UTF-8"}, "data-checksums"},
		},
		"postgresql": map[string]any{
			"listen": fmt.Sprintf("0.0.0.0:%d", PostgresPort), "connect_address": fmt.Sprintf("%s:%d", self, PostgresPort),
			"data_dir": "/data/pgdata", "bin_dir": "/usr/lib/postgresql/" + d.Version + "/bin",
			"parameters": map[string]any{"unix_socket_directories": "/tmp"},
			"authentication": map[string]any{
				"superuser":   map[string]string{"username": pgSuperuser, "password": sec.AdminPassword},
				"replication": map[string]string{"username": pgReplication, "password": sec.ReplicationPassword},
				"rewind":      map[string]string{"username": pgSuperuser, "password": sec.AdminPassword},
			},
			"pg_hba": []string{
				"local all all trust",
				"host replication " + pgReplication + " 0.0.0.0/0 scram-sha-256",
				"host all all 0.0.0.0/0 scram-sha-256",
			},
		},
		"tags": map[string]bool{"nofailover": false, "noloadbalance": false, "clonefrom": true},
	}
	archiveConfig(cfg, spec, st)
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return b
}

// pgTaskSpec is what the agent runs for a PostgreSQL member.
func pgTaskSpec(image string, d store.Database, spec Spec, st State, sec Secrets, mb store.DatabaseMember, etcdHosts, dns, search []string, walg map[string]string) *agentv1.TaskSpec {
	short := memberName(mb)
	ts := &agentv1.TaskSpec{
		TaskId: mb.ID, Image: image, NetworkMode: workload.Network,
		Restart:    agentv1.RestartPolicy_RESTART_POLICY_UNLESS_STOPPED,
		Mounts:     []*agentv1.Mount{{Type: agentv1.Mount_TYPE_VOLUME, Source: Volume(d, mb.Kind, mb.Ordinal), Target: "/data"}},
		DnsServers: dns, DnsSearch: search,
		NetworkAliases: []string{memberHost(d, mb.Kind, mb.Ordinal)},
		Command:        []string{"patroni"},
		Env: map[string]string{
			"PATRONI_CONFIG_B64": base64.StdEncoding.EncodeToString(patroniConfig(d, spec, st, sec, mb, etcdHosts)),
		},
		MemoryLimitBytes: int64(st.LimitMiB) << 20,
	}
	for k, v := range walg {
		ts.Env[k] = v
	}
	ts.Name = fmt.Sprintf("%s-%s-db-%s-%s", d.Project, d.Environment, d.Name, short)
	if d.Standalone() {
		ts.Name = fmt.Sprintf("db-%s-%s", d.Name, short)
	}
	ts.Labels = map[string]string{
		"syncloud.project": d.Project, "syncloud.environment": d.Environment, "syncloud.service": d.Name,
		"syncloud.service_id": d.ID, "syncloud.database": d.Name, "syncloud.db_member": short,
	}
	return ts
}

// ── probe ───────────────────────────────────────────────────────────────────

// patroniStatus is Patroni's GET /patroni.
type patroniStatus struct {
	State    string `json:"state"`
	Role     string `json:"role"` // primary | replica | standby_leader (master in old versions)
	Timeline int    `json:"timeline"`
	Xlog     struct {
		Location         int64 `json:"location"`
		ReceivedLocation int64 `json:"received_location"`
		ReplayedLocation int64 `json:"replayed_location"`
	} `json:"xlog"`
	Replication []struct {
		Name  string `json:"application_name"`
		State string `json:"state"`
		Sync  string `json:"sync_state"`
	} `json:"replication"`
	Pending bool `json:"pending_restart"`
}

var patroniHTTP = &http.Client{Timeout: 3 * time.Second}

func patroniGet(ctx context.Context, ip string) (patroniStatus, error) {
	var s patroniStatus
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://%s/patroni", addr(ip, patroniPort)), nil)
	res, err := patroniHTTP.Do(req)
	if err != nil {
		return s, err
	}
	defer res.Body.Close()
	// Patroni answers 503 on members that are not running yet, with a body.
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&s)
	return s, err
}

// probePostgres asks every member's Patroni for its role and position,
// follows the leader, and creates the app user and database once.
func (m *Manager) probePostgres(ctx context.Context, d store.Database, st State, sec Secrets, members []store.DatabaseMember) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	now := m.now()
	leader, leaderIP := -1, ""
	var leaderLoc int64
	status := map[string]patroniStatus{}
	for _, mb := range members {
		if mb.Kind != KindData || mb.IP == "" || mb.State != store.TaskRunning {
			continue
		}
		l := &Live{At: now}
		s, err := patroniGet(ctx, mb.IP)
		if err != nil {
			l.Error = err.Error()
			m.setLive(mb.ID, l)
			continue
		}
		status[mb.ID] = s
		switch s.Role {
		case "primary", "master":
			if s.State == "running" {
				leader, leaderIP, leaderLoc = mb.Ordinal, mb.IP, s.Xlog.Location
			}
			l.Role = "master"
		default:
			l.Role = "slave"
		}
		if s.State != "running" {
			l.Error = "postgres " + s.State
		}
		m.setLive(mb.ID, l)
	}
	for _, mb := range members {
		s, ok := status[mb.ID]
		if !ok || s.Role == "primary" || s.Role == "master" {
			continue
		}
		l := m.liveOf(mb.ID)
		if l == nil {
			continue
		}
		cp := *l
		cp.LinkUp = s.State == "running" && s.Xlog.ReceivedLocation > 0
		if leaderLoc > 0 {
			cp.LagBytes = max(0, leaderLoc-s.Xlog.ReplayedLocation)
		}
		m.setLive(mb.ID, &cp)
	}
	if leader >= 0 && leader != st.Primary {
		m.event(ctx, d.ID, "failover", fmt.Sprintf("m%d", st.Primary), fmt.Sprintf("m%d", leader), "Patroni elected a new leader", "patroni")
		st.Primary = leader
		_ = m.st.SetDatabaseState(ctx, d.ID, encode(st))
		m.publish(ctx, d.ID)
		m.changed()
	}
	if leader >= 0 && !st.Bootstrapped {
		if err := pgBootstrap(ctx, d, sec, leaderIP); err != nil {
			m.log.Warn("create the app user and database", "database", d.Name, "err", err)
			return
		}
		st.Bootstrapped = true
		_ = m.st.SetDatabaseState(ctx, d.ID, encode(st))
		m.event(ctx, d.ID, "created", "", "user "+pgAppUser+", database "+PgDatabase(d), "ready", "operator")
		m.publish(ctx, d.ID)
	}
}

// pgConnect opens an admin connection to a member.
func pgConnect(ctx context.Context, ip string, sec Secrets, database string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Database = ip, PostgresPort, pgSuperuser, sec.AdminPassword, database
	cfg.ConnectTimeout = 3 * time.Second
	return pgx.ConnectConfig(ctx, cfg)
}

// quoteLiteral quotes s as an SQL string literal.
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// pgBootstrap creates the app role and its database on a new cluster.
func pgBootstrap(ctx context.Context, d store.Database, sec Secrets, leaderIP string) error {
	c, err := pgConnect(ctx, leaderIP, sec, "postgres")
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	var n int
	if err := c.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, pgAppUser).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := c.Exec(ctx, `CREATE ROLE `+pgx.Identifier{pgAppUser}.Sanitize()+` LOGIN PASSWORD `+quoteLiteral(sec.Password)); err != nil {
			return err
		}
	}
	// Like the master user of a hosted database: it creates databases and
	// roles, and reads settings and statistics (pg_stat_statements for every
	// role); still not a superuser.
	if _, err := c.Exec(ctx, `ALTER ROLE `+pgx.Identifier{pgAppUser}.Sanitize()+` CREATEDB CREATEROLE`); err != nil {
		return err
	}
	if _, err := c.Exec(ctx, `GRANT pg_monitor TO `+pgx.Identifier{pgAppUser}.Sanitize()); err != nil {
		return err
	}
	db := PgDatabase(d)
	if err := c.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = $1`, db).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := c.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{db}.Sanitize()+` OWNER `+pgx.Identifier{pgAppUser}.Sanitize()); err != nil {
			return err
		}
	}
	return nil
}

// pgSwitchover asks the leader's Patroni to hand over to the best replica.
func (m *Manager) pgSwitchover(ctx context.Context, d store.Database, sec Secrets) error {
	st := parseState(d.State)
	members, err := m.st.DatabaseMembers(ctx, d.ID)
	if err != nil {
		return err
	}
	for _, mb := range members {
		if mb.Kind != KindData || mb.Ordinal != st.Primary || mb.IP == "" {
			continue
		}
		body, _ := json.Marshal(map[string]string{"leader": memberName(mb)})
		req, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://%s/switchover", addr(mb.IP, patroniPort)), bytes.NewReader(body))
		req.SetBasicAuth(adminUser, sec.RestPassword)
		req.Header.Set("Content-Type", "application/json")
		res, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("switchover: %s", strings.TrimSpace(string(msg)))
		}
		return nil
	}
	return errors.New("the leader is not running")
}

// pgHealth judges a PostgreSQL cluster: down without a leader, degraded
// while a member or replica stream is missing.
func pgHealth(d store.Database, st State, members []Member, created time.Time) string {
	data, running, streaming, leader := 0, 0, 0, false
	for _, x := range members {
		data++
		if x.State == store.TaskRunning {
			running++
		}
		switch x.Role {
		case "primary":
			leader = true
		case "replica":
			if x.LinkUp {
				streaming++
			}
		}
	}
	switch {
	case d.Deleting:
		return "deleting"
	case data == 0 || (!leader && time.Since(created) < 5*time.Minute && !st.Bootstrapped):
		return "starting"
	case !leader && running > 0 && !st.Bootstrapped:
		return "starting"
	case !leader:
		return "down"
	case running < 1+st.Replicas || streaming < st.Replicas || !st.Bootstrapped:
		return "degraded"
	}
	return "healthy"
}
