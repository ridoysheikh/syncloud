package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Deployment status while a pre-deploy hook runs (§5.11).
const DeployWaitingHook = "waiting_hook"

// Job run statuses.
const (
	RunPending   = "pending"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunTimedOut  = "timed_out"
	RunCancelled = "cancelled"
	RunSkipped   = "skipped"
)

type Job struct {
	ID              string
	EnvironmentID   string
	Project         string
	Environment     string
	Name            string
	Spec            string
	LastScheduledAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type JobRun struct {
	ID            string
	JobID         string
	EnvironmentID string
	ServiceID     string
	Revision      int
	Trigger       string
	Attempt       int
	Spec          string
	Status        string
	NodeID        string
	ExitCode      *int
	Message       string
	DeploymentID  string
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func unixPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}

const jobCols = `SELECT j.id, j.environment_id, p.name, e.name, j.name, j.spec, j.last_scheduled_at, j.created_at, j.updated_at
	FROM jobs j JOIN environments e ON e.id = j.environment_id JOIN projects p ON p.id = e.project_id`

func scanJob(r scanner) (Job, error) {
	var j Job
	var last sql.NullInt64
	var created, updated int64
	err := r.Scan(&j.ID, &j.EnvironmentID, &j.Project, &j.Environment, &j.Name, &j.Spec, &last, &created, &updated)
	j.LastScheduledAt, j.CreatedAt, j.UpdatedAt = timePtr(last), time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return j, err
}

func (s *Store) queryJobs(ctx context.Context, where string, args ...any) ([]Job, error) {
	rows, err := s.R.QueryContext(ctx, jobCols+" "+where+" ORDER BY p.name, e.name, j.name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) ListJobs(ctx context.Context) ([]Job, error) { return s.queryJobs(ctx, "") }

func (s *Store) ListJobsIn(ctx context.Context, environmentID string) ([]Job, error) {
	return s.queryJobs(ctx, "WHERE j.environment_id = ?", environmentID)
}

func (s *Store) JobByName(ctx context.Context, environmentID, name string) (Job, error) {
	j, err := scanJob(s.R.QueryRowContext(ctx, jobCols+" WHERE j.environment_id = ? AND j.name = ?", environmentID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

func (s *Store) JobByID(ctx context.Context, id string) (Job, error) {
	j, err := scanJob(s.R.QueryRowContext(ctx, jobCols+" WHERE j.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// PutJob creates or updates a job (by environment and name).
func (s *Store) PutJob(ctx context.Context, j Job) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO jobs (id, environment_id, name, spec, last_scheduled_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(environment_id, name) DO UPDATE SET spec = excluded.spec, updated_at = excluded.updated_at`,
		j.ID, j.EnvironmentID, j.Name, j.Spec, unixPtr(j.LastScheduledAt), j.CreatedAt.Unix(), j.UpdatedAt.Unix())
	return err
}

func (s *Store) DeleteJob(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetJobScheduled(ctx context.Context, id string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE jobs SET last_scheduled_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

const runCols = `SELECT id, coalesce(job_id, ''), environment_id, coalesce(service_id, ''), revision, trigger, attempt, spec, status,
	coalesce(node_id, ''), exit_code, message, coalesce(deployment_id, ''), created_at, started_at, finished_at FROM job_runs`

func scanRun(r scanner) (JobRun, error) {
	var x JobRun
	var exit, started, finished sql.NullInt64
	var created int64
	err := r.Scan(&x.ID, &x.JobID, &x.EnvironmentID, &x.ServiceID, &x.Revision, &x.Trigger, &x.Attempt, &x.Spec, &x.Status,
		&x.NodeID, &exit, &x.Message, &x.DeploymentID, &created, &started, &finished)
	if exit.Valid {
		c := int(exit.Int64)
		x.ExitCode = &c
	}
	x.CreatedAt, x.StartedAt, x.FinishedAt = time.Unix(created, 0).UTC(), timePtr(started), timePtr(finished)
	return x, err
}

func (s *Store) queryRuns(ctx context.Context, where string, args ...any) ([]JobRun, error) {
	rows, err := s.R.QueryContext(ctx, runCols+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRun
	for rows.Next() {
		x, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) CreateRun(ctx context.Context, x JobRun) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO job_runs (id, job_id, environment_id, service_id, revision, trigger, attempt, spec, status, node_id, message, deployment_id, created_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, nullStr(x.JobID), x.EnvironmentID, nullStr(x.ServiceID), x.Revision, x.Trigger, x.Attempt, x.Spec, x.Status,
		nullStr(x.NodeID), x.Message, nullStr(x.DeploymentID), x.CreatedAt.Unix(), unixPtr(x.FinishedAt))
	return err
}

// UpdateRun writes status fields.
func (s *Store) UpdateRun(ctx context.Context, x JobRun) error {
	var exit any
	if x.ExitCode != nil {
		exit = *x.ExitCode
	}
	_, err := s.W.ExecContext(ctx,
		`UPDATE job_runs SET status = ?, node_id = ?, exit_code = ?, message = ?, started_at = ?, finished_at = ? WHERE id = ?`,
		x.Status, nullStr(x.NodeID), exit, x.Message, unixPtr(x.StartedAt), unixPtr(x.FinishedAt), x.ID)
	return err
}

func (s *Store) RunByID(ctx context.Context, id string) (JobRun, error) {
	x, err := scanRun(s.R.QueryRowContext(ctx, runCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// ActiveRuns returns pending and running runs.
func (s *Store) ActiveRuns(ctx context.Context) ([]JobRun, error) {
	return s.queryRuns(ctx, `WHERE status IN ('pending', 'running') ORDER BY created_at`)
}

// JobRuns returns a job's newest runs, older than the run before names
// when set.
func (s *Store) JobRuns(ctx context.Context, jobID string, limit int, before string) ([]JobRun, error) {
	return s.queryRuns(ctx, `WHERE job_id = ? AND (? = '' OR (created_at, rowid) < (SELECT created_at, rowid FROM job_runs WHERE id = ?))
		ORDER BY created_at DESC, rowid DESC LIMIT ?`, jobID, before, before, limit)
}

func (s *Store) RunsIn(ctx context.Context, environmentID string, limit int) ([]JobRun, error) {
	return s.queryRuns(ctx, `WHERE environment_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, environmentID, limit)
}

// PruneRuns keeps the newest keep finished runs of a job.
func (s *Store) PruneRuns(ctx context.Context, jobID string, keep int) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM job_runs WHERE job_id = ? AND status NOT IN ('pending', 'running')
		AND id NOT IN (SELECT id FROM job_runs WHERE job_id = ? ORDER BY created_at DESC LIMIT ?)`, jobID, jobID, keep)
	return err
}

// ── revision switching for pre-deploy hooks ─────────────────────────────────

// AddRevision stores spec as a new revision without making it current. It
// returns the current revision when spec equals it (nothing to deploy).
func (s *Store) AddRevision(ctx context.Context, serviceID, spec, createdBy string, now time.Time) (rev int, changed bool, err error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var cur int
	var curSpec string
	if err := tx.QueryRowContext(ctx, `SELECT sv.revision, td.spec FROM services sv JOIN task_definitions td ON td.service_id = sv.id AND td.revision = sv.revision WHERE sv.id = ?`, serviceID).Scan(&cur, &curSpec); errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrNotFound
	} else if err != nil {
		return 0, false, err
	}
	if spec == curSpec {
		return cur, false, nil
	}
	var max int
	if err := tx.QueryRowContext(ctx, `SELECT max(revision) FROM task_definitions WHERE service_id = ?`, serviceID).Scan(&max); err != nil {
		return 0, false, err
	}
	rev = max + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_definitions (service_id, revision, spec, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
		serviceID, rev, spec, now.Unix(), createdBy); err != nil {
		return 0, false, err
	}
	return rev, true, tx.Commit()
}

// SetServiceRevision makes rev current.
func (s *Store) SetServiceRevision(ctx context.Context, serviceID string, rev int, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE services SET revision = ?, updated_at = ? WHERE id = ?`, rev, now.Unix(), serviceID)
	return err
}

// SetDeploymentStatus moves a deployment to status (e.g. from waiting_hook).
func (s *Store) SetDeploymentStatus(ctx context.Context, id, status, message string, finished *time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE deployments SET status = ?, message = CASE WHEN ? = '' THEN message ELSE ? END, finished_at = ? WHERE id = ?`,
		status, message, message, unixPtr(finished), id)
	return err
}

func (s *Store) DeploymentByID(ctx context.Context, id string) (Deployment, error) {
	d, err := scanDeployment(s.R.QueryRowContext(ctx, deploymentCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// ── incidents ───────────────────────────────────────────────────────────────

type Incident struct {
	ID        string     `json:"id"`
	ServiceID string     `json:"serviceId"`
	State     string     `json:"state"`
	Cause     string     `json:"cause"`
	OpenedAt  time.Time  `json:"openedAt"`
	ClosedAt  *time.Time `json:"closedAt"`
}

func (s *Store) OpenIncident(ctx context.Context, i Incident) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO incidents (id, service_id, state, cause, opened_at) VALUES (?, ?, ?, ?, ?)`,
		i.ID, i.ServiceID, i.State, i.Cause, i.OpenedAt.Unix())
	return err
}

func (s *Store) UpdateIncident(ctx context.Context, id, state, cause string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE incidents SET state = ?, cause = ? WHERE id = ?`, state, cause, id)
	return err
}

func (s *Store) CloseIncident(ctx context.Context, id string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE incidents SET closed_at = ? WHERE id = ? AND closed_at IS NULL`, at.Unix(), id)
	return err
}

// ListIncidents returns incidents, newest first (open only when openOnly).
func (s *Store) ListIncidents(ctx context.Context, serviceID string, openOnly bool, limit int) ([]Incident, error) {
	q := `SELECT id, service_id, state, cause, opened_at, closed_at FROM incidents WHERE 1 = 1`
	var args []any
	if serviceID != "" {
		q += ` AND service_id = ?`
		args = append(args, serviceID)
	}
	if openOnly {
		q += ` AND closed_at IS NULL`
	}
	q += ` ORDER BY opened_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var i Incident
		var opened int64
		var closed sql.NullInt64
		if err := rows.Scan(&i.ID, &i.ServiceID, &i.State, &i.Cause, &opened, &closed); err != nil {
			return nil, err
		}
		i.OpenedAt, i.ClosedAt = time.Unix(opened, 0).UTC(), timePtr(closed)
		out = append(out, i)
	}
	return out, rows.Err()
}
