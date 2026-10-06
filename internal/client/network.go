package client

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// SecurityRule allows traffic to (inbound) or from (outbound) a group's
// members (§8.3).
type SecurityRule struct {
	Protocol    string   `json:"protocol"`
	Ports       string   `json:"ports"`
	Peers       []string `json:"peers"`
	Description string   `json:"description"`
	Packets     uint64   `json:"packets,omitempty"`
	Bytes       uint64   `json:"bytes,omitempty"`
}

type SecurityGroup struct {
	ID          string         `json:"id,omitempty"`
	Project     string         `json:"project,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Default     bool           `json:"default,omitempty"`
	Inbound     []SecurityRule `json:"inbound"`
	Outbound    []SecurityRule `json:"outbound"`
	Services    []string       `json:"services"`
	Members     []string       `json:"members,omitempty"`
	CreatedAt   *time.Time     `json:"createdAt,omitempty"`
	UpdatedAt   *time.Time     `json:"updatedAt,omitempty"`
}

func sgPath(project string) string {
	return "/api/v1/projects/" + url.PathEscape(project) + "/security-groups"
}

func (c *Client) ListAllSecurityGroups(ctx context.Context) ([]SecurityGroup, map[string]string, error) {
	var out struct {
		Items      []SecurityGroup   `json:"items"`
		NodeErrors map[string]string `json:"nodeErrors"`
	}
	return out.Items, out.NodeErrors, c.Do(ctx, "GET", "/api/v1/security-groups", nil, &out)
}

func (c *Client) ListSecurityGroups(ctx context.Context, project string) ([]SecurityGroup, error) {
	var out list[SecurityGroup]
	return out.Items, c.Do(ctx, "GET", sgPath(project), nil, &out)
}

func (c *Client) GetSecurityGroup(ctx context.Context, project, name string) (SecurityGroup, error) {
	var out SecurityGroup
	return out, c.Do(ctx, "GET", sgPath(project)+"/"+url.PathEscape(name), nil, &out)
}

// securityGroupBody drops the read-only fields.
func securityGroupBody(g SecurityGroup) SecurityGroup {
	return SecurityGroup{Name: g.Name, Description: g.Description, Inbound: g.Inbound, Outbound: g.Outbound, Services: g.Services}
}

func (c *Client) CreateSecurityGroup(ctx context.Context, project string, g SecurityGroup) (SecurityGroup, error) {
	var out SecurityGroup
	return out, c.Do(ctx, "POST", sgPath(project), securityGroupBody(g), &out)
}

func (c *Client) UpdateSecurityGroup(ctx context.Context, project, name string, g SecurityGroup) (SecurityGroup, error) {
	var out SecurityGroup
	return out, c.Do(ctx, "PUT", sgPath(project)+"/"+url.PathEscape(name), securityGroupBody(g), &out)
}

func (c *Client) DeleteSecurityGroup(ctx context.Context, project, name string) error {
	return c.Do(ctx, "DELETE", sgPath(project)+"/"+url.PathEscape(name), nil, nil)
}

// PreviewChange is one service whose effective rules would change.
type PreviewChange struct {
	Service string   `json:"service"`
	Lines   []string `json:"lines"`
	Tasks   int      `json:"tasks"`
	Nodes   []string `json:"nodes"`
}

// PreviewSecurityGroup shows what saving g would change; existing names the
// group being edited ("" for a new one).
func (c *Client) PreviewSecurityGroup(ctx context.Context, project, existing string, g SecurityGroup) ([]PreviewChange, error) {
	body := struct {
		SecurityGroup
		Existing string `json:"existing"`
	}{securityGroupBody(g), existing}
	var out struct {
		Changes []PreviewChange `json:"changes"`
	}
	return out.Changes, c.Do(ctx, "POST", sgPath(project)+"/preview", body, &out)
}

type ServiceSecurity struct {
	Groups []SecurityGroup `json:"groups"`
	Lines  []string        `json:"lines"`
}

func (c *Client) ServiceSecurity(ctx context.Context, project, env, service string) (ServiceSecurity, error) {
	var out ServiceSecurity
	return out, c.Do(ctx, "GET", svcPath(project, env, service)+"/security", nil, &out)
}

type RuleMatch struct {
	GroupID   string       `json:"groupId"`
	Group     string       `json:"group"`
	Direction string       `json:"direction"`
	Index     int          `json:"index"`
	Rule      SecurityRule `json:"rule"`
}

type Reachability struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Verdict  struct {
		Allowed bool       `json:"allowed"`
		Reason  string     `json:"reason"`
		Egress  *RuleMatch `json:"egress"`
		Ingress *RuleMatch `json:"ingress"`
	} `json:"verdict"`
}

func (c *Client) CheckReachability(ctx context.Context, from, to, protocol string, port int) (Reachability, error) {
	var out Reachability
	return out, c.Do(ctx, "POST", "/api/v1/network/reachability", map[string]any{"from": from, "to": to, "protocol": protocol, "port": port}, &out)
}

type FirewallDrop struct {
	At        time.Time `json:"at"`
	Node      string    `json:"node"`
	Direction string    `json:"direction"`
	Src       string    `json:"src"`
	SrcName   string    `json:"srcName"`
	Dst       string    `json:"dst"`
	DstName   string    `json:"dstName"`
	Protocol  string    `json:"protocol"`
	Port      int       `json:"port"`
	Packets   uint64    `json:"packets"`
}

func (c *Client) FirewallDrops(ctx context.Context, since, node, direction, ip string) ([]FirewallDrop, error) {
	q := url.Values{}
	for k, v := range map[string]string{"since": since, "node": node, "direction": direction, "ip": ip} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var out list[FirewallDrop]
	return out.Items, c.Do(ctx, "GET", "/api/v1/firewall/drops?"+q.Encode(), nil, &out)
}

type RuleCounter struct {
	ID      string `json:"id"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

func (c *Client) FirewallCounters(ctx context.Context, node string) ([]RuleCounter, error) {
	p := "/api/v1/firewall/counters"
	if node != "" {
		p += "?node=" + url.QueryEscape(node)
	}
	var out list[RuleCounter]
	return out.Items, c.Do(ctx, "GET", p, nil, &out)
}

type IPAMAddress struct {
	IP      string    `json:"ip"`
	Owner   string    `json:"owner"`
	OwnerID string    `json:"ownerId"`
	Kind    string    `json:"kind"`
	Since   time.Time `json:"since"`
}

type IPAM struct {
	MeshCIDR      string `json:"meshCidr"`
	ContainerCIDR string `json:"containerCidr"`
	ServiceCIDR   string `json:"serviceCidr"`
	Nodes         []struct {
		Node      string        `json:"node"`
		MeshIP    string        `json:"meshIp"`
		Subnet    string        `json:"subnet"`
		Used      int           `json:"used"`
		Capacity  int           `json:"capacity"`
		Addresses []IPAMAddress `json:"addresses"`
	} `json:"nodes"`
	VIPs []struct {
		Service  string `json:"service"`
		VIP      string `json:"vip"`
		DNSName  string `json:"dnsName"`
		Backends int    `json:"backends"`
	} `json:"vips"`
	Released []struct {
		Kind       string    `json:"kind"`
		Address    string    `json:"address"`
		ReusableAt time.Time `json:"reusableAt"`
	} `json:"released"`
}

func (c *Client) GetIPAM(ctx context.Context) (IPAM, error) {
	var out IPAM
	return out, c.Do(ctx, "GET", "/api/v1/network/ipam", nil, &out)
}

type AddressRecord struct {
	IP         string     `json:"ip"`
	OwnerID    string     `json:"ownerId"`
	Owner      string     `json:"owner"`
	AssignedAt time.Time  `json:"assignedAt"`
	ReleasedAt *time.Time `json:"releasedAt"`
}

func (c *Client) IPHistory(ctx context.Context, ip string) ([]AddressRecord, error) {
	var out list[AddressRecord]
	return out.Items, c.Do(ctx, "GET", "/api/v1/network/ipam/history?ip="+url.QueryEscape(ip), nil, &out)
}

type DNSRecord struct {
	Name string   `json:"name"`
	Kind string   `json:"kind"`
	IPs  []string `json:"ips"`
}

func (c *Client) DNSRecords(ctx context.Context) ([]DNSRecord, error) {
	var out list[DNSRecord]
	return out.Items, c.Do(ctx, "GET", "/api/v1/network/dns", nil, &out)
}

type DNSAnswer struct {
	Name   string   `json:"name"`
	Found  bool     `json:"found"`
	Source string   `json:"source"`
	Kind   string   `json:"kind"`
	IPs    []string `json:"ips"`
	Error  string   `json:"error"`
}

func (c *Client) DNSLookup(ctx context.Context, name string) (DNSAnswer, error) {
	var out DNSAnswer
	return out, c.Do(ctx, "GET", "/api/v1/network/dns/lookup?name="+url.QueryEscape(name), nil, &out)
}

type Talker struct {
	Key   string  `json:"key"`
	Node  string  `json:"node"`
	Owner string  `json:"owner"`
	RxBps float64 `json:"rxBps"`
	TxBps float64 `json:"txBps"`
}

type NetworkThroughput struct {
	TopServices []Talker `json:"topServices"`
	TopTasks    []Talker `json:"topTasks"`
}

func (c *Client) NetworkThroughput(ctx context.Context, rng string) (NetworkThroughput, error) {
	var out NetworkThroughput
	return out, c.Do(ctx, "GET", "/api/v1/network/throughput?range="+url.QueryEscape(rng), nil, &out)
}

type Middleware struct {
	ID       string          `json:"id,omitempty"`
	Project  string          `json:"project,omitempty"`
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Config   json.RawMessage `json:"config,omitempty"`
	Services []string        `json:"services"`
}

func (c *Client) ListAllMiddlewares(ctx context.Context) ([]Middleware, error) {
	var out list[Middleware]
	return out.Items, c.Do(ctx, "GET", "/api/v1/middlewares", nil, &out)
}

func (c *Client) ListMiddlewares(ctx context.Context, project string) ([]Middleware, error) {
	var out list[Middleware]
	return out.Items, c.Do(ctx, "GET", "/api/v1/projects/"+url.PathEscape(project)+"/middlewares", nil, &out)
}

func (c *Client) CreateMiddleware(ctx context.Context, project string, m Middleware) (Middleware, error) {
	var out Middleware
	return out, c.Do(ctx, "POST", "/api/v1/projects/"+url.PathEscape(project)+"/middlewares", m, &out)
}

func (c *Client) UpdateMiddleware(ctx context.Context, project, name string, m Middleware) (Middleware, error) {
	var out Middleware
	return out, c.Do(ctx, "PUT", "/api/v1/projects/"+url.PathEscape(project)+"/middlewares/"+url.PathEscape(name), m, &out)
}

func (c *Client) DeleteMiddleware(ctx context.Context, project, name string) error {
	return c.Do(ctx, "DELETE", "/api/v1/projects/"+url.PathEscape(project)+"/middlewares/"+url.PathEscape(name), nil, nil)
}

func (c *Client) TraefikConfig(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.Do(ctx, "GET", "/api/v1/traefik/config", nil, &out)
}

func (c *Client) TraefikCustom(ctx context.Context) (string, error) {
	var out struct {
		YAML string `json:"yaml"`
	}
	return out.YAML, c.Do(ctx, "GET", "/api/v1/traefik/custom", nil, &out)
}

func (c *Client) PutTraefikCustom(ctx context.Context, yaml string) error {
	return c.Do(ctx, "PUT", "/api/v1/traefik/custom", map[string]string{"yaml": yaml}, nil)
}

func (c *Client) ValidateTraefikCustom(ctx context.Context, yaml string) error {
	return c.Do(ctx, "POST", "/api/v1/traefik/custom/validate", map[string]string{"yaml": yaml}, nil)
}

// TraefikSettings is GET /traefik/settings, kept loose so the CLI can set
// any field by name.
func (c *Client) TraefikSettings(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.Do(ctx, "GET", "/api/v1/traefik/settings", nil, &out)
}

func (c *Client) UpdateTraefikSettings(ctx context.Context, settings map[string]any) (map[string]any, error) {
	var out map[string]any
	return out, c.Do(ctx, "PUT", "/api/v1/traefik/settings", settings, &out)
}

// GitConnection is a connected Git provider (§5.8).
type GitConnection struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	APIURL        string    `json:"apiUrl"`
	WebURL        string    `json:"webUrl"`
	Account       string    `json:"account"`
	AppSlug       string    `json:"appSlug,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	InstallURL    string    `json:"installUrl,omitempty"`
	Services      []string  `json:"services"`
	Installations []string  `json:"installations,omitempty"`
	Problem       string    `json:"problem,omitempty"`
}

type GitRepo struct {
	FullName      string `json:"fullName"`
	CloneURL      string `json:"cloneUrl"`
	WebURL        string `json:"webUrl"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
}

