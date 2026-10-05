package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Session struct {
	TokenHash  string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	IP         string
	UserAgent  string
}

func (s *Store) CreateSession(ctx context.Context, ss Session) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ss.TokenHash, ss.UserID, ss.CreatedAt.Unix(), ss.ExpiresAt.Unix(), ss.LastSeenAt.Unix(), ss.IP, ss.UserAgent)
	return err
}

// SessionByHash returns ErrNotFound for unknown or expired sessions.
func (s *Store) SessionByHash(ctx context.Context, hash string, now time.Time) (Session, error) {
	var ss Session
	var created, expires, seen int64
	err := s.R.QueryRowContext(ctx,
		`SELECT token_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent
		 FROM sessions WHERE token_hash = ? AND expires_at > ?`, hash, now.Unix()).
		Scan(&ss.TokenHash, &ss.UserID, &created, &expires, &seen, &ss.IP, &ss.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return ss, ErrNotFound
	}
	ss.CreatedAt, ss.ExpiresAt, ss.LastSeenAt = time.Unix(created, 0), time.Unix(expires, 0), time.Unix(seen, 0)
	return ss, err
}

// TouchSession extends a session's expiry (sliding expiration).
func (s *Store) TouchSession(ctx context.Context, hash string, seen, expires time.Time) error {
	_, err := s.W.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`,
		seen.Unix(), expires.Unix(), hash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
