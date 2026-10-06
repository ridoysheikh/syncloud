package store

import (
	"context"
	"database/sql"
	"time"
)

// RegistryEvent is one manifest push, pull or delete reported by the registry.
type RegistryEvent struct {
	ID         int64     `json:"id"`
	EventID    string    `json:"-"`
	At         time.Time `json:"at"`
	Action     string    `json:"action"`
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	Digest     string    `json:"digest"`
	Actor      string    `json:"actor"`
	Addr       string    `json:"addr"`
	UserAgent  string    `json:"userAgent"`
}

// ImageStats are the push and pull counters of one digest.
type ImageStats struct {
	Repository   string     `json:"repository"`
	Digest       string     `json:"digest"`
	Pushes       int        `json:"pushes"`
	Pulls        int        `json:"pulls"`
	LastPushedAt *time.Time `json:"lastPushedAt"`
	LastPulledAt *time.Time `json:"lastPulledAt"`
}

// PullDedupWindow merges the manifest requests of one pull.
const PullDedupWindow = 30 * time.Second

// RecordRegistryEvents stores events, skipping ones already seen (the
// registry retries deliveries), and updates the per-digest counters. It
// returns how many were new.
func (s *Store) RecordRegistryEvents(ctx context.Context, evs []RegistryEvent) (int, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, e := range evs {
		if e.Action == "pull" {
			// One docker pull asks for several manifests (HEAD by tag, then
			// the index and the platform manifest by digest): keep the first.
			var dup int
			err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM registry_events WHERE action = 'pull' AND repository = ? AND actor = ? AND addr = ? AND at >= ?`,
				e.Repository, e.Actor, e.Addr, e.At.Add(-PullDedupWindow).Unix()).Scan(&dup)
			if err != nil {
				return 0, err
			}
			if dup > 0 {
				continue
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO registry_events (event_id, at, action, repository, tag, digest, actor, addr, user_agent)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.EventID, e.At.Unix(), e.Action, e.Repository, e.Tag, e.Digest, e.Actor, e.Addr, e.UserAgent)
		if err != nil {
			return 0, err
		}
		if added, _ := res.RowsAffected(); added == 0 || e.Digest == "" {
			continue
		}
		n++
		switch e.Action {
		case "push":
			_, err = tx.ExecContext(ctx, `INSERT INTO registry_image_stats (repository, digest, pushes, last_pushed_at) VALUES (?, ?, 1, ?)
				ON CONFLICT(repository, digest) DO UPDATE SET pushes = pushes + 1, last_pushed_at = MAX(COALESCE(last_pushed_at, 0), excluded.last_pushed_at)`,
				e.Repository, e.Digest, e.At.Unix())
		case "pull":
			_, err = tx.ExecContext(ctx, `INSERT INTO registry_image_stats (repository, digest, pulls, last_pulled_at) VALUES (?, ?, 1, ?)
				ON CONFLICT(repository, digest) DO UPDATE SET pulls = pulls + 1, last_pulled_at = MAX(COALESCE(last_pulled_at, 0), excluded.last_pulled_at)`,
				e.Repository, e.Digest, e.At.Unix())
		case "delete":
			_, err = tx.ExecContext(ctx, `DELETE FROM registry_image_stats WHERE repository = ? AND digest = ?`, e.Repository, e.Digest)
		}
		if err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

// ListRegistryEvents returns the newest events, of one repository when repo
// is set.
func (s *Store) ListRegistryEvents(ctx context.Context, repo string, limit int) ([]RegistryEvent, error) {
	q := `SELECT id, at, action, repository, tag, digest, actor, addr, user_agent FROM registry_events`
	args := []any{}
	if repo != "" {
		q += ` WHERE repository = ?`
		args = append(args, repo)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RegistryEvent{}
	for rows.Next() {
		var e RegistryEvent
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Action, &e.Repository, &e.Tag, &e.Digest, &e.Actor, &e.Addr, &e.UserAgent); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// ImageStatsFor returns the counters of every digest of repo ("" = all).
func (s *Store) ImageStatsFor(ctx context.Context, repo string) ([]ImageStats, error) {
	q := `SELECT repository, digest, pushes, pulls, last_pushed_at, last_pulled_at FROM registry_image_stats`
	args := []any{}
	if repo != "" {
		q += ` WHERE repository = ?`
		args = append(args, repo)
	}
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageStats
	for rows.Next() {
		var st ImageStats
		var pushed, pulled sql.NullInt64
		if err := rows.Scan(&st.Repository, &st.Digest, &st.Pushes, &st.Pulls, &pushed, &pulled); err != nil {
			return nil, err
		}
		st.LastPushedAt, st.LastPulledAt = timePtr(pushed), timePtr(pulled)
		out = append(out, st)
	}
	return out, rows.Err()
}

// PruneRegistryEvents deletes events older than before.
func (s *Store) PruneRegistryEvents(ctx context.Context, before time.Time) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM registry_events WHERE at < ?`, before.Unix())
	return err
}
