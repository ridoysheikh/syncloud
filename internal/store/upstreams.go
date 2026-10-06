package store

import (
	"context"
	"time"
)

type UpstreamCredential struct {
	ID          string    `json:"id"`
	Host        string    `json:"host"`
	Username    string    `json:"username"`
	PasswordEnc []byte    `json:"-"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// PutUpstreamCredential creates or replaces the credential for c.Host.
func (s *Store) PutUpstreamCredential(ctx context.Context, c UpstreamCredential) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO upstream_credentials (id, host, username, password_enc, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(host) DO UPDATE SET username = excluded.username, password_enc = excluded.password_enc, updated_at = excluded.updated_at`,
		c.ID, c.Host, c.Username, c.PasswordEnc, c.CreatedAt.Unix(), c.UpdatedAt.Unix())
	return err
}

func (s *Store) ListUpstreamCredentials(ctx context.Context) ([]UpstreamCredential, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, host, username, password_enc, created_at, updated_at FROM upstream_credentials ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpstreamCredential
	for rows.Next() {
		var c UpstreamCredential
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Host, &c.Username, &c.PasswordEnc, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt, c.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUpstreamCredential(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM upstream_credentials WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
