package store

import (
	"context"
	"database/sql"
	"time"
)

// AddressRecord is one assignment of a container address (§8.2).
type AddressRecord struct {
	IP         string     `json:"ip"`
	OwnerID    string     `json:"ownerId"`
	Owner      string     `json:"owner"`
	NodeID     string     `json:"nodeId"`
	AssignedAt time.Time  `json:"assignedAt"`
	ReleasedAt *time.Time `json:"releasedAt"`
}

// AssignAddress records that ownerID now holds ip, closing any open record
// of that address or owner.
func (s *Store) AssignAddress(ctx context.Context, ip, ownerID, owner, nodeID string, at time.Time) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE ip_history SET released_at = ? WHERE released_at IS NULL AND (ip = ? OR owner_id = ?)`, at.Unix(), ip, ownerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ip_history (ip, owner_id, owner, node_id, assigned_at) VALUES (?, ?, ?, ?, ?)`,
		ip, ownerID, owner, nodeID, at.Unix()); err != nil {
		return err
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM ip_history WHERE released_at IS NOT NULL AND released_at < ?`, at.Add(-30*24*time.Hour).Unix())
	return tx.Commit()
}

// ReleaseAddress closes ownerID's open record.
func (s *Store) ReleaseAddress(ctx context.Context, ownerID string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE ip_history SET released_at = ? WHERE owner_id = ? AND released_at IS NULL`, at.Unix(), ownerID)
	return err
}

// AddressHistory lists assignments, newest first; ip filters when set.
func (s *Store) AddressHistory(ctx context.Context, ip string, limit int) ([]AddressRecord, error) {
	q := `SELECT ip, owner_id, owner, node_id, assigned_at, released_at FROM ip_history`
	args := []any{}
	if ip != "" {
		q += ` WHERE ip = ?`
		args = append(args, ip)
	}
	q += ` ORDER BY assigned_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AddressRecord{}
	for rows.Next() {
		var r AddressRecord
		var at int64
		var rel sql.NullInt64
		if err := rows.Scan(&r.IP, &r.OwnerID, &r.Owner, &r.NodeID, &at, &rel); err != nil {
			return nil, err
		}
		r.AssignedAt = time.Unix(at, 0).UTC()
		r.ReleasedAt = timePtr(rel)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Released is an address-pool index in its cool-down.
type Released struct {
	Kind       string    `json:"kind"` // mesh | subnet | vip
	Index      int       `json:"index"`
	ReleasedAt time.Time `json:"releasedAt"`
}

func (s *Store) ListReleased(ctx context.Context) ([]Released, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT kind, idx, released_at FROM ipam_released ORDER BY released_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Released{}
	for rows.Next() {
		var r Released
		var at int64
		if err := rows.Scan(&r.Kind, &r.Index, &at); err != nil {
			return nil, err
		}
		r.ReleasedAt = time.Unix(at, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
