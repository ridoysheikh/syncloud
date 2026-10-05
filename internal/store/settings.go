package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Setting keys.
const (
	SettingSetupTokenHash    = "setup.token_hash"
	SettingSetupTokenExpires = "setup.token_expires"
	SettingBaseDomain        = "domain.base"
	// SettingRecoverySuffixHash lets the setup wizard confirm the user saved the
	// recovery key (they type its last 6 characters).
	SettingRecoverySuffixHash = "recovery.suffix_hash"
)

// GetSetting returns ("", false, nil) when the key is not set.
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	return setSetting(ctx, s.W, key, value)
}

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func setSetting(ctx context.Context, db execer, key, value string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().Unix())
	return err
}
