package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Nodes are the node names the project's workloads may run on (empty = any).
	Nodes     []string  `json:"nodes"`
	CreatedAt time.Time `json:"createdAt"`
}

type Environment struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"projectId"`
	Name      string            `json:"name"`
	SharedEnv map[string]string `json:"sharedEnv"`
	CreatedAt time.Time         `json:"createdAt"`
}

const envCols = `SELECT id, project_id, name, shared_env, created_at FROM environments`

func scanEnv(r scanner) (Environment, error) {
	var e Environment
	var shared string
	var at int64
	if err := r.Scan(&e.ID, &e.ProjectID, &e.Name, &shared, &at); err != nil {
		return e, err
	}
	e.CreatedAt = time.Unix(at, 0).UTC()
	err := json.Unmarshal([]byte(shared), &e.SharedEnv)
	return e, err
}

type Service struct {
	ID            string
	EnvironmentID string
	Project       string // names, joined in
	Environment   string
	Name          string
	Revision      int
	DesiredCount  int
	Deleting      bool
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type TaskDefinition struct {
	ServiceID string
	Revision  int
	Spec      string // JSON
	CreatedAt time.Time
	CreatedBy string
}

// Task states.
const (
	TaskPending  = "pending"
	TaskPulling  = "pulling"
	TaskStarting = "starting"
	TaskRunning  = "running"
	TaskExited   = "exited"
	TaskFailed   = "failed"
	TaskStopped  = "stopped"
	TaskLost     = "lost"
)

type Task struct {
	ID          string
	ServiceID   string
	Revision    int
	NodeID      string
	Desired     string // running | stopped
	State       string
	IP          string
	ContainerID string
	Health      string
	ExitCode    int
	Error       string
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	UpdatedAt   time.Time
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ── projects and environments ───────────────────────────────────────────────

// CreateProject creates a project with one environment, in one transaction.
func (s *Store) CreateProject(ctx context.Context, p Project, env Environment) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id, name, description, created_at) VALUES (?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.CreatedAt.Unix()); isUnique(err) {
		return ErrNameTaken
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO environments (id, project_id, name, created_at) VALUES (?, ?, ?, ?)`,
		env.ID, p.ID, env.Name, env.CreatedAt.Unix()); err != nil {
		return err
	}
	if err := createDefaultGroup(ctx, tx, "sg_"+randHex(8), p.ID, p.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, description, nodes, created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var at int64
		var nodes string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &nodes, &at); err != nil {
			return nil, err
		}
		p.Nodes = parseNodes(nodes)
		p.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ProjectByName(ctx context.Context, name string) (Project, error) {
	return s.project(ctx, `name = ?`, name)
}

func (s *Store) ProjectByID(ctx context.Context, id string) (Project, error) {
	return s.project(ctx, `id = ?`, id)
}

