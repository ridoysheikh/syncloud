package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type CloudProvider struct {
	ID        string
	Name      string
	Type      string
	ConfigEnc []byte
	Summary   string
	CreatedAt time.Time
}

func (s *Store) PutCloudProvider(ctx context.Context, p CloudProvider) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO cloud_providers (id, name, type, config_enc, summary, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, config_enc = excluded.config_enc, summary = excluded.summary`,
		p.ID, p.Name, p.Type, p.ConfigEnc, p.Summary, p.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) ListCloudProviders(ctx context.Context) ([]CloudProvider, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, type, config_enc, summary, created_at FROM cloud_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudProvider
	for rows.Next() {
		var p CloudProvider
		var at int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.ConfigEnc, &p.Summary, &at); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// ErrInUse means something still refers to the object.
var ErrInUse = errors.New("in use")

func (s *Store) DeleteCloudProvider(ctx context.Context, id string) error {
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM node_pools WHERE provider_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := s.W.ExecContext(ctx, `DELETE FROM cloud_providers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

type NodePool struct {
	ID         string
	Name       string
	Role       string // worker | edge
	ProviderID string // "" for a manual pool
	Spec       string // JSON
	Min, Max   int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s *Store) PutNodePool(ctx context.Context, p NodePool) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO node_pools (id, name, role, provider_id, spec, min_nodes, max_nodes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, role = excluded.role, provider_id = excluded.provider_id, spec = excluded.spec,
		  min_nodes = excluded.min_nodes, max_nodes = excluded.max_nodes, updated_at = excluded.updated_at`,
		p.ID, p.Name, p.Role, nullStr(p.ProviderID), p.Spec, p.Min, p.Max, p.CreatedAt.Unix(), p.UpdatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) ListNodePools(ctx context.Context) ([]NodePool, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, role, coalesce(provider_id, ''), spec, min_nodes, max_nodes, created_at, updated_at FROM node_pools ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodePool
	for rows.Next() {
		var p NodePool
		var c, u int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Role, &p.ProviderID, &p.Spec, &p.Min, &p.Max, &c, &u); err != nil {
			return nil, err
		}
		p.CreatedAt, p.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteNodePool(ctx context.Context, id string) error {
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM nodes WHERE pool_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := s.W.ExecContext(ctx, `DELETE FROM node_pools WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

// PoolServer is a server a pool created at its provider.
type PoolServer struct {
	PoolID    string    `json:"poolId"`
	ServerID  string    `json:"serverId"`
	Name      string    `json:"name"`
	State     string    `json:"state"` // creating | joined | failed | deleting
	NodeID    string    `json:"nodeId,omitempty"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Store) PutPoolServer(ctx context.Context, x PoolServer) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO pool_servers (pool_id, server_id, name, state, node_id, message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pool_id, server_id) DO UPDATE SET state = excluded.state, node_id = excluded.node_id, message = excluded.message, updated_at = excluded.updated_at`,
		x.PoolID, x.ServerID, x.Name, x.State, nullStr(x.NodeID), x.Message, x.CreatedAt.Unix(), x.UpdatedAt.Unix())
	return err
}

func (s *Store) ListPoolServers(ctx context.Context, poolID string) ([]PoolServer, error) {
	q := `SELECT pool_id, server_id, name, state, coalesce(node_id, ''), message, created_at, updated_at FROM pool_servers`
	var args []any
	if poolID != "" {
		q += ` WHERE pool_id = ?`
		args = append(args, poolID)
	}
	rows, err := s.R.QueryContext(ctx, q+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PoolServer
	for rows.Next() {
		var x PoolServer
		var c, u int64
		if err := rows.Scan(&x.PoolID, &x.ServerID, &x.Name, &x.State, &x.NodeID, &x.Message, &c, &u); err != nil {
			return nil, err
		}
		x.CreatedAt, x.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) DeletePoolServer(ctx context.Context, poolID, serverID string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM pool_servers WHERE pool_id = ? AND server_id = ?`, poolID, serverID)
	return err
}

type PoolEvent struct {
	ID      int64     `json:"id"`
	PoolID  string    `json:"poolId"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// AddPoolEvent records a decision; 500 are kept per pool.
func (s *Store) AddPoolEvent(ctx context.Context, e PoolEvent) error {
	if _, err := s.W.ExecContext(ctx, `INSERT INTO pool_events (pool_id, at, kind, message) VALUES (?, ?, ?, ?)`, e.PoolID, e.At.Unix(), e.Kind, e.Message); err != nil {
		return err
	}
	_, err := s.W.ExecContext(ctx, `DELETE FROM pool_events WHERE pool_id = ? AND id NOT IN (SELECT id FROM pool_events WHERE pool_id = ? ORDER BY id DESC LIMIT 500)`, e.PoolID, e.PoolID)
	return err
}

func (s *Store) ListPoolEvents(ctx context.Context, poolID string, limit int) ([]PoolEvent, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, pool_id, at, kind, message FROM pool_events WHERE pool_id = ? ORDER BY id DESC LIMIT ?`, poolID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PoolEvent{}
	for rows.Next() {
		var e PoolEvent
		var at int64
		if err := rows.Scan(&e.ID, &e.PoolID, &at, &e.Kind, &e.Message); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastPoolEvent returns the newest event of a kind ("" any).
func (s *Store) LastPoolEvent(ctx context.Context, poolID, kind string) (PoolEvent, error) {
	var e PoolEvent
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT id, pool_id, at, kind, message FROM pool_events WHERE pool_id = ? AND (? = '' OR kind = ?) ORDER BY id DESC LIMIT 1`,
		poolID, kind, kind).Scan(&e.ID, &e.PoolID, &at, &e.Kind, &e.Message)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	e.At = time.Unix(at, 0).UTC()
	return e, err
}
