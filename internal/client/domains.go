package client

import (
	"context"
	"net/url"
	"time"
)

// Domains, DNS records and certificates (Phase 28): what an app that
// manages its own names and certificates calls.

// Domain is a registered domain.
type Domain struct {
	Name           string   `json:"name"`
	Mode           string   `json:"mode"` // managed | manual
	Provider       string   `json:"provider"`
	NameServers    []string `json:"nameServers"`
	WildcardPolicy string   `json:"wildcardPolicy"`
	// ChallengeZone is the domain whose provider holds this domain's
	// _acme-challenge records (a CNAME delegates them there).
	ChallengeZone    string `json:"challengeZone"`
	ChallengeBuiltin bool   `json:"challengeBuiltin"`
	Grants           []struct {
		Project   string `json:"project"`
		Suffix    string `json:"suffix"`
		Wildcards bool   `json:"wildcards"`
	} `json:"grants"`
	Use struct {
		Services     int `json:"services"`
		Certificates int `json:"certificates"`
	} `json:"use"`
}

// DomainRecord is a record of a managed domain.
type DomainRecord struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl,omitempty"`
	Proxied  bool   `json:"proxied,omitempty"`
	Priority *int   `json:"priority,omitempty"`
	Managed  bool   `json:"managed,omitempty"` // created by SynCloud
}

// DNSPlan is what pointing a name at the cluster does (or would do).
type DNSPlan struct {
	Plan struct {
		Steps []struct {
			Action       string       `json:"action"`
			Reason       string       `json:"reason"`
			Record       DomainRecord `json:"record"`
			NeedsConfirm bool         `json:"needsConfirm"`
		} `json:"steps"`
	} `json:"plan"`
	Applied *struct{ Created, Updated, Deleted int } `json:"applied"`
	Manual  []DomainRecord                           `json:"manual"`
}

// CertificateInfo is a certificate as the API shows it (never its key).
type CertificateInfo struct {
	ID          string    `json:"id"`
	Names       []string  `json:"names"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	Method      string    `json:"method"`
	Authority   string    `json:"authority"`
	AuthorityID string    `json:"authorityId"`
	KeyType     string    `json:"keyType"`
	AutoRenew   bool      `json:"autoRenew"`
	NotAfter    time.Time `json:"notAfter"`
	DaysLeft    int       `json:"daysLeft"`
	Wildcard    bool      `json:"wildcard"`
	Zone        string    `json:"zone"`
	Fingerprint string    `json:"fingerprint"`
	LastError   string    `json:"lastError"`
	UsedBy      []string  `json:"usedBy"`
	Order       *struct {
		State      string `json:"state"`
		Challenges []struct {
			Name    string `json:"name"`
			Value   string `json:"value"`
			Auto    bool   `json:"auto"`
			Present bool   `json:"present"`
			Mode    string `json:"mode"`
			Target  string `json:"target"`
			CNAMEOK bool   `json:"cnameOk"`
		} `json:"challenges"`
	} `json:"order"`
}

// CertificateRequestInput asks for a certificate.
type CertificateRequestInput struct {
	Names     []string `json:"names"`
	Authority string   `json:"authority,omitempty"`
	Method    string   `json:"method,omitempty"`
	KeyType   string   `json:"keyType,omitempty"`
	Fallback  string   `json:"fallback,omitempty"`
	AutoRenew *bool    `json:"autoRenew,omitempty"`
}

// CertificateImportInput brings in a certificate from a provider.
type CertificateImportInput struct {
	Chain      string `json:"chain,omitempty"`
	Key        string `json:"key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Replace    string `json:"replace,omitempty"`
}

func (c *Client) ListDomains(ctx context.Context) ([]Domain, error) {
	var out list[Domain]
	return out.Items, c.Do(ctx, "GET", "/api/v1/domains", nil, &out)
}

func (c *Client) GetDomain(ctx context.Context, name string) (Domain, error) {
	var out Domain
	return out, c.Do(ctx, "GET", "/api/v1/domains/"+url.PathEscape(name), nil, &out)
}

func (c *Client) ListDomainRecords(ctx context.Context, zone string, q url.Values) ([]DomainRecord, error) {
	var out list[DomainRecord]
	return out.Items, c.Do(ctx, "GET", "/api/v1/domains/"+url.PathEscape(zone)+"/records?"+q.Encode(), nil, &out)
}

// CreateDomainRecord adds a record; name may be a label, the full name or "@".
func (c *Client) CreateDomainRecord(ctx context.Context, zone string, r DomainRecord) (DomainRecord, error) {
	var out DomainRecord
	return out, c.Do(ctx, "POST", "/api/v1/domains/"+url.PathEscape(zone)+"/records", r, &out)
}

func (c *Client) UpdateDomainRecord(ctx context.Context, zone, id string, r DomainRecord) (DomainRecord, error) {
	var out DomainRecord
	return out, c.Do(ctx, "PUT", "/api/v1/domains/"+url.PathEscape(zone)+"/records/"+url.PathEscape(id), r, &out)
}

func (c *Client) DeleteDomainRecord(ctx context.Context, zone, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/domains/"+url.PathEscape(zone)+"/records/"+url.PathEscape(id), nil, nil)
}

// PointDomain makes a name reach the cluster (or the targets given), without duplicates.
// plan only previews; replaceConflicts also removes records SynCloud did not create.
func (c *Client) PointDomain(ctx context.Context, zone, name string, targets []string, plan, replaceConflicts bool) (DNSPlan, error) {
	q := url.Values{}
	if plan {
		q.Set("plan", "true")
	}
	if replaceConflicts {
		q.Set("replace", "conflicts")
	}
	var out DNSPlan
	return out, c.Do(ctx, "POST", "/api/v1/domains/"+url.PathEscape(zone)+"/records/point?"+q.Encode(), map[string]any{"name": name, "targets": targets}, &out)
}