func (c *Client) ListGitConnections(ctx context.Context) ([]GitConnection, error) {
	var out list[GitConnection]
	return out.Items, c.Do(ctx, "GET", "/api/v1/integrations/git", nil, &out)
}

func (c *Client) CreateGitConnection(ctx context.Context, kind, name, serverURL, token string) (GitConnection, error) {
	var out GitConnection
	return out, c.Do(ctx, "POST", "/api/v1/integrations/git", map[string]string{"kind": kind, "name": name, "url": serverURL, "token": token}, &out)
}

func (c *Client) GetGitConnection(ctx context.Context, name string) (GitConnection, error) {
	var out GitConnection
	return out, c.Do(ctx, "GET", "/api/v1/integrations/git/"+url.PathEscape(name), nil, &out)
}

func (c *Client) DeleteGitConnection(ctx context.Context, name string) error {
	return c.Do(ctx, "DELETE", "/api/v1/integrations/git/"+url.PathEscape(name), nil, nil)
}

func (c *Client) ListGitRepos(ctx context.Context, name, query string) ([]GitRepo, error) {
	var out list[GitRepo]
	return out.Items, c.Do(ctx, "GET", "/api/v1/integrations/git/"+url.PathEscape(name)+"/repos?q="+url.QueryEscape(query), nil, &out)
}

