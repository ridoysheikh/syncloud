package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DatabaseBackup is one base backup run of a PostgreSQL cluster (Phase 13c).
type DatabaseBackup struct {
	ID         string // the task ID
	DatabaseID string
	NodeID     string
	Member     string
	Trigger    string // schedule | manual
	State      string // running | ok | failed
	Error      string
	StartedAt  time.Time
	FinishedAt *time.Time
}

const backupCols = `SELECT id, database_id, node_id, member, trigger, state, error, started_at, finished_at FROM database_backups`

func scanBackup(r scanner) (DatabaseBackup, error) {
	var b DatabaseBackup
	var started int64
	var finished sql.NullInt64
	err := r.Scan(&b.ID, &b.DatabaseID, &b.NodeID, &b.Member, &b.Trigger, &b.State, &b.Error, &started, &finished)
	b.StartedAt = time.Unix(started, 0).UTC()
	if finished.Valid {
		t := time.Unix(finished.Int64, 0).UTC()
		b.FinishedAt = &t
	}
	return b, err
}

// AddDatabaseBackup records a run that has started, keeping the newest 200.
func (s *Store) AddDatabaseBackup(ctx context.Context, b DatabaseBackup) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO database_backups (id, database_id, node_id, member, trigger, state, started_at) VALUES (?, ?, ?, ?, ?, 'running', ?)`,
		b.ID, b.DatabaseID, b.NodeID, b.Member, b.Trigger, b.StartedAt.Unix())
	if err != nil {
		return err
	}
	_, err = s.W.ExecContext(ctx, `DELETE FROM database_backups WHERE database_id = ? AND id NOT IN
		(SELECT id FROM database_backups WHERE database_id = ? ORDER BY started_at DESC LIMIT 200)`, b.DatabaseID, b.DatabaseID)
	return err
}

// FinishDatabaseBackup ends a running run (no-op when it already ended).
func (s *Store) FinishDatabaseBackup(ctx context.Context, id, state, errText string, at time.Time) (bool, error) {
	res, err := s.W.ExecContext(ctx, `UPDATE database_backups SET state = ?, error = ?, finished_at = ? WHERE id = ? AND state = 'running'`,
		state, errText, at.Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DatabaseBackupByID returns one run.
func (s *Store) DatabaseBackupByID(ctx context.Context, id string) (DatabaseBackup, error) {
	b, err := scanBackup(s.R.QueryRowContext(ctx, backupCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// DatabaseBackups lists a cluster's runs, newest first.
func (s *Store) DatabaseBackups(ctx context.Context, databaseID string, limit int) ([]DatabaseBackup, error) {
	rows, err := s.R.QueryContext(ctx, backupCols+` WHERE database_id = ? ORDER BY started_at DESC LIMIT ?`, databaseID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseBackup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
