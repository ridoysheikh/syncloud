package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type S3Endpoint struct {
	ID          string
	Name        string
	URL         string
	Region      string
	AccessKeyID string
	SecretEnc   []byte
	PathStyle   bool
	CreatedAt   time.Time
}

const s3EndpointCols = `SELECT id, name, url, region, access_key_id, secret_enc, path_style, created_at FROM s3_endpoints`

func scanS3Endpoint(r interface{ Scan(...any) error }) (S3Endpoint, error) {
	var e S3Endpoint
	var at int64
	err := r.Scan(&e.ID, &e.Name, &e.URL, &e.Region, &e.AccessKeyID, &e.SecretEnc, &e.PathStyle, &at)
	e.CreatedAt = time.Unix(at, 0).UTC()
	return e, err
}

// PutS3Endpoint creates or updates an endpoint (by ID).
func (s *Store) PutS3Endpoint(ctx context.Context, e S3Endpoint) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO s3_endpoints (id, name, url, region, access_key_id, secret_enc, path_style, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, url = excluded.url, region = excluded.region, access_key_id = excluded.access_key_id,
		secret_enc = excluded.secret_enc, path_style = excluded.path_style`,
		e.ID, e.Name, e.URL, e.Region, e.AccessKeyID, e.SecretEnc, e.PathStyle, e.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) ListS3Endpoints(ctx context.Context) ([]S3Endpoint, error) {
	rows, err := s.R.QueryContext(ctx, s3EndpointCols+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []S3Endpoint
	for rows.Next() {
		e, err := scanS3Endpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// S3EndpointByRef finds an endpoint by ID or name.
func (s *Store) S3EndpointByRef(ctx context.Context, ref string) (S3Endpoint, error) {
	e, err := scanS3Endpoint(s.R.QueryRowContext(ctx, s3EndpointCols+` WHERE id = ? OR name = ?`, ref, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// DeleteS3Endpoint deletes an endpoint no service is bound to.
func (s *Store) DeleteS3Endpoint(ctx context.Context, id string) error {
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM s3_bindings WHERE endpoint_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := s.W.ExecContext(ctx, `DELETE FROM s3_endpoints WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type S3Binding struct {
	ID         string    `json:"id"`
	ServiceID  string    `json:"serviceId"`
	EndpointID string    `json:"endpointId"`
	Endpoint   string    `json:"endpoint"` // name, joined in
	Bucket     string    `json:"bucket"`
	Prefix     string    `json:"prefix"`
	EnvPrefix  string    `json:"envPrefix"`
	CreatedAt  time.Time `json:"createdAt"`
	// Joined in for listings.
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
}

const s3BindingCols = `SELECT b.id, b.service_id, b.endpoint_id, e.name, b.bucket, b.prefix, b.env_prefix, b.created_at, p.name, en.name, sv.name
	FROM s3_bindings b JOIN s3_endpoints e ON e.id = b.endpoint_id JOIN services sv ON sv.id = b.service_id
	JOIN environments en ON en.id = sv.environment_id JOIN projects p ON p.id = en.project_id`

func (s *Store) queryS3Bindings(ctx context.Context, where string, args ...any) ([]S3Binding, error) {
	rows, err := s.R.QueryContext(ctx, s3BindingCols+" "+where+" ORDER BY p.name, en.name, sv.name, b.env_prefix", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []S3Binding{}
	for rows.Next() {
		var b S3Binding
		var at int64
		if err := rows.Scan(&b.ID, &b.ServiceID, &b.EndpointID, &b.Endpoint, &b.Bucket, &b.Prefix, &b.EnvPrefix, &at, &b.Project, &b.Environment, &b.Service); err != nil {
			return nil, err
		}
		b.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) ServiceS3Bindings(ctx context.Context, serviceID string) ([]S3Binding, error) {
	return s.queryS3Bindings(ctx, `WHERE b.service_id = ?`, serviceID)
}

func (s *Store) EndpointS3Bindings(ctx context.Context, endpointID string) ([]S3Binding, error) {
	return s.queryS3Bindings(ctx, `WHERE b.endpoint_id = ?`, endpointID)
}

// SetServiceS3Bindings replaces a service's bindings.
func (s *Store) SetServiceS3Bindings(ctx context.Context, serviceID string, bs []S3Binding) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM s3_bindings WHERE service_id = ?`, serviceID); err != nil {
		return err
	}
	for _, b := range bs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO s3_bindings (id, service_id, endpoint_id, bucket, prefix, env_prefix, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			b.ID, serviceID, b.EndpointID, b.Bucket, b.Prefix, b.EnvPrefix, b.CreatedAt.Unix()); isUnique(err) {
			return ErrNameTaken
		} else if err != nil {
			return err
		}
	}
	return tx.Commit()
}
