package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GitConnection is a connected Git provider (§5.8).
type GitConnection struct {
	ID         string
	Kind       string // github-app | github | gitlab | gitea
	Name       string
	APIURL     string
	WebURL     string
	Account    string // the user or organization it acts as
	AppID      int64  // GitHub App
	AppSlug    string // GitHub App
	SecretsEnc []byte
	CreatedAt  time.Time
}

const gitConnCols = `SELECT id, kind, name, api_url, web_url, account, app_id, app_slug, secrets_enc, created_at FROM git_connections`

func scanGitConn(r scanner) (GitConnection, error) {
	var c GitConnection
	var created int64
	err := r.Scan(&c.ID, &c.Kind, &c.Name, &c.APIURL, &c.WebURL, &c.Account, &c.AppID, &c.AppSlug, &c.SecretsEnc, &created)
	c.CreatedAt = time.Unix(created, 0).UTC()
	return c, err
}

func (s *Store) CreateGitConnection(ctx context.Context, c GitConnection) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO git_connections (id, kind, name, api_url, web_url, account, app_id, app_slug, secrets_enc, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Kind, c.Name, c.APIURL, c.WebURL, c.Account, c.AppID, c.AppSlug, c.SecretsEnc, c.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) GitConnection(ctx context.Context, idOrName string) (GitConnection, error) {
	c, err := scanGitConn(s.R.QueryRowContext(ctx, gitConnCols+` WHERE id = ? OR name = ?`, idOrName, idOrName))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) ListGitConnections(ctx context.Context) ([]GitConnection, error) {
	rows, err := s.R.QueryContext(ctx, gitConnCols+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitConnection
	for rows.Next() {
		c, err := scanGitConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteGitConnection removes a connection no source uses.
func (s *Store) DeleteGitConnection(ctx context.Context, id string) error {
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM git_sources WHERE connection_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := s.W.ExecContext(ctx, `DELETE FROM git_connections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
