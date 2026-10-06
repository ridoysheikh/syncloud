package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ScalingPolicy is a service's target tracking policy (§5.5).
type ScalingPolicy struct {
	ServiceID        string    `json:"-"`
	Enabled          bool      `json:"enabled"`
	Min              int       `json:"min"`
	Max              int       `json:"max"`
	Metric           string    `json:"metric"`
	Target           float64   `json:"target"`
	ScaleOutCooldown int       `json:"scaleOutCooldown"`
	ScaleInCooldown  int       `json:"scaleInCooldown"`
	ScaleInChecks    int       `json:"scaleInChecks"`
	UpdatedAt        time.Time `json:"updatedAt"`
	UpdatedBy        string    `json:"updatedBy"`
}

func (s *Store) PutScalingPolicy(ctx context.Context, p ScalingPolicy) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO autoscaling_policies (service_id, enabled, min_count, max_count, metric, target,
		scale_out_cooldown, scale_in_cooldown, scale_in_checks, updated_at, updated_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_id) DO UPDATE SET enabled = excluded.enabled, min_count = excluded.min_count, max_count = excluded.max_count,
		  metric = excluded.metric, target = excluded.target, scale_out_cooldown = excluded.scale_out_cooldown,
		  scale_in_cooldown = excluded.scale_in_cooldown, scale_in_checks = excluded.scale_in_checks,
		  updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		p.ServiceID, p.Enabled, p.Min, p.Max, p.Metric, p.Target, p.ScaleOutCooldown, p.ScaleInCooldown, p.ScaleInChecks, p.UpdatedAt.Unix(), p.UpdatedBy)
	return err
}

const scalingPolicyCols = `SELECT service_id, enabled, min_count, max_count, metric, target, scale_out_cooldown, scale_in_cooldown, scale_in_checks,
	updated_at, updated_by FROM autoscaling_policies`

func scanScalingPolicy(r scanner) (ScalingPolicy, error) {
	var p ScalingPolicy
	var at int64
	err := r.Scan(&p.ServiceID, &p.Enabled, &p.Min, &p.Max, &p.Metric, &p.Target, &p.ScaleOutCooldown, &p.ScaleInCooldown, &p.ScaleInChecks, &at, &p.UpdatedBy)
	p.UpdatedAt = time.Unix(at, 0).UTC()
	return p, err
}

func (s *Store) ScalingPolicy(ctx context.Context, serviceID string) (ScalingPolicy, error) {
	p, err := scanScalingPolicy(s.R.QueryRowContext(ctx, scalingPolicyCols+` WHERE service_id = ?`, serviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ListScalingPolicies(ctx context.Context) ([]ScalingPolicy, error) {
	rows, err := s.R.QueryContext(ctx, scalingPolicyCols+` ORDER BY service_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScalingPolicy
	for rows.Next() {
		p, err := scanScalingPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteScalingPolicy(ctx context.Context, serviceID string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM autoscaling_policies WHERE service_id = ?`, serviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ScalingEvent is one autoscaler decision that changed the desired count.
type ScalingEvent struct {
	ID        int64     `json:"id"`
	ServiceID string    `json:"-"`
	At        time.Time `json:"at"`
	From      int       `json:"from"`
	To        int       `json:"to"`
	Metric    string    `json:"metric"`
	Value     *float64  `json:"value"` // nil when there was no data (min/max clamp)
	Target    float64   `json:"target"`
	Reason    string    `json:"reason"`
}

// AddScalingEvent records a decision and keeps the newest 500 per service.
func (s *Store) AddScalingEvent(ctx context.Context, e ScalingEvent) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO scaling_events (service_id, at, from_count, to_count, metric, value, target, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, e.ServiceID, e.At.Unix(), e.From, e.To, e.Metric, e.Value, e.Target, e.Reason)
	if err != nil {
		return err
	}
	_, err = s.W.ExecContext(ctx, `DELETE FROM scaling_events WHERE service_id = ? AND id <= (
		SELECT id FROM scaling_events WHERE service_id = ? ORDER BY id DESC LIMIT 1 OFFSET 500)`, e.ServiceID, e.ServiceID)
	return err
}

func (s *Store) ScalingEvents(ctx context.Context, serviceID string, limit int) ([]ScalingEvent, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, service_id, at, from_count, to_count, metric, value, target, reason FROM scaling_events
		WHERE service_id = ? ORDER BY id DESC LIMIT ?`, serviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScalingEvent{}
	for rows.Next() {
		var e ScalingEvent
		var at int64
		var v sql.NullFloat64
		if err := rows.Scan(&e.ID, &e.ServiceID, &at, &e.From, &e.To, &e.Metric, &v, &e.Target, &e.Reason); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		if v.Valid {
			e.Value = &v.Float64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
