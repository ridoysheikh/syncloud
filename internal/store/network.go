package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// NodeNetwork is a node's mesh allocation and WireGuard identity.
type NodeNetwork struct {
	NodeID      string
	NodeName    string
	MeshIndex   int
	SubnetIndex int
	PublicKey   string
	Endpoint    string
	UpdatedAt   time.Time
}

// ErrPoolExhausted means no index is free.
var ErrPoolExhausted = errors.New("address pool exhausted")

// IndexPool describes the allocatable indexes of one kind.
type IndexPool struct {
	Kind     string
	Min, Max int
	// Skip excludes indexes (e.g. ones that map to .0/.255 addresses).
	Skip func(int) bool
	// Prefer is tried first when free (e.g. 1 for the controller node).
	Prefer int
}

// AllocateNodeNetwork returns the node's allocation, creating one on first
// use with the lowest free index of each pool.
func (s *Store) AllocateNodeNetwork(ctx context.Context, nodeID string, mesh, subnet IndexPool, cooldown time.Duration, now time.Time) (NodeNetwork, error) {
	if nn, err := s.NodeNetwork(ctx, nodeID); err == nil {
		return nn, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nn, err
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return NodeNetwork{}, err
	}
	defer tx.Rollback()
	mi, err := freeIndex(ctx, tx, mesh, "mesh_index", cooldown, now)
	if err != nil {
		return NodeNetwork{}, err
	}
	si, err := freeIndex(ctx, tx, subnet, "subnet_index", cooldown, now)
	if err != nil {
		return NodeNetwork{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO node_network (node_id, mesh_index, subnet_index, updated_at) VALUES (?, ?, ?, ?)`,
		nodeID, mi, si, now.Unix()); err != nil {
		return NodeNetwork{}, err
	}
	if err := tx.Commit(); err != nil {
		return NodeNetwork{}, err
	}
	return s.NodeNetwork(ctx, nodeID)
}

func freeIndex(ctx context.Context, tx *sql.Tx, p IndexPool, col string, cooldown time.Duration, now time.Time) (int, error) {
	taken := map[int]bool{}
	rows, err := tx.QueryContext(ctx,
		`SELECT `+col+` FROM node_network UNION SELECT idx FROM ipam_released WHERE kind = ? AND released_at > ?`,
		p.Kind, now.Add(-cooldown).Unix())
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			rows.Close()
			return 0, err
		}
		taken[i] = true
	}
	rows.Close()
	ok := func(i int) bool { return i >= p.Min && i <= p.Max && !taken[i] && (p.Skip == nil || !p.Skip(i)) }
	if p.Prefer != 0 && ok(p.Prefer) {
		return p.Prefer, nil
	}
	for i := p.Min; i <= p.Max; i++ {
		if ok(i) {
			return i, nil
		}
	}
	return 0, ErrPoolExhausted
}

const nodeNetworkCols = `SELECT nn.node_id, n.name, nn.mesh_index, nn.subnet_index, nn.public_key, nn.endpoint, nn.updated_at
	FROM node_network nn JOIN nodes n ON n.id = nn.node_id`

func scanNodeNetwork(r scanner) (NodeNetwork, error) {
	var nn NodeNetwork
	var updated int64
	err := r.Scan(&nn.NodeID, &nn.NodeName, &nn.MeshIndex, &nn.SubnetIndex, &nn.PublicKey, &nn.Endpoint, &updated)
	nn.UpdatedAt = time.Unix(updated, 0).UTC()
	return nn, err
}

func (s *Store) NodeNetwork(ctx context.Context, nodeID string) (NodeNetwork, error) {
	nn, err := scanNodeNetwork(s.R.QueryRowContext(ctx, nodeNetworkCols+` WHERE nn.node_id = ?`, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return nn, ErrNotFound
	}
	return nn, err
}

func (s *Store) ListNodeNetworks(ctx context.Context) ([]NodeNetwork, error) {
	rows, err := s.R.QueryContext(ctx, nodeNetworkCols+` ORDER BY nn.mesh_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeNetwork
	for rows.Next() {
		nn, err := scanNodeNetwork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, nn)
	}
	return out, rows.Err()
}

// SetNodeWireGuard records the node's public key and endpoint. It reports
// whether anything changed.
func (s *Store) SetNodeWireGuard(ctx context.Context, nodeID, publicKey, endpoint string, now time.Time) (bool, error) {
	res, err := s.W.ExecContext(ctx,
		`UPDATE node_network SET public_key = ?, endpoint = ?, updated_at = ?
		 WHERE node_id = ? AND (public_key != ? OR endpoint != ?)`,
		publicKey, endpoint, now.Unix(), nodeID, publicKey, endpoint)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
