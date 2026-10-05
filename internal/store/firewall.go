package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type FirewallRule struct {
	Protocol    string   `json:"protocol"`
	Ports       string   `json:"ports"`
	Sources     []string `json:"sources"`
	Description string   `json:"description"`
}

type FirewallPolicy struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Targets     []string       `json:"targets"`
	Rules       []FirewallRule `json:"rules"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

// AppliesTo reports whether the policy targets nodeID.
func (p FirewallPolicy) AppliesTo(nodeID string) bool {
	for _, t := range p.Targets {
		if t == "*" || t == nodeID {
			return true
		}
	}
	return false
}

func (s *Store) ListFirewallPolicies(ctx context.Context) ([]FirewallPolicy, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, description, targets, rules, created_at, updated_at FROM firewall_policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FirewallPolicy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) FirewallPolicy(ctx context.Context, id string) (FirewallPolicy, error) {
	p, err := scanPolicy(s.R.QueryRowContext(ctx, `SELECT id, name, description, targets, rules, created_at, updated_at FROM firewall_policies WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func scanPolicy(r scanner) (FirewallPolicy, error) {
	var p FirewallPolicy
	var targets, rules string
	var created, updated int64
	if err := r.Scan(&p.ID, &p.Name, &p.Description, &targets, &rules, &created, &updated); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(targets), &p.Targets); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(rules), &p.Rules); err != nil {
		return p, err
	}
	p.CreatedAt, p.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return p, nil
}

// PutFirewallPolicy creates or replaces a policy. It returns ErrNameTaken
// when another policy has the name.
func (s *Store) PutFirewallPolicy(ctx context.Context, p FirewallPolicy) error {
	targets, _ := json.Marshal(p.Targets)
	rules, _ := json.Marshal(p.Rules)
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO firewall_policies (id, name, description, targets, rules, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, targets = excluded.targets,
		   rules = excluded.rules, updated_at = excluded.updated_at`,
		p.ID, p.Name, p.Description, string(targets), string(rules), p.CreatedAt.Unix(), p.UpdatedAt.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: firewall_policies.name") {
		return ErrNameTaken
	}
	return err
}

func (s *Store) DeleteFirewallPolicy(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM firewall_policies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
