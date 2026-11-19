package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Certificate is one stored certificate. Host is its key: the hostname of an
// automatic certificate, or a generated id ("cert_…") for one a person asked
// for (Phase 28), whose names are in Names.
type Certificate struct {
	Host          string
	Issuer        string // acme | self-signed (as before)
	CertPEM       string
	KeyEnc        []byte
	NotAfter      time.Time
	Status        string // valid | pending | failed | rate_limited | self_signed | awaiting_dns | verifying | revoked
	LastError     string
	Failures      int
	NextAttemptAt time.Time
	UpdatedAt     time.Time

	// Phase 28.
	Names       string // JSON list; "" means [Host]
	Method      string // http-01 | dns-01 | dns-01-manual | import | origin-ca | self-signed
	Zone        string
	Source      string // acme | imported | origin-ca | self-signed
	Managed     bool   // asked for by a person, not by a route
	AuthorityID string
	FallbackID  string
	KeyType     string
	AutoRenew   bool
	NotBefore   time.Time
	Serial      string
	Fingerprint string
	RevokedAt   time.Time
	AlertDays   string // JSON list; "" means 30,14,7,1
	PinnedHosts string // JSON list
	CreatedAt   time.Time
	IssuerName  string
}

const certCols = `host, issuer, cert_pem, key_enc, not_after, status, last_error, failures, next_attempt_at, updated_at,
	names, method, zone, source, managed, authority_id, fallback_id, key_type, auto_renew, not_before, serial, fingerprint,
	revoked_at, alert_days, pinned_hosts, created_at, issuer_name`

func scanCert(sc interface{ Scan(...any) error }) (Certificate, error) {
	var c Certificate
	var notAfter, next, updated, notBefore, revoked, created int64
	var managed, auto int
	err := sc.Scan(&c.Host, &c.Issuer, &c.CertPEM, &c.KeyEnc, &notAfter, &c.Status, &c.LastError, &c.Failures, &next, &updated,
		&c.Names, &c.Method, &c.Zone, &c.Source, &managed, &c.AuthorityID, &c.FallbackID, &c.KeyType, &auto, &notBefore, &c.Serial, &c.Fingerprint,
		&revoked, &c.AlertDays, &c.PinnedHosts, &created, &c.IssuerName)
	c.NotAfter, c.NextAttemptAt, c.UpdatedAt = time.Unix(notAfter, 0).UTC(), time.Unix(next, 0).UTC(), time.Unix(updated, 0).UTC()
	c.NotBefore, c.RevokedAt, c.CreatedAt = time.Unix(notBefore, 0).UTC(), time.Unix(revoked, 0).UTC(), time.Unix(created, 0).UTC()
	c.Managed, c.AutoRenew = managed != 0, auto != 0
	return c, err
}

