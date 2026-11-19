package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Domains, DNS providers and who may use which names (Phase 28).

// DNSProvider is a connection to a DNS API. Config is the sealed JSON.
type DNSProvider struct {
	ID        string
	Name      string
	Type      string
	ConfigEnc []byte
	Status    string
	LastError string
	CreatedAt time.Time
}

// Zone is a registered domain.
type Zone struct {
	Name           string
	ProviderID     string
	ProviderZoneID string
	Mode           string // managed | manual
	WildcardPolicy string // off | on
	AuthorityID    string
	// ChallengeZone is the domain whose provider holds this domain's
	// _acme-challenge TXT records (a CNAME delegates them there).
	ChallengeZone string
	// ChallengeBuiltin: answer the challenge with SynCloud's own server even
	// though the domain has a provider (a domain without one uses it anyway
	// while the server is on).
	ChallengeBuiltin bool
	CreatedAt        time.Time
}

// ZoneGrant lets a project (or "*": all projects) use a name and what is below it.
type ZoneGrant struct {
	Zone      string `json:"zone"`
	Project   string `json:"project"`
	Suffix    string `json:"suffix"`
	Wildcards bool   `json:"wildcards"`
}

// ManagedRecord is a record the platform created in a provider's zone.
type ManagedRecord struct {
	ProviderID string
	Zone       string
	RecordID   string
	Type       string
	Name       string
	Content    string
	OwnerKind  string
	OwnerID    string
	Purpose    string
	CreatedAt  time.Time
}

