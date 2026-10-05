package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	IsRoot       bool
	CreatedAt    time.Time
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateRootUser creates the first account and consumes the setup token in one
// transaction, so two concurrent setup requests cannot both succeed.
func (s *Store) CreateRootUser(ctx context.Context, u User) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrSetupDone
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, email, name, password_hash, is_root, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
		u.ID, u.Email, u.Name, u.PasswordHash, u.CreatedAt.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key IN (?, ?)`,
		SettingSetupTokenHash, SettingSetupTokenExpires); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrSetupDone = errors.New("setup already completed")

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.R.QueryRowContext(ctx,
		`SELECT id, email, name, password_hash, is_root, created_at FROM users WHERE email = ?`, email))
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.R.QueryRowContext(ctx,
		`SELECT id, email, name, password_hash, is_root, created_at FROM users WHERE id = ?`, id))
}

func scanUser(row *sql.Row) (User, error) {
	var u User
	var created int64
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsRoot, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, err
}