func (c *Client) ListGitBranches(ctx context.Context, name, repo string) ([]string, error) {
	var out list[string]
	return out.Items, c.Do(ctx, "GET", "/api/v1/integrations/git/"+url.PathEscape(name)+"/branches?repo="+url.QueryEscape(repo), nil, &out)
}

// GitHubAppManifest starts the GitHub App flow (finished in a browser).
type GitHubAppManifest struct {
	PostURL  string `json:"postUrl"`
	Manifest string `json:"manifest"`
	State    string `json:"state"`
}

func (c *Client) CreateGitHubAppManifest(ctx context.Context, name, org, githubURL string) (GitHubAppManifest, error) {
	var out GitHubAppManifest
	return out, c.Do(ctx, "POST", "/api/v1/integrations/github/manifest", map[string]string{"name": name, "org": org, "githubUrl": githubURL}, &out)
}

// GitServer is the built-in Git server (Forgejo).
type GitServer struct {
	Enabled    bool   `json:"enabled"`
	State      string `json:"state"`
	URL        string `json:"url"`
	Image      string `json:"image"`
	AdminUser  string `json:"adminUser"`
	Connection string `json:"connection"`
	Problem    string `json:"problem,omitempty"`
}

func (c *Client) GetGitServer(ctx context.Context) (GitServer, error) {
	var out GitServer
	return out, c.Do(ctx, "GET", "/api/v1/gitserver", nil, &out)
}

func (c *Client) SetGitServer(ctx context.Context, enabled bool) (GitServer, error) {
	var out GitServer
	return out, c.Do(ctx, "PUT", "/api/v1/gitserver", map[string]bool{"enabled": enabled}, &out)
}

func (c *Client) GitServerCredentials(ctx context.Context) (map[string]string, error) {
	var out map[string]string
	return out, c.Do(ctx, "GET", "/api/v1/gitserver/credentials", nil, &out)
}
