package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The port table (Phase 26): every public port on every load-balancer node
// it is served from. Its primary key (node, protocol, port) is what makes
// a second holder on one node impossible.

// Owners of public ports.
const (
	PortOwnerService  = "service"
	PortOwnerDatabase = "database"
)

// How a public port was picked.
const (
	PortAuto   = "auto"   // from the automatic range
	PortChosen = "chosen" // given by the user
)

// ErrPortTaken means a port is already held on one of the nodes.
var ErrPortTaken = errors.New("port already taken on a load balancer")

// PortClaim is one public port held on one node.
type PortClaim struct {
	Node      string    `json:"node"`
	Protocol  string    `json:"protocol"`
	Port      int       `json:"port"`
	OwnerKind string    `json:"ownerKind"`
	OwnerID   string    `json:"ownerId"`
	PortName  string    `json:"portName"`
	Mode      string    `json:"mode"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListPortClaims returns every claim, by protocol, port and node.
func (s *Store) ListPortClaims(ctx context.Context) ([]PortClaim, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT node, protocol, port, owner_kind, owner_id, port_name, mode, created_at
		FROM public_ports ORDER BY port, protocol, node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortClaim
	for rows.Next() {
		var c PortClaim
		var at int64
		if err := rows.Scan(&c.Node, &c.Protocol, &c.Port, &c.OwnerKind, &c.OwnerID, &c.PortName, &c.Mode, &at); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(0, at).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReplaceOwnerClaims replaces one owner's claims.
func (s *Store) ReplaceOwnerClaims(ctx context.Context, kind, id string, claims []PortClaim) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceClaims(ctx, tx, kind, id, claims); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceAllPortClaims rewrites the whole table (a resync after load
// balancers changed). Claims that would be a second holder on a node are
// skipped and returned.
func (s *Store) ReplaceAllPortClaims(ctx context.Context, claims []PortClaim) ([]PortClaim, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM public_ports`); err != nil {
		return nil, err
	}
	var skipped []PortClaim
	for _, c := range claims {
		if err := insertClaim(ctx, tx, c); errors.Is(err, ErrPortTaken) {
			skipped = append(skipped, c)
		} else if err != nil {
			return nil, err
		}
	}
	return skipped, tx.Commit()
}

func replaceClaims(ctx context.Context, tx *sql.Tx, kind, id string, claims []PortClaim) error {
	// Keep when each port was first claimed: the oldest holder of a
	// number keeps its plain Traefik names.
	since := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT protocol, port, port_name, created_at FROM public_ports WHERE owner_kind = ? AND owner_id = ?`, kind, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var proto, name string
		var port int
		var at int64
		if err := rows.Scan(&proto, &port, &name, &at); err != nil {
			rows.Close()
			return err
		}
		k := claimKey(proto, port, name)
		if v, ok := since[k]; !ok || at < v {
			since[k] = at
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM public_ports WHERE owner_kind = ? AND owner_id = ?`, kind, id); err != nil {
		return err
	}
	for _, c := range claims {
		c.OwnerKind, c.OwnerID = kind, id
		if at, ok := since[claimKey(c.Protocol, c.Port, c.PortName)]; ok {
			c.CreatedAt = time.Unix(0, at).UTC()
		}
		if err := insertClaim(ctx, tx, c); err != nil {
			return err
		}
	}
	return nil
}

func claimKey(proto string, port int, name string) string {
	return fmt.Sprintf("%s/%d/%s", proto, port, name)
}

func insertClaim(ctx context.Context, tx *sql.Tx, c PortClaim) error {
	if c.Mode == "" {
		c.Mode = PortAuto
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO public_ports (node, protocol, port, owner_kind, owner_id, port_name, mode, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Node, c.Protocol, c.Port, c.OwnerKind, c.OwnerID, c.PortName, c.Mode, c.CreatedAt.UnixNano())
	if isUnique(err) {
		return ErrPortTaken
	}
	return err
}
