package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Rules of a project's default security group (§8.3), as JSON. The same
// text is used by the migration for existing projects.
const (
	DefaultGroupName        = "default"
	DefaultGroupDescription = "Services in the same environment can reach each other; all outbound traffic is allowed."
	DefaultGroupInbound     = `[{"protocol":"any","ports":"","peers":["environment:self"],"description":"Services in the same environment"}]`
	DefaultGroupOutbound    = `[{"protocol":"any","ports":"","peers":["any"],"description":"All outbound traffic"}]`
)

type SecurityGroup struct {
	ID          string
	ProjectID   string
	Project     string // name, joined in
	Name        string
	Description string
	Inbound     string // JSON
	Outbound    string // JSON
	Default     bool
	ServiceIDs  []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const sgCols = `SELECT g.id, g.project_id, p.name, g.name, g.description, g.inbound, g.outbound, g.is_default, g.created_at, g.updated_at
	FROM security_groups g JOIN projects p ON p.id = g.project_id`

func (s *Store) querySecurityGroups(ctx context.Context, where string, args ...any) ([]SecurityGroup, error) {
	rows, err := s.R.QueryContext(ctx, sgCols+" "+where, args...)
	if err != nil {
		return nil, err
	}
	var out []SecurityGroup
	for rows.Next() {
		var g SecurityGroup
		var c, u int64
		if err := rows.Scan(&g.ID, &g.ProjectID, &g.Project, &g.Name, &g.Description, &g.Inbound, &g.Outbound, &g.Default, &c, &u); err != nil {
			rows.Close()
			return nil, err
		}
		g.CreatedAt, g.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := s.securityGroupMembers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ServiceIDs = members[out[i].ID]
	}
	return out, nil
}

func (s *Store) securityGroupMembers(ctx context.Context) (map[string][]string, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT group_id, service_id FROM security_group_services ORDER BY service_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var g, sv string
		if err := rows.Scan(&g, &sv); err != nil {
			return nil, err
		}
		out[g] = append(out[g], sv)
	}
	return out, rows.Err()
}

// ListSecurityGroups returns every group, by project and name.
func (s *Store) ListSecurityGroups(ctx context.Context) ([]SecurityGroup, error) {
	return s.querySecurityGroups(ctx, "ORDER BY p.name, g.is_default DESC, g.name")
}

func (s *Store) SecurityGroupByName(ctx context.Context, projectID, name string) (SecurityGroup, error) {
	gs, err := s.querySecurityGroups(ctx, "WHERE g.project_id = ? AND g.name = ?", projectID, name)
	if err != nil {
		return SecurityGroup{}, err
	}
	if len(gs) == 0 {
		return SecurityGroup{}, ErrNotFound
	}
	return gs[0], nil
}

// PutSecurityGroup creates or updates a group and replaces its attachments.
func (s *Store) PutSecurityGroup(ctx context.Context, g SecurityGroup) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO security_groups (id, project_id, name, description, inbound, outbound, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, inbound = excluded.inbound,
		  outbound = excluded.outbound, updated_at = excluded.updated_at`,
		g.ID, g.ProjectID, g.Name, g.Description, g.Inbound, g.Outbound, g.Default, g.CreatedAt.Unix(), g.UpdatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM security_group_services WHERE group_id = ?`, g.ID); err != nil {
		return err
	}
	for _, sv := range g.ServiceIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO security_group_services (group_id, service_id) VALUES (?, ?)`, g.ID, sv); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteSecurityGroup(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM security_groups WHERE id = ? AND is_default = 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func createDefaultGroup(ctx context.Context, tx *sql.Tx, id, projectID string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO security_groups (id, project_id, name, description, inbound, outbound, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, id, projectID, DefaultGroupName, DefaultGroupDescription, DefaultGroupInbound, DefaultGroupOutbound, at.Unix(), at.Unix())
	return err
}

// SetRunIP records a job run container's address; changed is false when
// it was known already.
func (s *Store) SetRunIP(ctx context.Context, id, ip string) (changed bool, err error) {
	res, err := s.W.ExecContext(ctx, `UPDATE job_runs SET ip = ? WHERE id = ? AND ip != ?`, ip, id, ip)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RunAddress is an active job run with its container address.
type RunAddress struct {
	ID, ServiceID, EnvironmentID, IP string
}

// ActiveRunAddresses lists pending and running runs that have an address.
func (s *Store) ActiveRunAddresses(ctx context.Context) ([]RunAddress, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, coalesce(service_id, ''), environment_id, ip FROM job_runs
		WHERE status IN ('pending', 'running') AND ip != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunAddress
	for rows.Next() {
		var r RunAddress
		if err := rows.Scan(&r.ID, &r.ServiceID, &r.EnvironmentID, &r.IP); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

var errNoDefault = errors.New("project has no default security group")

// DefaultSecurityGroup returns a project's default group.
func (s *Store) DefaultSecurityGroup(ctx context.Context, projectID string) (SecurityGroup, error) {
	gs, err := s.querySecurityGroups(ctx, "WHERE g.project_id = ? AND g.is_default = 1", projectID)
	if err != nil {
		return SecurityGroup{}, err
	}
	if len(gs) == 0 {
		return SecurityGroup{}, errNoDefault
	}
	return gs[0], nil
}
