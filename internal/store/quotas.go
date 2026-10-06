package store

import (
	"context"
	"time"
)

type Quota struct {
	ProjectID     string
	EnvironmentID string // "" for the project-wide quota
	Limits        string // JSON
	UpdatedAt     time.Time
}

func (s *Store) ListQuotas(ctx context.Context) ([]Quota, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT project_id, environment_id, limits, updated_at FROM quotas`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Quota
	for rows.Next() {
		var q Quota
		var at int64
		if err := rows.Scan(&q.ProjectID, &q.EnvironmentID, &q.Limits, &at); err != nil {
			return nil, err
		}
		q.UpdatedAt = time.Unix(at, 0).UTC()
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Store) PutQuota(ctx context.Context, q Quota) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO quotas (project_id, environment_id, limits, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, environment_id) DO UPDATE SET limits = excluded.limits, updated_at = excluded.updated_at`,
		q.ProjectID, q.EnvironmentID, q.Limits, q.UpdatedAt.Unix())
	return err
}

func (s *Store) DeleteQuota(ctx context.Context, projectID, envID string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM quotas WHERE project_id = ? AND environment_id = ?`, projectID, envID)
	return err
}

// UsageRow is one project-environment-day of metered usage.
type UsageRow struct {
	Project          string  `json:"project"`
	Environment      string  `json:"environment"`
	Day              string  `json:"day"`
	CPUReservedHours float64 `json:"cpuReservedHours"`
	MemReservedHours float64 `json:"memReservedGiBHours"`
	CPUUsedHours     float64 `json:"cpuUsedHours"`
	MemUsedHours     float64 `json:"memUsedGiBHours"`
	NetOutBytes      int64   `json:"netOutBytes"`
	LogBytes         int64   `json:"logBytes"`
	BuildSeconds     int64   `json:"buildSeconds"`
}

// AddUsage adds to a day's totals.
func (s *Store) AddUsage(ctx context.Context, u UsageRow) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO usage_daily (project, environment, day, cpu_reserved_hours, mem_reserved_hours, cpu_used_hours,
		mem_used_hours, net_out_bytes, log_bytes, build_seconds) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project, environment, day) DO UPDATE SET
		  cpu_reserved_hours = cpu_reserved_hours + excluded.cpu_reserved_hours,
		  mem_reserved_hours = mem_reserved_hours + excluded.mem_reserved_hours,
		  cpu_used_hours = cpu_used_hours + excluded.cpu_used_hours,
		  mem_used_hours = mem_used_hours + excluded.mem_used_hours,
		  net_out_bytes = net_out_bytes + excluded.net_out_bytes,
		  log_bytes = log_bytes + excluded.log_bytes,
		  build_seconds = build_seconds + excluded.build_seconds`,
		u.Project, u.Environment, u.Day, u.CPUReservedHours, u.MemReservedHours, u.CPUUsedHours, u.MemUsedHours, u.NetOutBytes, u.LogBytes, u.BuildSeconds)
	return err
}

// ListUsage returns days in [from, to] (YYYY-MM-DD), optionally for one project.
func (s *Store) ListUsage(ctx context.Context, project, from, to string) ([]UsageRow, error) {
	q := `SELECT project, environment, day, cpu_reserved_hours, mem_reserved_hours, cpu_used_hours, mem_used_hours, net_out_bytes, log_bytes, build_seconds
		FROM usage_daily WHERE day >= ? AND day <= ?`
	args := []any{from, to}
	if project != "" {
		q += ` AND project = ?`
		args = append(args, project)
	}
	rows, err := s.R.QueryContext(ctx, q+` ORDER BY day, project, environment`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.Project, &u.Environment, &u.Day, &u.CPUReservedHours, &u.MemReservedHours, &u.CPUUsedHours, &u.MemUsedHours,
			&u.NetOutBytes, &u.LogBytes, &u.BuildSeconds); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
