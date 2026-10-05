package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Environment struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
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
	return tx.Commit()
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, description, created_at FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var at int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &at); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ProjectByName(ctx context.Context, name string) (Project, error) {
	var p Project
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT id, name, description, created_at FROM projects WHERE name = ?`, name).Scan(&p.ID, &p.Name, &p.Description, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.CreatedAt = time.Unix(at, 0).UTC()
	return p, err
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
	rows, err := s.R.QueryContext(ctx, `SELECT id, project_id, name, created_at FROM environments WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Environment
	for rows.Next() {
		var e Environment
		var at int64
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Name, &at); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EnvironmentByName(ctx context.Context, projectID, name string) (Environment, error) {
	var e Environment
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT id, project_id, name, created_at FROM environments WHERE project_id = ? AND name = ?`, projectID, name).
		Scan(&e.ID, &e.ProjectID, &e.Name, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	e.CreatedAt = time.Unix(at, 0).UTC()
	return e, err
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
	res, err := s.W.ExecContext(ctx, `UPDATE nodes SET schedulable = ? WHERE id = ?`, on, id)
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
	taken := map[int]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT vip_index FROM services WHERE vip_index IS NOT NULL
		UNION SELECT idx FROM ipam_released WHERE kind = 'vip' AND released_at > ?`, now.Add(-cooldown).Unix())
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			rows.Close()
			return 0, err
		}
		taken[i] = true
	}
	rows.Close()
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
