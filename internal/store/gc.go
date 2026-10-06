package store

import (
	"context"
	"time"
)

// Retention of history the controller keeps (§Phase 9 GC). Zero keeps forever.
type Retention struct {
	Audit       time.Duration // audit log
	Incidents   time.Duration // closed incidents
	Deployments time.Duration // finished deployments
	Builds      time.Duration // finished builds (the deployed build of a service is kept)
	Usage       time.Duration // daily usage rows
	Revisions   int           // task definitions per service (current, previous and in-flight ones are kept)
}

// DefaultRetention is what the controller applies unless configured.
var DefaultRetention = Retention{
	Audit: 365 * 24 * time.Hour, Incidents: 90 * 24 * time.Hour, Deployments: 90 * 24 * time.Hour,
	Builds: 90 * 24 * time.Hour, Usage: 400 * 24 * time.Hour, Revisions: 50,
}

// GCResult counts deleted rows per table.
type GCResult map[string]int64

// GC deletes history older than the retention.
func (s *Store) GC(ctx context.Context, r Retention, now time.Time) (GCResult, error) {
	out := GCResult{}
	run := func(table, q string, args ...any) error {
		res, err := s.W.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		out[table] += n
		return nil
	}
	before := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	steps := []struct {
		on    bool
		table string
		q     string
		args  []any
	}{
		{r.Audit > 0, "audit_events", `DELETE FROM audit_events WHERE at < ?`, []any{before(r.Audit)}},
		{r.Incidents > 0, "incidents", `DELETE FROM incidents WHERE closed_at IS NOT NULL AND closed_at < ?`, []any{before(r.Incidents)}},
		{r.Deployments > 0, "deployments", `DELETE FROM deployments WHERE finished_at IS NOT NULL AND finished_at < ?
			AND id NOT IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY service_id ORDER BY started_at DESC) AS n FROM deployments) WHERE n <= 5)`,
			[]any{before(r.Deployments)}},
		{r.Builds > 0, "builds", `DELETE FROM builds WHERE finished_at IS NOT NULL AND finished_at < ?
			AND id NOT IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY service_id ORDER BY created_at DESC) AS n FROM builds WHERE deployed = 1) WHERE n <= 1)`,
			[]any{before(r.Builds)}},
		{r.Usage > 0, "usage_daily", `DELETE FROM usage_daily WHERE day < ?`, []any{now.Add(-r.Usage).UTC().Format("2006-01-02")}},
		{r.Revisions > 0, "task_definitions", `DELETE FROM task_definitions WHERE (service_id, revision) IN (
			SELECT td.service_id, td.revision FROM task_definitions td JOIN services sv ON sv.id = td.service_id
			WHERE td.revision < sv.revision - 1
			  AND td.revision <= (SELECT MAX(revision) FROM task_definitions WHERE service_id = td.service_id) - ?
			  AND td.revision NOT IN (SELECT from_rev FROM deployments WHERE service_id = td.service_id AND status = 'in_progress')
			  AND td.revision NOT IN (SELECT revision FROM tasks WHERE service_id = td.service_id))`, []any{r.Revisions}},
	}
	for _, st := range steps {
		if !st.on {
			continue
		}
		if err := run(st.table, st.q, st.args...); err != nil {
			return out, err
		}
	}
	return out, nil
}