func (s *Store) project(ctx context.Context, where string, arg any) (Project, error) {
	var p Project
	var at int64
	var nodes string
	err := s.R.QueryRowContext(ctx, `SELECT id, name, description, nodes, created_at FROM projects WHERE `+where, arg).Scan(&p.ID, &p.Name, &p.Description, &nodes, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.Nodes = parseNodes(nodes)
	p.CreatedAt = time.Unix(at, 0).UTC()
	return p, err
}

// SetProjectNodes sets the nodes a project's workloads may run on (nil = any).
func (s *Store) SetProjectNodes(ctx context.Context, id string, nodes []string) error {
	if nodes == nil {
		nodes = []string{}
	}
	b, _ := json.Marshal(nodes)
	res, err := s.W.ExecContext(ctx, `UPDATE projects SET nodes = ? WHERE id = ?`, string(b), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func parseNodes(s string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// ErrNotEmpty means the resource still has children.
var ErrNotEmpty = errors.New("not empty")

// DeleteProject removes a project without services.
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM services sv JOIN environments e ON e.id = sv.environment_id WHERE e.project_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrNotEmpty
	}
	res, err := s.W.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateEnvironment(ctx context.Context, e Environment) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO environments (id, project_id, name, created_at) VALUES (?, ?, ?, ?)`,
		e.ID, e.ProjectID, e.Name, e.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) ListEnvironments(ctx context.Context, projectID string) ([]Environment, error) {
	rows, err := s.R.QueryContext(ctx, envCols+` WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Environment
	for rows.Next() {
		e, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EnvironmentByName(ctx context.Context, projectID, name string) (Environment, error) {
	e, err := scanEnv(s.R.QueryRowContext(ctx, envCols+` WHERE project_id = ? AND name = ?`, projectID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

func (s *Store) EnvironmentByID(ctx context.Context, id string) (Environment, error) {
	e, err := scanEnv(s.R.QueryRowContext(ctx, envCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// SetSharedEnv replaces an environment's shared variables.
func (s *Store) SetSharedEnv(ctx context.Context, envID string, vars map[string]string) error {
	if vars == nil {
		vars = map[string]string{}
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	res, err := s.W.ExecContext(ctx, `UPDATE environments SET shared_env = ? WHERE id = ?`, string(b), envID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteEnvironment(ctx context.Context, id string) error {
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM services WHERE environment_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrNotEmpty
	}
	_, err := s.W.ExecContext(ctx, `DELETE FROM environments WHERE id = ?`, id)
	return err
}

// ── services ────────────────────────────────────────────────────────────────

const serviceCols = `SELECT sv.id, sv.environment_id, p.name, e.name, sv.name, sv.revision, sv.desired_count, sv.deleting, sv.status, sv.created_at, sv.updated_at
	FROM services sv JOIN environments e ON e.id = sv.environment_id JOIN projects p ON p.id = e.project_id`

func scanService(r scanner) (Service, error) {
	var sv Service
	var created, updated int64
	err := r.Scan(&sv.ID, &sv.EnvironmentID, &sv.Project, &sv.Environment, &sv.Name, &sv.Revision, &sv.DesiredCount, &sv.Deleting, &sv.Status, &created, &updated)
	sv.CreatedAt, sv.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return sv, err
}

func (s *Store) queryServices(ctx context.Context, where string, args ...any) ([]Service, error) {
	rows, err := s.R.QueryContext(ctx, serviceCols+" "+where+" ORDER BY p.name, e.name, sv.name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		sv, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) ListServices(ctx context.Context) ([]Service, error) { return s.queryServices(ctx, "") }

func (s *Store) ListServicesIn(ctx context.Context, environmentID string) ([]Service, error) {
	return s.queryServices(ctx, "WHERE sv.environment_id = ?", environmentID)
}

func (s *Store) ServiceByID(ctx context.Context, id string) (Service, error) {
	sv, err := scanService(s.R.QueryRowContext(ctx, serviceCols+" WHERE sv.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return sv, ErrNotFound
	}
	return sv, err
}

func (s *Store) ServiceByName(ctx context.Context, environmentID, name string) (Service, error) {
	sv, err := scanService(s.R.QueryRowContext(ctx, serviceCols+" WHERE sv.environment_id = ? AND sv.name = ?", environmentID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return sv, ErrNotFound
	}
	return sv, err
}

// CreateService creates a service with its first revision.
func (s *Store) CreateService(ctx context.Context, sv Service, spec string, createdBy string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var dbs int // services and databases share DNS names
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM databases WHERE environment_id = ? AND name = ?`, sv.EnvironmentID, sv.Name).Scan(&dbs); err != nil {
		return err
	}
	if dbs > 0 {
		return ErrNameTaken
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO services (id, environment_id, name, revision, desired_count, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?, ?)`,
		sv.ID, sv.EnvironmentID, sv.Name, sv.DesiredCount, sv.CreatedAt.Unix(), sv.CreatedAt.Unix()); isUnique(err) {
		return ErrNameTaken
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_definitions (service_id, revision, spec, created_at, created_by) VALUES (?, 1, ?, ?, ?)`,
		sv.ID, spec, sv.CreatedAt.Unix(), createdBy); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateService records a new revision when spec differs from the current
// one, and sets the desired count. It returns the resulting revision.
func (s *Store) UpdateService(ctx context.Context, id, spec string, desired int, createdBy string, now time.Time) (int, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rev int
	var cur string
	if err := tx.QueryRowContext(ctx, `SELECT sv.revision, td.spec FROM services sv JOIN task_definitions td ON td.service_id = sv.id AND td.revision = sv.revision WHERE sv.id = ?`, id).Scan(&rev, &cur); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	if spec != "" && spec != cur {
		rev++
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_definitions (service_id, revision, spec, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			id, rev, spec, now.Unix(), createdBy); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE services SET revision = ?, desired_count = ?, updated_at = ? WHERE id = ?`, rev, desired, now.Unix(), id); err != nil {
		return 0, err
	}
	return rev, tx.Commit()
}

func (s *Store) SetServiceStatus(ctx context.Context, id, status string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE services SET status = ? WHERE id = ? AND status != ?`, status, id, status)
	return err
}

// MarkServiceDeleting scales the service to zero; the reconciler deletes it
// once its tasks are gone.
func (s *Store) MarkServiceDeleting(ctx context.Context, id string, now time.Time) error {
	res, err := s.W.ExecContext(ctx, `UPDATE services SET deleting = 1, desired_count = 0, updated_at = ? WHERE id = ?`, now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteService(ctx context.Context, id string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, id)
	return err
}

func (s *Store) TaskDefinition(ctx context.Context, serviceID string, revision int) (TaskDefinition, error) {
	td := TaskDefinition{ServiceID: serviceID, Revision: revision}
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT spec, created_at, created_by FROM task_definitions WHERE service_id = ? AND revision = ?`, serviceID, revision).
		Scan(&td.Spec, &at, &td.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return td, ErrNotFound
	}
	td.CreatedAt = time.Unix(at, 0).UTC()
	return td, err
}

func (s *Store) ListTaskDefinitions(ctx context.Context, serviceID string) ([]TaskDefinition, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT revision, spec, created_at, created_by FROM task_definitions WHERE service_id = ? ORDER BY revision DESC`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskDefinition
	for rows.Next() {
		td := TaskDefinition{ServiceID: serviceID}
		var at int64
		if err := rows.Scan(&td.Revision, &td.Spec, &at, &td.CreatedBy); err != nil {
			return nil, err
		}
		td.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, td)
	}
	return out, rows.Err()
}

// ── tasks ───────────────────────────────────────────────────────────────────

const taskCols = `SELECT id, service_id, revision, coalesce(node_id, ''), desired, state, ip, container_id, health, exit_code, error,
	created_at, started_at, finished_at, updated_at FROM tasks`

func scanTask(r scanner) (Task, error) {
	var t Task
	var created, updated int64
	var started, finished sql.NullInt64
	err := r.Scan(&t.ID, &t.ServiceID, &t.Revision, &t.NodeID, &t.Desired, &t.State, &t.IP, &t.ContainerID, &t.Health, &t.ExitCode, &t.Error,
		&created, &started, &finished, &updated)
	t.CreatedAt, t.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if started.Valid {
		v := time.Unix(started.Int64, 0).UTC()
		t.StartedAt = &v
	}
	if finished.Valid {
		v := time.Unix(finished.Int64, 0).UTC()
		t.FinishedAt = &v
	}
	return t, err
}

func (s *Store) queryTasks(ctx context.Context, where string, args ...any) ([]Task, error) {
	rows, err := s.R.QueryContext(ctx, taskCols+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ServiceTasks returns a service's tasks, newest first; history limits stopped ones.
func (s *Store) ServiceTasks(ctx context.Context, serviceID string, history int) ([]Task, error) {
	return s.queryTasks(ctx, `WHERE service_id = ? AND (desired = 'running' OR id IN (
		SELECT id FROM tasks WHERE service_id = ? AND desired = 'stopped' ORDER BY created_at DESC LIMIT ?)) ORDER BY created_at DESC`,
		serviceID, serviceID, history)
}

func (s *Store) NodeTasks(ctx context.Context, nodeID string) ([]Task, error) {
	return s.queryTasks(ctx, `WHERE node_id = ? AND (desired = 'running' OR state NOT IN ('stopped', 'lost', 'exited', 'failed')) ORDER BY created_at`, nodeID)
}

func (s *Store) ActiveTasks(ctx context.Context) ([]Task, error) {
	return s.queryTasks(ctx, `WHERE desired = 'running' OR state NOT IN ('stopped', 'lost', 'exited', 'failed') ORDER BY created_at`)
}

func (s *Store) TaskByID(ctx context.Context, id string) (Task, error) {
	t, err := scanTask(s.R.QueryRowContext(ctx, taskCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) CreateTask(ctx context.Context, t Task) error {
	var node any
	if t.NodeID != "" {
		node = t.NodeID
	}
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO tasks (id, service_id, revision, node_id, desired, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ServiceID, t.Revision, node, t.Desired, t.State, t.CreatedAt.Unix(), t.CreatedAt.Unix())
	return err
}

// UpdateTaskStatus applies an agent report.
func (s *Store) UpdateTaskStatus(ctx context.Context, t Task) error {
	var started, finished any
	if t.StartedAt != nil {
		started = t.StartedAt.Unix()
	}
	if t.FinishedAt != nil {
		finished = t.FinishedAt.Unix()
	}
	_, err := s.W.ExecContext(ctx,
		`UPDATE tasks SET state = ?, ip = ?, container_id = ?, health = ?, exit_code = ?, error = ?,
		   started_at = coalesce(?, started_at), finished_at = coalesce(?, finished_at), updated_at = ? WHERE id = ?`,
		t.State, t.IP, t.ContainerID, t.Health, t.ExitCode, t.Error, started, finished, t.UpdatedAt.Unix(), t.ID)
	return err
}

// SetTaskDesired marks a task to be stopped (or a stopped one's final state).
func (s *Store) SetTaskDesired(ctx context.Context, id, desired, state string, now time.Time) error {
	if state == "" {
		_, err := s.W.ExecContext(ctx, `UPDATE tasks SET desired = ?, updated_at = ? WHERE id = ?`, desired, now.Unix(), id)
		return err
	}
	_, err := s.W.ExecContext(ctx, `UPDATE tasks SET desired = ?, state = ?, finished_at = coalesce(finished_at, ?), updated_at = ? WHERE id = ?`,
		desired, state, now.Unix(), now.Unix(), id)
	return err
}

// PruneTasks deletes stopped tasks beyond keep per service.
func (s *Store) PruneTasks(ctx context.Context, serviceID string, keep int) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM tasks WHERE service_id = ? AND desired = 'stopped' AND state IN ('stopped', 'lost', 'exited', 'failed')
		AND id NOT IN (SELECT id FROM tasks WHERE service_id = ? AND desired = 'stopped' ORDER BY created_at DESC LIMIT ?)`, serviceID, serviceID, keep)
	return err
}

// ── node scheduling ─────────────────────────────────────────────────────────

func (s *Store) SetNodeSchedulable(ctx context.Context, id string, on bool) error {
	// Allowing tasks again also ends a drain.
	res, err := s.W.ExecContext(ctx, `UPDATE nodes SET schedulable = ?, draining = CASE WHEN ? THEN 0 ELSE draining END WHERE id = ?`, on, on, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureServiceVIP returns the service's VIP index, allocating the lowest
// free one on first use.
func (s *Store) EnsureServiceVIP(ctx context.Context, serviceID string, pool IndexPool, cooldown time.Duration, now time.Time) (int, error) {
	var cur sql.NullInt64
	if err := s.R.QueryRowContext(ctx, `SELECT vip_index FROM services WHERE id = ?`, serviceID).Scan(&cur); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	if cur.Valid {
		return int(cur.Int64), nil
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	taken, err := takenVIPs(ctx, tx, cooldown, now) // shared with database VIPs
	if err != nil {
		return 0, err
	}
	for i := pool.Min; i <= pool.Max; i++ {
		if taken[i] || (pool.Skip != nil && pool.Skip(i)) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET vip_index = ? WHERE id = ? AND vip_index IS NULL`, i, serviceID); err != nil {
			return 0, err
		}
		return i, tx.Commit()
	}
	return 0, ErrPoolExhausted
}

// ── custom domains ──────────────────────────────────────────────────────────

type Domain struct {
	ID        string    `json:"id"`
	ServiceID string    `json:"serviceId"`
	Host      string    `json:"host"`
	PortName  string    `json:"port"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) AddDomain(ctx context.Context, d Domain) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO domains (id, service_id, host, port_name, created_at) VALUES (?, ?, ?, ?, ?)`,
		d.ID, d.ServiceID, d.Host, d.PortName, d.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) DeleteDomain(ctx context.Context, serviceID, host string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM domains WHERE service_id = ? AND host = ?`, serviceID, host)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListDomains returns custom domains of one service ("" = all).
func (s *Store) ListDomains(ctx context.Context, serviceID string) ([]Domain, error) {
	q, args := `SELECT id, service_id, host, port_name, created_at FROM domains ORDER BY host`, []any{}
	if serviceID != "" {
		q, args = `SELECT id, service_id, host, port_name, created_at FROM domains WHERE service_id = ? ORDER BY host`, []any{serviceID}
	}
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var d Domain
		var at int64
		if err := rows.Scan(&d.ID, &d.ServiceID, &d.Host, &d.PortName, &at); err != nil {
			return nil, err
		}
		d.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── deployments ─────────────────────────────────────────────────────────────

// Deployment statuses.
const (
	DeployInProgress = "in_progress"
	DeploySucceeded  = "succeeded"
	DeployFailed     = "failed"
	DeployRolledBack = "rolled_back"
	DeploySuperseded = "superseded"
)

type Deployment struct {
	ID         string     `json:"id"`
	ServiceID  string     `json:"serviceId"`
	FromRev    int        `json:"fromRevision"`
	ToRev      int        `json:"toRevision"`
	Status     string     `json:"status"`
	Failed     int        `json:"failedTasks"`
	Message    string     `json:"message"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// StartDeployment supersedes any running deployment of the service and
// records a new one.
func (s *Store) StartDeployment(ctx context.Context, d Deployment) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE deployments SET status = ?, finished_at = ? WHERE service_id = ? AND status IN (?, ?)`,
		DeploySuperseded, d.StartedAt.Unix(), d.ServiceID, DeployInProgress, DeployWaitingHook); err != nil {
		return err
	}
	status := d.Status
	if status == "" {
		status = DeployInProgress
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments (id, service_id, from_rev, to_rev, status, message, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.ServiceID, d.FromRev, d.ToRev, status, d.Message, d.StartedAt.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

const deploymentCols = `SELECT id, service_id, from_rev, to_rev, status, failed, message, started_at, finished_at FROM deployments`

func scanDeployment(r scanner) (Deployment, error) {
	var d Deployment
	var started int64
	var finished sql.NullInt64
	err := r.Scan(&d.ID, &d.ServiceID, &d.FromRev, &d.ToRev, &d.Status, &d.Failed, &d.Message, &started, &finished)
	d.StartedAt = time.Unix(started, 0).UTC()
	if finished.Valid {
		t := time.Unix(finished.Int64, 0).UTC()
		d.FinishedAt = &t
	}
	return d, err
}

// ActiveDeployment returns the service's in-progress deployment.
func (s *Store) ActiveDeployment(ctx context.Context, serviceID string) (Deployment, error) {
	d, err := scanDeployment(s.R.QueryRowContext(ctx, deploymentCols+` WHERE service_id = ? AND status = ? ORDER BY started_at DESC LIMIT 1`, serviceID, DeployInProgress))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// ListDeployments returns a service's newest deployments, older than the
// deployment before names when set.
func (s *Store) ListDeployments(ctx context.Context, serviceID string, limit int, before string) ([]Deployment, error) {
	rows, err := s.R.QueryContext(ctx, deploymentCols+` WHERE service_id = ?
		AND (? = '' OR (started_at, rowid) < (SELECT started_at, rowid FROM deployments WHERE id = ?))
		ORDER BY started_at DESC, rowid DESC LIMIT ?`, serviceID, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// IncDeploymentFailures counts a failed task of the deployment's revision.
func (s *Store) IncDeploymentFailures(ctx context.Context, id string) (int, error) {
	var n int
	err := s.W.QueryRowContext(ctx, `UPDATE deployments SET failed = failed + 1 WHERE id = ? RETURNING failed`, id).Scan(&n)
	return n, err
}

func (s *Store) FinishDeployment(ctx context.Context, id, status, message string, now time.Time) error {
	// An empty message keeps the existing one (e.g. "automatic rollback: …").
	_, err := s.W.ExecContext(ctx, `UPDATE deployments SET status = ?, message = CASE WHEN ? = '' THEN message ELSE ? END, finished_at = ? WHERE id = ? AND status = ?`,
		status, message, message, now.Unix(), id, DeployInProgress)
	return err
}

// DrainNode stops new placements on the node and marks it draining.
func (s *Store) DrainNode(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `UPDATE nodes SET schedulable = 0, draining = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
