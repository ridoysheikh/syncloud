package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type LifecyclePolicy struct {
	Repository string
	Rules      string // JSON
	UpdatedAt  time.Time
	UpdatedBy  string
}

func (s *Store) PutLifecyclePolicy(ctx context.Context, p LifecyclePolicy) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO registry_lifecycle (repository, rules, updated_at, updated_by) VALUES (?, ?, ?, ?)
		ON CONFLICT(repository) DO UPDATE SET rules = excluded.rules, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		p.Repository, p.Rules, p.UpdatedAt.Unix(), p.UpdatedBy)
	return err
}

func (s *Store) LifecyclePolicy(ctx context.Context, repo string) (LifecyclePolicy, error) {
	var p LifecyclePolicy
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT repository, rules, updated_at, updated_by FROM registry_lifecycle WHERE repository = ?`, repo).
		Scan(&p.Repository, &p.Rules, &at, &p.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.UpdatedAt = time.Unix(at, 0).UTC()
	return p, err
}

func (s *Store) ListLifecyclePolicies(ctx context.Context) ([]LifecyclePolicy, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT repository, rules, updated_at, updated_by FROM registry_lifecycle ORDER BY repository`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LifecyclePolicy
	for rows.Next() {
		var p LifecyclePolicy
		var at int64
		if err := rows.Scan(&p.Repository, &p.Rules, &at, &p.UpdatedBy); err != nil {
			return nil, err
		}
		p.UpdatedAt = time.Unix(at, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteLifecyclePolicy(ctx context.Context, repo string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM registry_lifecycle WHERE repository = ?`, repo)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type GCRun struct {
	ID             string     `json:"id"`
	Trigger        string     `json:"trigger"`
	Status         string     `json:"status"`
	Expired        int        `json:"expired"`
	ReclaimedBytes int64      `json:"reclaimedBytes"`
	Message        string     `json:"message"`
	Details        string     `json:"-"`
	StartedAt      time.Time  `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
}

func (s *Store) CreateGCRun(ctx context.Context, r GCRun) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO registry_gc_runs (id, trigger, status, started_at) VALUES (?, ?, ?, ?)`,
		r.ID, r.Trigger, r.Status, r.StartedAt.Unix())
	return err
}

func (s *Store) FinishGCRun(ctx context.Context, r GCRun) error {
	_, err := s.W.ExecContext(ctx, `UPDATE registry_gc_runs SET status = ?, expired = ?, reclaimed_bytes = ?, message = ?, details = ?, finished_at = ? WHERE id = ?`,
		r.Status, r.Expired, r.ReclaimedBytes, r.Message, r.Details, unixPtr(r.FinishedAt), r.ID)
	return err
}

// GCRuns returns the latest runs, newest first.
func (s *Store) GCRuns(ctx context.Context, limit int) ([]GCRun, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, trigger, status, expired, reclaimed_bytes, message, details, started_at, finished_at
		FROM registry_gc_runs ORDER BY started_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GCRun
	for rows.Next() {
		var r GCRun
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Trigger, &r.Status, &r.Expired, &r.ReclaimedBytes, &r.Message, &r.Details, &started, &finished); err != nil {
			return nil, err
		}
		r.StartedAt, r.FinishedAt = time.Unix(started, 0).UTC(), timePtr(finished)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FailRunningGCRuns marks runs interrupted by a controller restart.
func (s *Store) FailRunningGCRuns(ctx context.Context, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE registry_gc_runs SET status = 'failed', message = 'interrupted by a controller restart', finished_at = ? WHERE status = 'running'`, now.Unix())
	return err
}
