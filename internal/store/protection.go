package store

import (
	"context"
	"time"
)

// ProjectProtection is a project's stored traffic protection (Phase 25).
type ProjectProtection struct {
	ProjectID string
	Spec      string // JSON
	Secret    []byte // sealed bypass secret (nil = none)
	UpdatedAt time.Time
}

func (s *Store) PutProjectProtection(ctx context.Context, p ProjectProtection) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO project_protection (project_id, spec, secret, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (project_id) DO UPDATE SET spec = excluded.spec, secret = excluded.secret, updated_at = excluded.updated_at`,
		p.ProjectID, p.Spec, p.Secret, p.UpdatedAt.Unix())
	return err
}

func (s *Store) ListProjectProtection(ctx context.Context) ([]ProjectProtection, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT project_id, spec, secret, updated_at FROM project_protection`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectProtection
	for rows.Next() {
		var p ProjectProtection
		var at int64
		if err := rows.Scan(&p.ProjectID, &p.Spec, &p.Secret, &at); err != nil {
			return nil, err
		}
		p.UpdatedAt = time.Unix(at, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}
