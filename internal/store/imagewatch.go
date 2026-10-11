package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ImageWatch is a service's auto-deploy of new pushes of its image.
type ImageWatch struct {
	ServiceID   string
	AutoDeploy  bool
	PollSeconds int
	// WebhookHash is the sha256 (hex) of the deploy webhook's token; "" = none.
	WebhookHash  string
	CheckedAt    *time.Time
	LastDigest   string
	LastError    string
	LastDeployAt *time.Time
	UpdatedAt    time.Time
}

const imageWatchCols = `SELECT service_id, auto_deploy, poll_seconds, webhook_hash, checked_at, last_digest, last_error, last_deploy_at, updated_at FROM image_watches`

func scanImageWatch(r interface{ Scan(...any) error }) (ImageWatch, error) {
	var w ImageWatch
	var checked, deployed sql.NullInt64
	var updated int64
	err := r.Scan(&w.ServiceID, &w.AutoDeploy, &w.PollSeconds, &w.WebhookHash, &checked, &w.LastDigest, &w.LastError, &deployed, &updated)
	w.CheckedAt, w.LastDeployAt, w.UpdatedAt = timePtr(checked), timePtr(deployed), time.Unix(updated, 0).UTC()
	return w, err
}

// ImageWatch returns a service's image watch; ErrNotFound when it has none.
func (s *Store) ImageWatch(ctx context.Context, serviceID string) (ImageWatch, error) {
	w, err := scanImageWatch(s.R.QueryRowContext(ctx, imageWatchCols+` WHERE service_id = ?`, serviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// ImageWatchByWebhook finds the watch whose deploy webhook token hashes to hash.
func (s *Store) ImageWatchByWebhook(ctx context.Context, hash string) (ImageWatch, error) {
	if hash == "" {
		return ImageWatch{}, ErrNotFound
	}
	w, err := scanImageWatch(s.R.QueryRowContext(ctx, imageWatchCols+` WHERE webhook_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// AutoDeployWatches lists the watches that watch their registry.
func (s *Store) AutoDeployWatches(ctx context.Context) ([]ImageWatch, error) {
	rows, err := s.R.QueryContext(ctx, imageWatchCols+` WHERE auto_deploy = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageWatch
	for rows.Next() {
		w, err := scanImageWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// PutImageWatchSettings sets a service's auto-deploy and polling interval.
func (s *Store) PutImageWatchSettings(ctx context.Context, serviceID string, autoDeploy bool, pollSeconds int, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO image_watches (service_id, auto_deploy, poll_seconds, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (service_id) DO UPDATE SET auto_deploy = excluded.auto_deploy, poll_seconds = excluded.poll_seconds,
		  last_error = CASE WHEN excluded.auto_deploy = 0 THEN '' ELSE last_error END, updated_at = excluded.updated_at`,
		serviceID, autoDeploy, pollSeconds, now.Unix())
	return err
}

// SetImageWebhook sets (or with "" removes) the deploy webhook's token hash.
func (s *Store) SetImageWebhook(ctx context.Context, serviceID, hash string, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO image_watches (service_id, auto_deploy, webhook_hash, updated_at) VALUES (?, 0, ?, ?)
		ON CONFLICT (service_id) DO UPDATE SET webhook_hash = excluded.webhook_hash, updated_at = excluded.updated_at`,
		serviceID, hash, now.Unix())
	return err
}

// RecordImageCheck stores the result of a check of the registry.
func (s *Store) RecordImageCheck(ctx context.Context, serviceID, digest, errMsg string, deployed bool, now time.Time) error {
	var dep any
	if deployed {
		dep = now.Unix()
	}
	_, err := s.W.ExecContext(ctx, `UPDATE image_watches SET checked_at = ?, last_error = ?,
		last_digest = CASE WHEN ? <> '' THEN ? ELSE last_digest END, last_deploy_at = coalesce(?, last_deploy_at) WHERE service_id = ?`,
		now.Unix(), errMsg, digest, digest, dep, serviceID)
	return err
}
