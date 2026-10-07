package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DatabaseOrphan is a removed database member whose container and volume
// still have to be removed from its node.
type DatabaseOrphan struct {
	TaskID    string
	NodeID    string
	Volume    string
	CreatedAt time.Time
}

// AddDatabaseOrphan records a member volume to remove (idempotent).
func (s *Store) AddDatabaseOrphan(ctx context.Context, o DatabaseOrphan) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO database_orphans (task_id, node_id, volume, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (task_id) DO NOTHING`, o.TaskID, o.NodeID, o.Volume, o.CreatedAt.Unix())
	return err
}

// DatabaseOrphanByTask returns the orphan of a task.
func (s *Store) DatabaseOrphanByTask(ctx context.Context, taskID string) (DatabaseOrphan, error) {
	var o DatabaseOrphan
	var created int64
	err := s.R.QueryRowContext(ctx, `SELECT task_id, node_id, volume, created_at FROM database_orphans WHERE task_id = ?`, taskID).
		Scan(&o.TaskID, &o.NodeID, &o.Volume, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	o.CreatedAt = time.Unix(created, 0).UTC()
	return o, err
}

// NodeDatabaseOrphans lists a node's orphans.
func (s *Store) NodeDatabaseOrphans(ctx context.Context, nodeID string) ([]DatabaseOrphan, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT task_id, node_id, volume, created_at FROM database_orphans WHERE node_id = ? ORDER BY task_id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseOrphan
	for rows.Next() {
		var o DatabaseOrphan
		var created int64
		if err := rows.Scan(&o.TaskID, &o.NodeID, &o.Volume, &created); err != nil {
			return nil, err
		}
		o.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteDatabaseOrphan forgets an orphan once its node removed it.
func (s *Store) DeleteDatabaseOrphan(ctx context.Context, taskID string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM database_orphans WHERE task_id = ?`, taskID)
	return err
}
