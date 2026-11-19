// Package dns manages domains and their records (Phase 28). SynCloud manages
// and instructs: with a provider connected (Cloudflare) it writes the
// records itself, without one it shows which records to create and checks
// them live. It is not a name server.
package dns

import (
	"context"
	"errors"
	"strings"
)

// Record is one DNS record as a provider holds it.
type Record struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"` // the full name, no trailing dot
	Content  string `json:"content"`
	TTL      int    `json:"ttl"` // seconds; 1 = automatic
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"` // MX, SRV
	Comment  string `json:"comment,omitempty"`
}

// ZoneInfo is a zone a provider can manage.
type ZoneInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status,omitempty"`
	NameServers []string `json:"nameServers,omitempty"`
}

// Provider is a DNS API. Cloudflare is the first; others (Route 53,
// DigitalOcean, Hetzner) can follow without changing the rest.
type Provider interface {
	// Verify checks that the credentials work.
	Verify(ctx context.Context) error
	Zones(ctx context.Context) ([]ZoneInfo, error)
	Records(ctx context.Context, zoneID string) ([]Record, error)
	Create(ctx context.Context, zoneID string, r Record) (Record, error)
	Update(ctx context.Context, zoneID, id string, r Record) (Record, error)
	Delete(ctx context.Context, zoneID, id string) error
}

// OriginCA is implemented by providers that issue origin certificates
// (Cloudflare): the certificate is signed for a CSR and is trusted only
// behind the provider's proxy.
type OriginCA interface {
	OriginCertificate(ctx context.Context, csrPEM string, hostnames []string, validityDays int) (certPEM string, err error)
}

// Error is a provider failure with the provider's own message.
type Error struct {
	Provider string
	Status   int
	Message  string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Provider + ": request failed"
	}
	return e.Provider + ": " + e.Message
}

// ErrNoProvider means the domain has no provider: its records are manual.
var ErrNoProvider = errors.New("this domain has no DNS provider connected: create the records at your DNS host")

// Types of records the panel handles.
var RecordTypes = []string{"A", "AAAA", "CNAME", "TXT", "MX", "SRV", "CAA", "NS"}

// Fqdn lowercases a name and drops a trailing dot.
func Fqdn(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

// InZone reports whether name is the zone or below it.
func InZone(name, zone string) bool {
	name, zone = Fqdn(name), Fqdn(zone)
	return name == zone || strings.HasSuffix(name, "."+zone)
}
