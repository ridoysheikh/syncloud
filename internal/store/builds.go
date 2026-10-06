package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type GitSource struct {
	ID            string
	ServiceID     string
	URL           string
	Branch        string
	Dockerfile    string
	Context       string
	TokenEnc      []byte
	AutoDeploy    bool
	PollSeconds   int
	WebhookSecret string
	LastSHA       string
	LastCheckedAt *time.Time
	LastError     string
	Failures      int
	CreatedAt     time.Time
	Tags          string            // tag pattern ("" = tags are not watched)
	Paths         []string          // path filters ("!" excludes); empty = any change
	Builder       string            // auto | dockerfile | nixpacks | static
	RefSHAs       map[string]string // last seen commit of each matching ref
	LastWebhookAt *time.Time
}

const gitCols = `SELECT id, service_id, url, branch, dockerfile, context, token_enc, auto_deploy, poll_seconds, webhook_secret,
	last_sha, last_checked_at, last_error, failures, created_at, tags, paths, builder, ref_shas, last_webhook_at FROM git_sources`

func scanGit(r scanner) (GitSource, error) {
	var g GitSource
	var checked, hooked sql.NullInt64
	var created int64
	var paths, refs string
	err := r.Scan(&g.ID, &g.ServiceID, &g.URL, &g.Branch, &g.Dockerfile, &g.Context, &g.TokenEnc, &g.AutoDeploy, &g.PollSeconds,
		&g.WebhookSecret, &g.LastSHA, &checked, &g.LastError, &g.Failures, &created, &g.Tags, &paths, &g.Builder, &refs, &hooked)
	if err != nil {
		return g, err
	}
	g.LastCheckedAt, g.LastWebhookAt, g.CreatedAt = timePtr(checked), timePtr(hooked), time.Unix(created, 0).UTC()
	_ = json.Unmarshal([]byte(paths), &g.Paths)
	_ = json.Unmarshal([]byte(refs), &g.RefSHAs)
	if g.RefSHAs == nil {
		g.RefSHAs = map[string]string{}
	}
	return g, nil
}

func (s *Store) PutGitSource(ctx context.Context, g GitSource) error {
	if g.Paths == nil {
		g.Paths = []string{}
	}
	if g.Builder == "" {
		g.Builder = "auto"
	}
	paths, _ := json.Marshal(g.Paths)
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO git_sources (id, service_id, url, branch, dockerfile, context, token_enc, auto_deploy, poll_seconds, webhook_secret, created_at, tags, paths, builder)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(service_id) DO UPDATE SET url = excluded.url, branch = excluded.branch, dockerfile = excluded.dockerfile,
		   context = excluded.context, token_enc = coalesce(excluded.token_enc, token_enc), auto_deploy = excluded.auto_deploy,
		   poll_seconds = excluded.poll_seconds, failures = 0, last_error = '', tags = excluded.tags, paths = excluded.paths,
		   builder = excluded.builder`,
		g.ID, g.ServiceID, g.URL, g.Branch, g.Dockerfile, g.Context, g.TokenEnc, g.AutoDeploy, g.PollSeconds, g.WebhookSecret, g.CreatedAt.Unix(),
		g.Tags, string(paths), g.Builder)
	return err
}

// SetGitRefs stores the last seen commit of every watched ref.
func (s *Store) SetGitRefs(ctx context.Context, id string, refs map[string]string) error {
	b, _ := json.Marshal(refs)
	_, err := s.W.ExecContext(ctx, `UPDATE git_sources SET ref_shas = ? WHERE id = ?`, string(b), id)
	return err
}

// RecordGitWebhook notes a webhook delivery (polling then slows down).
func (s *Store) RecordGitWebhook(ctx context.Context, id string, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE git_sources SET last_webhook_at = ? WHERE id = ?`, now.Unix(), id)
	return err
}

