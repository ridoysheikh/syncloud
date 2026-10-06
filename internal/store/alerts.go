package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type AlertChannel struct {
	ID        string
	Name      string
	Type      string
	ConfigEnc []byte
	Summary   string
	CreatedAt time.Time
}

func (s *Store) PutAlertChannel(ctx context.Context, c AlertChannel) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO alert_channels (id, name, type, config_enc, summary, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, type = excluded.type, config_enc = excluded.config_enc, summary = excluded.summary`,
		c.ID, c.Name, c.Type, c.ConfigEnc, c.Summary, c.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) ListAlertChannels(ctx context.Context) ([]AlertChannel, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, type, config_enc, summary, created_at FROM alert_channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertChannel
	for rows.Next() {
		var c AlertChannel
		var at int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.ConfigEnc, &c.Summary, &at); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AlertChannel(ctx context.Context, id string) (AlertChannel, error) {
	var c AlertChannel
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT id, name, type, config_enc, summary, created_at FROM alert_channels WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Type, &c.ConfigEnc, &c.Summary, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CreatedAt = time.Unix(at, 0).UTC()
	return c, err
}

func (s *Store) DeleteAlertChannel(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM alert_channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type AlertRule struct {
	ID        string
	Name      string
	Spec      string // JSON
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) PutAlertRule(ctx context.Context, r AlertRule) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO alert_rules (id, name, spec, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, spec = excluded.spec, enabled = excluded.enabled, updated_at = excluded.updated_at`,
		r.ID, r.Name, r.Spec, r.Enabled, r.CreatedAt.Unix(), r.UpdatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, spec, enabled, created_at, updated_at FROM alert_rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		var r AlertRule
		var c, u int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Spec, &r.Enabled, &c, &u); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AlertState is a rule instance whose condition holds.
type AlertState struct {
	RuleID  string    `json:"ruleId"`
	Key     string    `json:"key"`
	Label   string    `json:"label"`
	State   string    `json:"state"` // pending | firing
	Since   time.Time `json:"since"`
	Value   *float64  `json:"value"`
	Message string    `json:"message"`
}

func (s *Store) PutAlertState(ctx context.Context, a AlertState) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO alert_states (rule_id, key, label, state, since, value, message) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(rule_id, key) DO UPDATE SET label = excluded.label, state = excluded.state, since = excluded.since,
		  value = excluded.value, message = excluded.message`,
		a.RuleID, a.Key, a.Label, a.State, a.Since.Unix(), a.Value, a.Message)
	return err
}

func (s *Store) DeleteAlertState(ctx context.Context, ruleID, key string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM alert_states WHERE rule_id = ? AND key = ?`, ruleID, key)
	return err
}

func (s *Store) ListAlertStates(ctx context.Context) ([]AlertState, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT rule_id, key, label, state, since, value, message FROM alert_states ORDER BY since`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertState{}
	for rows.Next() {
		var a AlertState
		var since int64
		var v sql.NullFloat64
		if err := rows.Scan(&a.RuleID, &a.Key, &a.Label, &a.State, &since, &v, &a.Message); err != nil {
			return nil, err
		}
		a.Since = time.Unix(since, 0).UTC()
		if v.Valid {
			a.Value = &v.Float64
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AlertEvent is a notification: an alert firing or resolving, or a one-off
// event (a failed deployment).
type AlertEvent struct {
	ID       int64     `json:"id"`
	RuleID   string    `json:"ruleId"`
	RuleName string    `json:"rule"`
	Severity string    `json:"severity"`
	Kind     string    `json:"kind"`
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Message  string    `json:"message"`
	Value    *float64  `json:"value"`
	Delivery string    `json:"delivery"`
	At       time.Time `json:"at"`
}

// AddAlertEvent records a notification; events older than 90 days go.
func (s *Store) AddAlertEvent(ctx context.Context, e AlertEvent) (int64, error) {
	res, err := s.W.ExecContext(ctx, `INSERT INTO alert_events (rule_id, rule_name, severity, kind, key, label, message, value, delivery, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.RuleID, e.RuleName, e.Severity, e.Kind, e.Key, e.Label, e.Message, e.Value, e.Delivery, e.At.Unix())
	if err != nil {
		return 0, err
	}
	_, _ = s.W.ExecContext(ctx, `DELETE FROM alert_events WHERE at < ?`, e.At.Add(-90*24*time.Hour).Unix())
	return res.LastInsertId()
}

func (s *Store) SetAlertDelivery(ctx context.Context, id int64, delivery string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE alert_events SET delivery = ? WHERE id = ?`, delivery, id)
	return err
}

func (s *Store) ListAlertEvents(ctx context.Context, limit int) ([]AlertEvent, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, rule_id, rule_name, severity, kind, key, label, message, value, delivery, at
		FROM alert_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertEvent{}
	for rows.Next() {
		var e AlertEvent
		var at int64
		var v sql.NullFloat64
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Severity, &e.Kind, &e.Key, &e.Label, &e.Message, &v, &e.Delivery, &at); err != nil {
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

// FailedSince lists what failed after t: deployments, builds and job runs,
// for event alerts. Each item is (kind, id, service ID, message, when).
type Failure struct {
	Kind      string // deployment | build | job
	ID        string
	ServiceID string
	JobName   string
	Message   string
	At        time.Time
}

func (s *Store) FailedSince(ctx context.Context, t time.Time) ([]Failure, error) {
	var out []Failure
	q := []struct {
		kind, sql string
	}{
		{"deployment", `SELECT id, service_id, '', message, finished_at FROM deployments WHERE status IN ('failed', 'rolled_back') AND finished_at > ?`},
		{"build", `SELECT id, service_id, '', message, finished_at FROM builds WHERE status = 'failed' AND finished_at > ?`},
		{"job", `SELECT r.id, coalesce(r.service_id, ''), coalesce(j.name, ''), r.message, r.finished_at FROM job_runs r LEFT JOIN jobs j ON j.id = r.job_id
			WHERE r.status IN ('failed', 'timed_out') AND r.trigger != 'build' AND r.finished_at > ?`},
	}
	for _, x := range q {
		rows, err := s.R.QueryContext(ctx, x.sql, t.Unix())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			f := Failure{Kind: x.kind}
			var at int64
			if err := rows.Scan(&f.ID, &f.ServiceID, &f.JobName, &f.Message, &at); err != nil {
				rows.Close()
				return nil, err
			}
			f.At = time.Unix(at, 0).UTC()
			out = append(out, f)
		}
		rows.Close()
	}
	return out, nil
}