func (s *Store) ListDNSProviders(ctx context.Context) ([]DNSProvider, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, name, type, config_enc, status, last_error, created_at FROM dns_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DNSProvider
	for rows.Next() {
		var p DNSProvider
		var at int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.ConfigEnc, &p.Status, &p.LastError, &at); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DNSProvider(ctx context.Context, id string) (DNSProvider, error) {
	var p DNSProvider
	var at int64
	err := s.R.QueryRowContext(ctx, `SELECT id, name, type, config_enc, status, last_error, created_at FROM dns_providers WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Type, &p.ConfigEnc, &p.Status, &p.LastError, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.CreatedAt = time.Unix(at, 0).UTC()
	return p, err
}

func (s *Store) PutDNSProvider(ctx context.Context, p DNSProvider) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO dns_providers (id, name, type, config_enc, status, last_error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, config_enc = excluded.config_enc, status = excluded.status, last_error = excluded.last_error`,
		p.ID, p.Name, p.Type, p.ConfigEnc, p.Status, p.LastError, p.CreatedAt.Unix())
	return err
}

func (s *Store) SetDNSProviderStatus(ctx context.Context, id, status, lastError string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE dns_providers SET status = ?, last_error = ? WHERE id = ?`, status, lastError, id)
	return err
}

// DeleteDNSProvider removes a provider; its zones stay, as manual ones.
func (s *Store) DeleteDNSProvider(ctx context.Context, id string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE dns_zones SET provider_id = NULL, provider_zone_id = '', mode = 'manual' WHERE provider_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM dns_managed_records WHERE provider_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM dns_providers WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

const zoneCols = `name, COALESCE(provider_id, ''), provider_zone_id, mode, wildcard_policy, authority_id, challenge_zone, challenge_builtin, created_at`

func scanZone(sc interface{ Scan(...any) error }) (Zone, error) {
	var z Zone
	var at int64
	var cb int
	err := sc.Scan(&z.Name, &z.ProviderID, &z.ProviderZoneID, &z.Mode, &z.WildcardPolicy, &z.AuthorityID, &z.ChallengeZone, &cb, &at)
	z.ChallengeBuiltin = cb != 0
	z.CreatedAt = time.Unix(at, 0).UTC()
	return z, err
}

func (s *Store) ListZones(ctx context.Context) ([]Zone, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+zoneCols+` FROM dns_zones ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Zone
	for rows.Next() {
		z, err := scanZone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

func (s *Store) Zone(ctx context.Context, name string) (Zone, error) {
	z, err := scanZone(s.R.QueryRowContext(ctx, `SELECT `+zoneCols+` FROM dns_zones WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return z, ErrNotFound
	}
	return z, err
}

// ErrZoneExists means the domain is already registered.
var ErrZoneExists = errors.New("the domain is already registered")

// CreateZone registers a domain with its first grants, in one step.
func (s *Store) CreateZone(ctx context.Context, z Zone, grants []ZoneGrant) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var provider any
	if z.ProviderID != "" {
		provider = z.ProviderID
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO dns_zones (name, provider_id, provider_zone_id, mode, wildcard_policy, authority_id, challenge_zone, challenge_builtin, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		z.Name, provider, z.ProviderZoneID, z.Mode, z.WildcardPolicy, z.AuthorityID, z.ChallengeZone, boolInt(z.ChallengeBuiltin), z.CreatedAt.Unix()); err != nil {
		if isUnique(err) {
			return ErrZoneExists
		}
		return err
	}
	for _, g := range grants {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO zone_grants (zone, project, suffix, wildcards) VALUES (?, ?, ?, ?)`,
			z.Name, g.Project, g.Suffix, boolInt(g.Wildcards)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateZone changes a domain's provider link, wildcard policy and authority.
func (s *Store) UpdateZone(ctx context.Context, z Zone) error {
	var provider any
	if z.ProviderID != "" {
		provider = z.ProviderID
	}
	res, err := s.W.ExecContext(ctx,
		`UPDATE dns_zones SET provider_id = ?, provider_zone_id = ?, mode = ?, wildcard_policy = ?, authority_id = ?, challenge_zone = ?, challenge_builtin = ? WHERE name = ?`,
		provider, z.ProviderZoneID, z.Mode, z.WildcardPolicy, z.AuthorityID, z.ChallengeZone, boolInt(z.ChallengeBuiltin), z.Name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteZone(ctx context.Context, name string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM dns_managed_records WHERE zone = ?`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM dns_zones WHERE name = ?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListZoneGrants(ctx context.Context, zone string) ([]ZoneGrant, error) {
	q, args := `SELECT zone, project, suffix, wildcards FROM zone_grants ORDER BY zone, suffix, project`, []any(nil)
	if zone != "" {
		q, args = `SELECT zone, project, suffix, wildcards FROM zone_grants WHERE zone = ? ORDER BY suffix, project`, []any{zone}
	}
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ZoneGrant
	for rows.Next() {
		var g ZoneGrant
		var w int
		if err := rows.Scan(&g.Zone, &g.Project, &g.Suffix, &w); err != nil {
			return nil, err
		}
		g.Wildcards = w != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetZoneGrants replaces a domain's grants.
func (s *Store) SetZoneGrants(ctx context.Context, zone string, grants []ZoneGrant) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM zone_grants WHERE zone = ?`, zone); err != nil {
		return err
	}
	for _, g := range grants {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO zone_grants (zone, project, suffix, wildcards) VALUES (?, ?, ?, ?)`,
			zone, g.Project, g.Suffix, boolInt(g.Wildcards)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListManagedRecords(ctx context.Context, providerID, zone string) ([]ManagedRecord, error) {
	rows, err := s.R.QueryContext(ctx,
		`SELECT provider_id, zone, record_id, type, name, content, owner_kind, owner_id, purpose, created_at
		 FROM dns_managed_records WHERE provider_id = ? AND (? = '' OR zone = ?) ORDER BY name, type`, providerID, zone, zone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManagedRecord
	for rows.Next() {
		var m ManagedRecord
		var at int64
		if err := rows.Scan(&m.ProviderID, &m.Zone, &m.RecordID, &m.Type, &m.Name, &m.Content, &m.OwnerKind, &m.OwnerID, &m.Purpose, &at); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) PutManagedRecord(ctx context.Context, m ManagedRecord) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT OR REPLACE INTO dns_managed_records (provider_id, zone, record_id, type, name, content, owner_kind, owner_id, purpose, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ProviderID, m.Zone, m.RecordID, m.Type, m.Name, m.Content, m.OwnerKind, m.OwnerID, m.Purpose, m.CreatedAt.Unix())
	return err
}

func (s *Store) DeleteManagedRecord(ctx context.Context, providerID, recordID string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM dns_managed_records WHERE provider_id = ? AND record_id = ?`, providerID, recordID)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListServiceHosts returns the hosts of every service domain.
func (s *Store) ListServiceHosts(ctx context.Context) ([]string, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT host FROM domains`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ── the built-in challenge server's records ─────────────────────────────────

// AcmeDNSRecord is a TXT value the built-in challenge server answers.
type AcmeDNSRecord struct {
	Name      string
	Value     string
	CreatedAt time.Time
}

func (s *Store) PutAcmeDNSRecord(ctx context.Context, name, value string, at time.Time) error {
	_, err := s.W.ExecContext(ctx, `INSERT OR IGNORE INTO acme_dns_records (name, value, created_at) VALUES (?, ?, ?)`, name, value, at.Unix())
	return err
}

func (s *Store) DeleteAcmeDNSRecord(ctx context.Context, name, value string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM acme_dns_records WHERE name = ? AND value = ?`, name, value)
	return err
}

// AcmeDNSValues returns the TXT values stored at a name.
func (s *Store) AcmeDNSValues(ctx context.Context, name string) ([]string, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT value FROM acme_dns_records WHERE name = ? ORDER BY created_at, value`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AcmeDNSNameExists reports whether any value is stored at a name or below it.
func (s *Store) AcmeDNSNameExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM acme_dns_records WHERE name = ?`, name).Scan(&n)
	return n > 0, err
}

func (s *Store) CountAcmeDNSRecords(ctx context.Context) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM acme_dns_records`).Scan(&n)
	return n, err
}

// PruneAcmeDNSRecords removes values older than the cut-off (orders that never finished).
func (s *Store) PruneAcmeDNSRecords(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM acme_dns_records WHERE created_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