// ChallengeRoute says where the _acme-challenge TXT record of a name goes:
// written at the name (direct), written where a CNAME delegates it, or by hand.
type ChallengeRoute struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Zone       string `json:"zone"`
	Mode       string `json:"mode"` // direct | delegated | builtin | manual
	Auto       bool   `json:"auto"`
	Target     string `json:"target"`
	TargetZone string `json:"targetZone"`
	CNAME      *struct {
		Name    string   `json:"name"`
		Target  string   `json:"target"`
		Present bool     `json:"present"`
		Seen    []string `json:"seen"`
		Managed bool     `json:"managed"`
	} `json:"cname"`
	Note string `json:"note"`
}

// DomainChallenge shows where the _acme-challenge record of a name goes (name
// "" is the domain itself; a wildcard is written *.name).
func (c *Client) DomainChallenge(ctx context.Context, zone, name string) (ChallengeRoute, error) {
	var out ChallengeRoute
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	return out, c.Do(ctx, "GET", "/api/v1/domains/"+url.PathEscape(zone)+"/challenge?"+q.Encode(), nil, &out)
}

// DelegateDomainChallenge creates the CNAME that delegates a name's
// _acme-challenge to the domain's validation domain, when the domain has a
// provider. replaceConflicts also removes other records on that name.
func (c *Client) DelegateDomainChallenge(ctx context.Context, zone, name string, replaceConflicts bool) (ChallengeRoute, error) {
	var out ChallengeRoute
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	if replaceConflicts {
		q.Set("replace", "conflicts")
	}
	return out, c.Do(ctx, "POST", "/api/v1/domains/"+url.PathEscape(zone)+"/challenge/delegation?"+q.Encode(), nil, &out)
}

func (c *Client) RequestCertificate(ctx context.Context, in CertificateRequestInput) (CertificateInfo, error) {
	var out CertificateInfo
	return out, c.Do(ctx, "POST", "/api/v1/certificates", in, &out)
}

func (c *Client) GetCertificate(ctx context.Context, id string) (CertificateInfo, error) {
	var out CertificateInfo
	return out, c.Do(ctx, "GET", "/api/v1/certificates/"+url.PathEscape(id), nil, &out)
}

func (c *Client) VerifyCertificate(ctx context.Context, id string) (CertificateInfo, error) {
	var out CertificateInfo
	return out, c.Do(ctx, "POST", "/api/v1/certificates/"+url.PathEscape(id)+"/verify", nil, &out)
}

func (c *Client) RevokeCertificate(ctx context.Context, id string) (CertificateInfo, error) {
	var out CertificateInfo
	return out, c.Do(ctx, "POST", "/api/v1/certificates/"+url.PathEscape(id)+"/revoke", nil, &out)
}

func (c *Client) DeleteCertificate(ctx context.Context, id string, force bool) error {
	path := "/api/v1/certificates/" + url.PathEscape(id)
	if force {
		path += "?force=true"
	}
	return c.Do(ctx, "DELETE", path, nil, nil)
}

func (c *Client) ImportCertificate(ctx context.Context, in CertificateImportInput) (CertificateInfo, []string, error) {
	var out struct {
		Certificate CertificateInfo `json:"certificate"`
		Warnings    []string        `json:"warnings"`
	}
	err := c.Do(ctx, "POST", "/api/v1/certificates/import", in, &out)
	return out.Certificate, out.Warnings, err
}

// ExportCertificate returns the chain and the PRIVATE KEY. It needs the
// certificate:ExportCertificate permission, which nobody has until it is granted.
func (c *Client) ExportCertificate(ctx context.Context, id string) (chain, privateKey string, err error) {
	var out struct {
		Certificate string `json:"certificate"`
		PrivateKey  string `json:"privateKey"`
	}
	err = c.Do(ctx, "GET", "/api/v1/certificates/"+url.PathEscape(id)+"/bundle", nil, &out)
	return out.Certificate, out.PrivateKey, err
}

// ChallengeServer is SynCloud's own ACME challenge DNS server.
type ChallengeServer struct {
	Enabled   bool   `json:"enabled"`
	Zone      string `json:"zone"`
	NS        string `json:"ns"`
	IP        string `json:"ip"`
	Listen    string `json:"listen"`
	Listening bool   `json:"listening"`
	// Addresses it listens on: Listen, or each address that is not loopback
	// when another server (systemd-resolved) holds the port on one.
	Addresses []string `json:"addresses"`
	Error     string   `json:"error"`
	Records   int      `json:"records"`
	// Sslip: the zone is _acme-challenge.<ip>.sslip.io, which sslip.io
	// delegates to the controller (nothing to create at any DNS host).
	Sslip bool `json:"sslip"`
	// Domains are the registered domains whose challenges it answers.
	Domains []string `json:"domains"`
}

func (c *Client) ChallengeServer(ctx context.Context) (ChallengeServer, error) {
	var out ChallengeServer
	return out, c.Do(ctx, "GET", "/api/v1/domains/challenge-server", nil, &out)
}

// SetChallengeServer turns the server on (zone _acme-challenge.<public
// ip>.sslip.io, nothing to create at any DNS host) or off. ip overrides the
// detected address and is then kept when the address changes.
func (c *Client) SetChallengeServer(ctx context.Context, enabled bool, ip string) (ChallengeServer, error) {
	var out ChallengeServer
	return out, c.Do(ctx, "PUT", "/api/v1/domains/challenge-server", map[string]any{"enabled": enabled, "ip": ip}, &out)
}
