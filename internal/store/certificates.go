package store

import (
	"context"
	"time"
)

type Certificate struct {
	Host          string
	Issuer        string // acme | self-signed
	CertPEM       string
	KeyEnc        []byte
	NotAfter      time.Time
	Status        string // valid | pending | failed | rate_limited
	LastError     string
	Failures      int
	NextAttemptAt time.Time
	UpdatedAt     time.Time
}

func (s *Store) ListCertificates(ctx context.Context) ([]Certificate, error) {
	rows, err := s.R.QueryContext(ctx,
		`SELECT host, issuer, cert_pem, key_enc, not_after, status, last_error, failures, next_attempt_at, updated_at
		 FROM certificates ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Certificate
	for rows.Next() {
		var c Certificate
		var notAfter, next, updated int64
		if err := rows.Scan(&c.Host, &c.Issuer, &c.CertPEM, &c.KeyEnc, &notAfter, &c.Status, &c.LastError, &c.Failures, &next, &updated); err != nil {
			return nil, err
		}
		c.NotAfter, c.NextAttemptAt, c.UpdatedAt = time.Unix(notAfter, 0).UTC(), time.Unix(next, 0).UTC(), time.Unix(updated, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) PutCertificate(ctx context.Context, c Certificate) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO certificates (host, issuer, cert_pem, key_enc, not_after, status, last_error, failures, next_attempt_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(host) DO UPDATE SET issuer = excluded.issuer, cert_pem = excluded.cert_pem, key_enc = excluded.key_enc,
		   not_after = excluded.not_after, status = excluded.status, last_error = excluded.last_error,
		   failures = excluded.failures, next_attempt_at = excluded.next_attempt_at, updated_at = excluded.updated_at`,
		c.Host, c.Issuer, c.CertPEM, c.KeyEnc, c.NotAfter.Unix(), c.Status, c.LastError, c.Failures, c.NextAttemptAt.Unix(), c.UpdatedAt.Unix())
	return err
}

func (s *Store) DeleteCertificate(ctx context.Context, host string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM certificates WHERE host = ?`, host)
	return err
}
