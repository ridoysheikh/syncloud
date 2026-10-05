package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MaxAccessKeysPerUser allows rotation: create the new key, switch, delete the old one (§7.1).
const MaxAccessKeysPerUser = 2

var ErrLimitReached = errors.New("limit reached")

type AccessKey struct {
	ID          string
	UserID      string
	SecretEnc   []byte
	Description string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	LastUsedIP  string
}

// CreateAccessKey enforces the per-user limit inside the write transaction.
func (s *Store) CreateAccessKey(ctx context.Context, k AccessKey) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM access_keys WHERE user_id = ?`, k.UserID).Scan(&n); err != nil {
		return err
	}
	if n >= MaxAccessKeysPerUser {
		return ErrLimitReached
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO access_keys (id, user_id, secret_enc, description, created_at) VALUES (?, ?, ?, ?, ?)`,
		k.ID, k.UserID, k.SecretEnc, k.Description, k.CreatedAt.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AccessKeyByID(ctx context.Context, id string) (AccessKey, error) {
	row := s.R.QueryRowContext(ctx,
		`SELECT id, user_id, secret_enc, description, created_at, last_used_at, coalesce(last_used_ip, '') FROM access_keys WHERE id = ?`, id)
	k, err := scanAccessKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

func (s *Store) ListAccessKeys(ctx context.Context, userID string) ([]AccessKey, error) {
	rows, err := s.R.QueryContext(ctx,
		`SELECT id, user_id, secret_enc, description, created_at, last_used_at, coalesce(last_used_ip, '')
		 FROM access_keys WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessKey
	for rows.Next() {
		k, err := scanAccessKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAccessKey deletes a key owned by userID. It returns ErrNotFound otherwise.
func (s *Store) DeleteAccessKey(ctx context.Context, userID, id string) error {
	return deleteOwned(ctx, s.W, `DELETE FROM access_keys WHERE id = ? AND user_id = ?`, id, userID)
}

func (s *Store) TouchAccessKey(ctx context.Context, id, ip string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE access_keys SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, at.Unix(), ip, id)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scanAccessKey(r scanner) (AccessKey, error) {
	var k AccessKey
	var created int64
	var used sql.NullInt64
	err := r.Scan(&k.ID, &k.UserID, &k.SecretEnc, &k.Description, &created, &used, &k.LastUsedIP)
	k.CreatedAt = time.Unix(created, 0)
	k.LastUsedAt = nullTime(used)
	return k, err
}

type APIToken struct {
	ID         string
	UserID     string
	Name       string
	TokenHash  string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	LastUsedIP string
}

func (s *Store) CreateAPIToken(ctx context.Context, t APIToken) error {
	var exp any
	if t.ExpiresAt != nil {
		exp = t.ExpiresAt.Unix()
	}
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO api_tokens (id, user_id, name, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Name, t.TokenHash, t.CreatedAt.Unix(), exp)
	return err
}

// APITokenByHash returns ErrNotFound for unknown or expired tokens.
func (s *Store) APITokenByHash(ctx context.Context, hash string, now time.Time) (APIToken, error) {
	row := s.R.QueryRowContext(ctx,
		`SELECT id, user_id, name, token_hash, created_at, expires_at, last_used_at, coalesce(last_used_ip, '')
		 FROM api_tokens WHERE token_hash = ? AND (expires_at IS NULL OR expires_at > ?)`, hash, now.Unix())
	t, err := scanAPIToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.R.QueryContext(ctx,
		`SELECT id, user_id, name, token_hash, created_at, expires_at, last_used_at, coalesce(last_used_ip, '')
		 FROM api_tokens WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIToken(ctx context.Context, userID, id string) error {
	return deleteOwned(ctx, s.W, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
}

func (s *Store) TouchAPIToken(ctx context.Context, id, ip string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, at.Unix(), ip, id)
	return err
}

func scanAPIToken(r scanner) (APIToken, error) {
	var t APIToken
	var created int64
	var exp, used sql.NullInt64
	err := r.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &created, &exp, &used, &t.LastUsedIP)
	t.CreatedAt = time.Unix(created, 0)
	t.ExpiresAt = nullTime(exp)
	t.LastUsedAt = nullTime(used)
	return t, err
}

func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

func deleteOwned(ctx context.Context, db *sql.DB, q, id, userID string) error {
	res, err := db.ExecContext(ctx, q, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