func (s *Store) ListCertificates(ctx context.Context) ([]Certificate, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+certCols+` FROM certificates ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Certificate
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) PutCertificate(ctx context.Context, c Certificate) error {
	if c.Method == "" {
		c.Method = "http-01"
	}
	if c.Source == "" {
		c.Source = "acme"
	}
	if c.CreatedAt.IsZero() || c.CreatedAt.Unix() <= 0 {
		c.CreatedAt = c.UpdatedAt
	}
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO certificates (`+certCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(host) DO UPDATE SET issuer = excluded.issuer, cert_pem = excluded.cert_pem, key_enc = excluded.key_enc,
		   not_after = excluded.not_after, status = excluded.status, last_error = excluded.last_error,
		   failures = excluded.failures, next_attempt_at = excluded.next_attempt_at, updated_at = excluded.updated_at,
		   names = excluded.names, method = excluded.method, zone = excluded.zone, source = excluded.source, managed = excluded.managed,
		   authority_id = excluded.authority_id, fallback_id = excluded.fallback_id, key_type = excluded.key_type,
		   auto_renew = excluded.auto_renew, not_before = excluded.not_before, serial = excluded.serial,
		   fingerprint = excluded.fingerprint, revoked_at = excluded.revoked_at, alert_days = excluded.alert_days,
		   pinned_hosts = excluded.pinned_hosts, issuer_name = excluded.issuer_name`,
		c.Host, c.Issuer, c.CertPEM, c.KeyEnc, c.NotAfter.Unix(), c.Status, c.LastError, c.Failures, c.NextAttemptAt.Unix(), c.UpdatedAt.Unix(),
		c.Names, c.Method, c.Zone, c.Source, boolInt(c.Managed), c.AuthorityID, c.FallbackID, c.KeyType, boolInt(c.AutoRenew),
		c.NotBefore.Unix(), c.Serial, c.Fingerprint, c.RevokedAt.Unix(), c.AlertDays, c.PinnedHosts, c.CreatedAt.Unix(), c.IssuerName)
	return err
}

func (s *Store) DeleteCertificate(ctx context.Context, host string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM certificates WHERE host = ?`,
		`DELETE FROM cert_orders WHERE cert_key = ?`,
		`DELETE FROM cert_events WHERE cert_key = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, host); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ── authorities ─────────────────────────────────────────────────────────────

// CertAuthority is a certificate authority added by the user.
type CertAuthority struct {
	ID            string
	Name          string
	Kind          string // acme | cloudflare-origin
	Preset        string
	DirectoryURL  string
	Email         string
	EABKid        string
	EABHMACEnc    []byte
	AccountKeyEnc []byte
	KeyType       string
	ProviderID    string
	Status        string
	LastError     string
	CreatedAt     time.Time
}

const authCols = `id, name, kind, preset, directory_url, email, eab_kid, eab_hmac_enc, account_key_enc, key_type, provider_id, status, last_error, created_at`

func scanAuthority(sc interface{ Scan(...any) error }) (CertAuthority, error) {
	var a CertAuthority
	var at int64
	err := sc.Scan(&a.ID, &a.Name, &a.Kind, &a.Preset, &a.DirectoryURL, &a.Email, &a.EABKid, &a.EABHMACEnc, &a.AccountKeyEnc, &a.KeyType, &a.ProviderID, &a.Status, &a.LastError, &at)
	a.CreatedAt = time.Unix(at, 0).UTC()
	return a, err
}

func (s *Store) ListCertAuthorities(ctx context.Context) ([]CertAuthority, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+authCols+` FROM cert_authorities ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CertAuthority
	for rows.Next() {
		a, err := scanAuthority(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CertAuthority(ctx context.Context, id string) (CertAuthority, error) {
	a, err := scanAuthority(s.R.QueryRowContext(ctx, `SELECT `+authCols+` FROM cert_authorities WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Store) PutCertAuthority(ctx context.Context, a CertAuthority) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO cert_authorities (`+authCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, directory_url = excluded.directory_url, email = excluded.email,
		   eab_kid = excluded.eab_kid, eab_hmac_enc = excluded.eab_hmac_enc, account_key_enc = excluded.account_key_enc,
		   key_type = excluded.key_type, provider_id = excluded.provider_id, status = excluded.status, last_error = excluded.last_error`,
		a.ID, a.Name, a.Kind, a.Preset, a.DirectoryURL, a.Email, a.EABKid, a.EABHMACEnc, a.AccountKeyEnc, a.KeyType, a.ProviderID, a.Status, a.LastError, a.CreatedAt.Unix())
	if isUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) DeleteCertAuthority(ctx context.Context, id string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM cert_authorities WHERE id = ?`, id)
	return err
}

// ── history, orders and requests ────────────────────────────────────────────

// CertEvent is one entry of a certificate's history.
type CertEvent struct {
	ID      int64     `json:"id"`
	CertKey string    `json:"certificate"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Detail  string    `json:"detail"`
	Actor   string    `json:"actor"`
}

// AddCertEvent records an event and drops the entries older than a year.
func (s *Store) AddCertEvent(ctx context.Context, e CertEvent) error {
	if _, err := s.W.ExecContext(ctx, `INSERT INTO cert_events (cert_key, at, kind, detail, actor) VALUES (?, ?, ?, ?, ?)`,
		e.CertKey, e.At.Unix(), e.Kind, e.Detail, e.Actor); err != nil {
		return err
	}
	_, err := s.W.ExecContext(ctx, `DELETE FROM cert_events WHERE at < ?`, e.At.Add(-366*24*time.Hour).Unix())
	return err
}

// ListCertEvents returns a certificate's history, newest first.
func (s *Store) ListCertEvents(ctx context.Context, key string, limit int) ([]CertEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.R.QueryContext(ctx, `SELECT id, cert_key, at, kind, detail, actor FROM cert_events WHERE cert_key = ? ORDER BY at DESC, id DESC LIMIT ?`, key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CertEvent{}
	for rows.Next() {
		var e CertEvent
		var at int64
		if err := rows.Scan(&e.ID, &e.CertKey, &at, &e.Kind, &e.Detail, &e.Actor); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// CertOrder is an ACME order in progress.
type CertOrder struct {
	CertKey       string
	AuthorityID   string
	OrderURI      string
	State         string
	Challenges    string // JSON
	KeyEnc        []byte
	CreatedAt     time.Time
	ExpiresAt     time.Time
	LastCheckedAt time.Time
	LastError     string
}

func (s *Store) ListCertOrders(ctx context.Context) ([]CertOrder, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT cert_key, authority_id, order_uri, state, challenges, key_enc, created_at, expires_at, last_checked_at, last_error FROM cert_orders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CertOrder
	for rows.Next() {
		var o CertOrder
		var c, e, l int64
		if err := rows.Scan(&o.CertKey, &o.AuthorityID, &o.OrderURI, &o.State, &o.Challenges, &o.KeyEnc, &c, &e, &l, &o.LastError); err != nil {
			return nil, err
		}
		o.CreatedAt, o.ExpiresAt, o.LastCheckedAt = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC(), time.Unix(l, 0).UTC()
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) PutCertOrder(ctx context.Context, o CertOrder) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO cert_orders (cert_key, authority_id, order_uri, state, challenges, key_enc, created_at, expires_at, last_checked_at, last_error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(cert_key) DO UPDATE SET authority_id = excluded.authority_id, order_uri = excluded.order_uri, state = excluded.state,
		   challenges = excluded.challenges, key_enc = excluded.key_enc, expires_at = excluded.expires_at,
		   last_checked_at = excluded.last_checked_at, last_error = excluded.last_error`,
		o.CertKey, o.AuthorityID, o.OrderURI, o.State, o.Challenges, o.KeyEnc, o.CreatedAt.Unix(), o.ExpiresAt.Unix(), o.LastCheckedAt.Unix(), o.LastError)
	return err
}

func (s *Store) DeleteCertOrder(ctx context.Context, key string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM cert_orders WHERE cert_key = ?`, key)
	return err
}

// CertRequest is a CSR waiting for its signed certificate.
type CertRequest struct {
	ID        string
	Names     string // JSON
	KeyType   string
	KeyEnc    []byte
	CSRPEM    string
	CreatedAt time.Time
}

func (s *Store) PutCertRequest(ctx context.Context, r CertRequest) error {
	_, err := s.W.ExecContext(ctx, `INSERT OR REPLACE INTO cert_requests (id, names, key_type, key_enc, csr_pem, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.Names, r.KeyType, r.KeyEnc, r.CSRPEM, r.CreatedAt.Unix())
	return err
}

func (s *Store) CertRequest(ctx context.Context, id string) (CertRequest, error) {
	var r CertRequest
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT id, names, key_type, key_enc, csr_pem, created_at FROM cert_requests WHERE id = ?`, id).
		Scan(&r.ID, &r.Names, &r.KeyType, &r.KeyEnc, &r.CSRPEM, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.CreatedAt = time.Unix(at, 0).UTC()
	return r, err
}

func (s *Store) ListCertRequests(ctx context.Context) ([]CertRequest, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, names, key_type, key_enc, csr_pem, created_at FROM cert_requests ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CertRequest
	for rows.Next() {
		var r CertRequest
		var at int64
		if err := rows.Scan(&r.ID, &r.Names, &r.KeyType, &r.KeyEnc, &r.CSRPEM, &at); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCertRequest(ctx context.Context, id string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM cert_requests WHERE id = ?`, id)
	return err
}