func (s *Store) GitSourceByService(ctx context.Context, serviceID string) (GitSource, error) {
	g, err := scanGit(s.R.QueryRowContext(ctx, gitCols+` WHERE service_id = ?`, serviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

func (s *Store) GitSourceByID(ctx context.Context, id string) (GitSource, error) {
	g, err := scanGit(s.R.QueryRowContext(ctx, gitCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

func (s *Store) ListGitSources(ctx context.Context) ([]GitSource, error) {
	rows, err := s.R.QueryContext(ctx, gitCols+` ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitSource
	for rows.Next() {
		g, err := scanGit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) DeleteGitSource(ctx context.Context, serviceID string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM git_sources WHERE service_id = ?`, serviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordGitCheck stores a poll result. sha is set only on success.
func (s *Store) RecordGitCheck(ctx context.Context, id, sha, errMsg string, now time.Time) error {
	if errMsg != "" {
		_, err := s.W.ExecContext(ctx, `UPDATE git_sources SET last_checked_at = ?, last_error = ?, failures = failures + 1 WHERE id = ?`, now.Unix(), errMsg, id)
		return err
	}
	_, err := s.W.ExecContext(ctx, `UPDATE git_sources SET last_checked_at = ?, last_error = '', failures = 0, last_sha = ? WHERE id = ?`, now.Unix(), sha, id)
	return err
}

type Build struct {
	ID         string     `json:"id"`
	ServiceID  string     `json:"serviceId"`
	SHA        string     `json:"sha"`
	Ref        string     `json:"ref"`
	Trigger    string     `json:"trigger"`
	Status     string     `json:"status"`
	Image      string     `json:"image"`
	RunID      string     `json:"runId"`
	Message    string     `json:"message"`
	Deployed   bool       `json:"deployed"`
	BaseSHA    string     `json:"baseSha"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

const buildCols = `SELECT id, service_id, sha, ref, trigger, status, image, run_id, message, deployed, created_at, started_at, finished_at, base_sha FROM builds`

func scanBuild(r scanner) (Build, error) {
	var b Build
	var created int64
	var started, finished sql.NullInt64
	err := r.Scan(&b.ID, &b.ServiceID, &b.SHA, &b.Ref, &b.Trigger, &b.Status, &b.Image, &b.RunID, &b.Message, &b.Deployed, &created, &started, &finished, &b.BaseSHA)
	b.CreatedAt, b.StartedAt, b.FinishedAt = time.Unix(created, 0).UTC(), timePtr(started), timePtr(finished)
	return b, err
}

// CreateBuild records a build; ErrNameTaken means this commit was built already.
func (s *Store) CreateBuild(ctx context.Context, b Build) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO builds (id, service_id, sha, ref, trigger, status, image, created_at, base_sha) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ServiceID, b.SHA, b.Ref, b.Trigger, b.Status, b.Image, b.CreatedAt.Unix(), b.BaseSHA)
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) UpdateBuild(ctx context.Context, b Build) error {
	_, err := s.W.ExecContext(ctx, `UPDATE builds SET status = ?, run_id = ?, message = ?, deployed = ?, started_at = ?, finished_at = ? WHERE id = ?`,
		b.Status, b.RunID, b.Message, b.Deployed, unixPtr(b.StartedAt), unixPtr(b.FinishedAt), b.ID)
	return err
}

func (s *Store) BuildByRun(ctx context.Context, runID string) (Build, error) {
	b, err := scanBuild(s.R.QueryRowContext(ctx, buildCols+` WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func (s *Store) BuildByID(ctx context.Context, id string) (Build, error) {
	b, err := scanBuild(s.R.QueryRowContext(ctx, buildCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func (s *Store) ServiceBuilds(ctx context.Context, serviceID string, limit int) ([]Build, error) {
	return s.queryBuilds(ctx, `WHERE service_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, serviceID, limit)
}

func (s *Store) BuildsByStatus(ctx context.Context, statuses ...string) ([]Build, error) {
	q, args := `WHERE status IN (`, []any{}
	for i, st := range statuses {
		if i > 0 {
			q += ", "
		}
		q += "?"
		args = append(args, st)
	}
	return s.queryBuilds(ctx, q+`) ORDER BY created_at`, args...)
}

func (s *Store) RecentBuilds(ctx context.Context, limit int) ([]Build, error) {
	return s.queryBuilds(ctx, `ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
}

func (s *Store) queryBuilds(ctx context.Context, where string, args ...any) ([]Build, error) {
	rows, err := s.R.QueryContext(ctx, buildCols+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Build
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
