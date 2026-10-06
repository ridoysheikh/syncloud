package store

import (
	"context"
	"time"
)

type Middleware struct {
	ID         string
	ProjectID  string
	Project    string // name, joined in
	Name       string
	Type       string
	Config     string // JSON
	ServiceIDs []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s *Store) ListMiddlewares(ctx context.Context) ([]Middleware, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT m.id, m.project_id, p.name, m.name, m.type, m.config, m.created_at, m.updated_at
		FROM middlewares m JOIN projects p ON p.id = m.project_id ORDER BY p.name, m.name`)
	if err != nil {
		return nil, err
	}
	var out []Middleware
	for rows.Next() {
		var m Middleware
		var c, u int64
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Project, &m.Name, &m.Type, &m.Config, &c, &u); err != nil {
			rows.Close()
			return nil, err
		}
		m.CreatedAt, m.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	att, err := s.R.QueryContext(ctx, `SELECT middleware_id, service_id FROM middleware_services ORDER BY service_id`)
	if err != nil {
		return nil, err
	}
	defer att.Close()
	by := map[string][]string{}
	for att.Next() {
		var m, sv string
		if err := att.Scan(&m, &sv); err != nil {
			return nil, err
		}
		by[m] = append(by[m], sv)
	}
	for i := range out {
		out[i].ServiceIDs = by[out[i].ID]
	}
	return out, att.Err()
}

// PutMiddleware creates or updates a middleware and replaces its attachments.
func (s *Store) PutMiddleware(ctx context.Context, m Middleware) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO middlewares (id, project_id, name, type, config, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, type = excluded.type, config = excluded.config, updated_at = excluded.updated_at`,
		m.ID, m.ProjectID, m.Name, m.Type, m.Config, m.CreatedAt.Unix(), m.UpdatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM middleware_services WHERE middleware_id = ?`, m.ID); err != nil {
		return err
	}
	for _, sv := range m.ServiceIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO middleware_services (middleware_id, service_id) VALUES (?, ?)`, m.ID, sv); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteMiddleware(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM middlewares WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
