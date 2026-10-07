package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// EtcdMember is one member of the platform etcd (Phase 13).
type EtcdMember struct {
	ID         string
	Ordinal    int
	NodeID     string
	State      string // pending | running | exited | failed | stopped
	IP         string
	Error      string
	SpecHash   string
	Cluster    string // the initial-cluster it was started with
	NewCluster bool   // started with initial-cluster-state new
	MemberID   string // etcd's member ID (hex), once known
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const etcdCols = `SELECT id, ordinal, node_id, state, ip, error, spec_hash, cluster, new_cluster, member_id, created_at, updated_at FROM etcd_members`

func scanEtcd(r scanner) (EtcdMember, error) {
	var m EtcdMember
	var created, updated int64
	err := r.Scan(&m.ID, &m.Ordinal, &m.NodeID, &m.State, &m.IP, &m.Error, &m.SpecHash, &m.Cluster, &m.NewCluster, &m.MemberID, &created, &updated)
	m.CreatedAt, m.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return m, err
}

// EtcdMembers lists the members by ordinal.
func (s *Store) EtcdMembers(ctx context.Context) ([]EtcdMember, error) {
	rows, err := s.R.QueryContext(ctx, etcdCols+` ORDER BY ordinal`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EtcdMember
	for rows.Next() {
		m, err := scanEtcd(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) EtcdMemberByID(ctx context.Context, id string) (EtcdMember, error) {
	m, err := scanEtcd(s.R.QueryRowContext(ctx, etcdCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

func (s *Store) CreateEtcdMember(ctx context.Context, m EtcdMember) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO etcd_members (id, ordinal, node_id, state, cluster, new_cluster, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, m.ID, m.Ordinal, m.NodeID, m.State, m.Cluster, m.NewCluster, m.CreatedAt.Unix(), m.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

// UpdateEtcdMember saves a member's observed state and spec.
func (s *Store) UpdateEtcdMember(ctx context.Context, m EtcdMember, now time.Time) error {
	return s.execOne(ctx, `UPDATE etcd_members SET state = ?, ip = ?, error = ?, spec_hash = ?, member_id = ?, updated_at = ? WHERE id = ?`,
		m.State, m.IP, m.Error, m.SpecHash, m.MemberID, now.Unix(), m.ID)
}

func (s *Store) DeleteEtcdMember(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM etcd_members WHERE id = ?`, id)
}
