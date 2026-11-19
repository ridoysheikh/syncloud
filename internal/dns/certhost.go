package dns

import (
	"context"

	"github.com/ridoysheikh/syncloud/internal/certs"
)

// CertHost gives the certificates what they need from the domains.
type CertHost struct{ *Service }

var _ certs.DNSHost = CertHost{}

func (d CertHost) HasTXT(ctx context.Context, name string, values []string) ([]string, error) {
	return HasTXT(ctx, d.LookupResolvers(), name, values)
}

func (d CertHost) ZoneOf(ctx context.Context, host string) (string, string, bool) {
	z, ok := d.ZoneFor(ctx, host)
	return z.Name, z.AuthorityID, ok
}

func (d CertHost) OriginCertificate(ctx context.Context, providerID, csrPEM string, names []string, days int) (string, error) {
	o, err := d.OriginCA(ctx, providerID)
	if err != nil {
		return "", err
	}
	return o.OriginCertificate(ctx, csrPEM, names, days)
}

func (d CertHost) WildcardZones(ctx context.Context) []certs.ZonePolicy {
	zs, err := d.Zones(ctx)
	if err != nil {
		return nil
	}
	var out []certs.ZonePolicy
	for _, z := range zs {
		if z.WildcardPolicy == "on" {
			out = append(out, certs.ZonePolicy{Zone: z.Name, AuthorityID: z.AuthorityID})
		}
	}
	return out
}
