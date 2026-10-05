package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Node statuses (§5.6).
const (
	NodePending  = "pending" // joined, never connected
	NodeReady    = "ready"
	NodeSuspect  = "suspect"   // no heartbeat for 20s
	NodeNotReady = "not_ready" // no heartbeat for 60s
)

type Node struct {
	ID         string
	Name       string
	Status     string
	Info       string // JSON
	CertSerial string
	CreatedAt  time.Time
	StatusAt   time.Time
	LastSeenAt *time.Time
}

type JoinToken struct {
	ID          string
	TokenHash   string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	SingleUse   bool
	Uses        int
}

var ErrNameTaken = errors.New("name already taken")

func (s *Store) CreateJoinToken(ctx context.Context, t JoinToken) error {
	var by any
	if t.CreatedBy != "" {
		by = t.CreatedBy
	}
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO join_tokens (id, token_hash, description, created_by, created_at, expires_at, single_use) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.TokenHash, t.Description, by, t.CreatedAt.Unix(), t.ExpiresAt.Unix(), t.SingleUse)
	return err
}

func (s *Store) ListJoinTokens(ctx context.Context, now time.Time) ([]JoinToken, error) {
	rows, err := s.R.QueryContext(ctx,
		`SELECT id, token_hash, description, coalesce(created_by, ''), created_at, expires_at, single_use, uses
		 FROM join_tokens WHERE expires_at > ? AND NOT (single_use AND uses > 0) ORDER BY created_at`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JoinToken
	for rows.Next() {
		var t JoinToken
		var created, expires int64
		if err := rows.Scan(&t.ID, &t.TokenHash, &t.Description, &t.CreatedBy, &created, &expires, &t.SingleUse, &t.Uses); err != nil {
			return nil, err
		}
		t.CreatedAt, t.ExpiresAt = time.Unix(created, 0), time.Unix(expires, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteExpiredJoinTokens removes tokens that can no longer be used.
func (s *Store) DeleteExpiredJoinTokens(ctx context.Context, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM join_tokens WHERE expires_at <= ? OR (single_use AND uses > 0)`, now.Unix())
	return err
}

func (s *Store) DeleteJoinToken(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM join_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// JoinNode consumes a join token and creates the node in one transaction.
// It returns ErrNotFound if the token is unknown, expired or used up, and
// ErrNameTaken if the node name exists.
func (s *Store) JoinNode(ctx context.Context, tokenHash string, n Node, now time.Time) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE join_tokens SET uses = uses + 1
		 WHERE token_hash = ? AND expires_at > ? AND NOT (single_use AND uses > 0)`, tokenHash, now.Unix())
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM nodes WHERE name = ?`, n.Name).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrNameTaken
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO nodes (id, name, status, info, cert_serial, created_at, status_at) VALUES (?, ?, ?, '{}', ?, ?, ?)`,
		n.ID, n.Name, NodePending, n.CertSerial, n.CreatedAt.Unix(), n.CreatedAt.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) NodeByID(ctx context.Context, id string) (Node, error) {
	n, err := scanNode(s.R.QueryRowContext(ctx, nodeCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

func (s *Store) NodeByName(ctx context.Context, name string) (Node, error) {
	n, err := scanNode(s.R.QueryRowContext(ctx, nodeCols+` WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.R.QueryContext(ctx, nodeCols+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateNodeInfo stores the NodeInfo from an agent's Hello.
func (s *Store) UpdateNodeInfo(ctx context.Context, id, info string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE nodes SET info = ? WHERE id = ?`, info, id)
	return err
}

// SetNodeStatus records a status transition.
func (s *Store) SetNodeStatus(ctx context.Context, id, status string, at, lastSeen time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE nodes SET status = ?, status_at = ?, last_seen_at = ? WHERE id = ?`,
		status, at.Unix(), lastSeen.Unix(), id)
	return err
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const nodeCols = `SELECT id, name, status, info, cert_serial, created_at, status_at, last_seen_at FROM nodes`

func scanNode(r scanner) (Node, error) {
	var n Node
	var created, statusAt int64
	var seen sql.NullInt64
	err := r.Scan(&n.ID, &n.Name, &n.Status, &n.Info, &n.CertSerial, &created, &statusAt, &seen)
	n.CreatedAt, n.StatusAt, n.LastSeenAt = time.Unix(created, 0), time.Unix(statusAt, 0), nullTime(seen)
	return n, err
}
