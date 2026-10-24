package store

import (
	"context"
	"time"
)

// FirewallBlock is a source the host firewall drops (Phase 24).
type FirewallBlock struct {
	ID        string     `json:"id"`
	Source    string     `json:"source"`
	Node      string     `json:"node"` // "" = every node
	Reason    string     `json:"reason"`
	CreatedBy string     `json:"createdBy"`
	Auto      bool       `json:"auto"` // made by a guard (auto-ban)
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt"` // nil = until removed
}

// PutFirewallBlock adds a block, or replaces the one for the same source
// and node (a later block extends or shortens it).
func (s *Store) PutFirewallBlock(ctx context.Context, b FirewallBlock) error {
	var exp int64
	if b.ExpiresAt != nil {
		exp = b.ExpiresAt.Unix()
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO firewall_blocks (id, source, node, reason, created_by, auto, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, node) DO UPDATE SET reason = excluded.reason, created_by = excluded.created_by, auto = excluded.auto,
			created_at = excluded.created_at, expires_at = excluded.expires_at`,
		b.ID, b.Source, b.Node, b.Reason, b.CreatedBy, b.Auto, b.CreatedAt.Unix(), exp)
	return err
}

func (s *Store) ListFirewallBlocks(ctx context.Context) ([]FirewallBlock, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, source, node, reason, created_by, auto, created_at, expires_at FROM firewall_blocks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FirewallBlock{}
	for rows.Next() {
		var b FirewallBlock
		var at, exp int64
		if err := rows.Scan(&b.ID, &b.Source, &b.Node, &b.Reason, &b.CreatedBy, &b.Auto, &at, &exp); err != nil {
			return nil, err
		}
		b.CreatedAt = time.Unix(at, 0).UTC()
		if exp > 0 {
			t := time.Unix(exp, 0).UTC()
			b.ExpiresAt = &t
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteFirewallBlock(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM firewall_blocks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpiredFirewallBlocks removes blocks that ran out, reporting how many.
func (s *Store) DeleteExpiredFirewallBlocks(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM firewall_blocks WHERE expires_at > 0 AND expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
