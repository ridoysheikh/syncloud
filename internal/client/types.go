package client

import (
	"context"
	"net/url"
	"time"
)

// Types mirror the OpenAPI schemas (internal/api/openapi.json).

type SystemStatus struct {
	Version       string `json:"version"`
	SetupRequired bool   `json:"setupRequired"`
	BaseDomain    string `json:"baseDomain"`
}

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	IsRoot    bool      `json:"isRoot"`
	CreatedAt time.Time `json:"createdAt"`
}

type AccessKey struct {
	ID              string     `json:"id"`
	Description     string     `json:"description"`
	CreatedAt       time.Time  `json:"createdAt"`
	LastUsedAt      *time.Time `json:"lastUsedAt"`
	LastUsedIP      string     `json:"lastUsedIp"`
	SecretAccessKey string     `json:"secretAccessKey,omitempty"`
}

type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	LastUsedIP string     `json:"lastUsedIp"`
	Token      string     `json:"token,omitempty"`
}

type list[T any] struct {
	Items []T `json:"items"`
}

func (c *Client) SystemStatus(ctx context.Context) (SystemStatus, error) {
	var out SystemStatus
	return out, c.Do(ctx, "GET", "/api/v1/system/status", nil, &out)
}

func (c *Client) Whoami(ctx context.Context) (User, error) {
	var out User
	return out, c.Do(ctx, "GET", "/api/v1/auth/me", nil, &out)
}

func (c *Client) ListAccessKeys(ctx context.Context) ([]AccessKey, error) {
	var out list[AccessKey]
	return out.Items, c.Do(ctx, "GET", "/api/v1/iam/access-keys", nil, &out)
}

func (c *Client) CreateAccessKey(ctx context.Context, description string) (AccessKey, error) {
	var out AccessKey
	return out, c.Do(ctx, "POST", "/api/v1/iam/access-keys", map[string]string{"description": description}, &out)
}

func (c *Client) DeleteAccessKey(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/iam/access-keys/"+id, nil, nil)
}

func (c *Client) ListTokens(ctx context.Context) ([]Token, error) {
	var out list[Token]
	return out.Items, c.Do(ctx, "GET", "/api/v1/iam/tokens", nil, &out)
}

func (c *Client) CreateToken(ctx context.Context, name string, expiresInDays int) (Token, error) {
	var out Token
	return out, c.Do(ctx, "POST", "/api/v1/iam/tokens", map[string]any{"name": name, "expiresInDays": expiresInDays}, &out)
}

func (c *Client) DeleteToken(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/iam/tokens/"+id, nil, nil)
}

type NodeInfo struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Kernel        string `json:"kernel"`
	Arch          string `json:"arch"`
	CPUCores      int    `json:"cpuCores"`
	MemoryBytes   uint64 `json:"memoryBytes"`
	DiskBytes     uint64 `json:"diskBytes"`
	DockerVersion string `json:"dockerVersion"`
	AgentVersion  string `json:"agentVersion"`
}

type NodeMetrics struct {
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryUsedBytes  uint64  `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64  `json:"memoryTotalBytes"`
	DiskUsedBytes    uint64  `json:"diskUsedBytes"`
	DiskTotalBytes   uint64  `json:"diskTotalBytes"`
	Load1            float64 `json:"load1"`
	Load5            float64 `json:"load5"`
	Load15           float64 `json:"load15"`
	NetRxBytes       uint64  `json:"netRxBytes"`
	NetTxBytes       uint64  `json:"netTxBytes"`
	UptimeSeconds    int64   `json:"uptimeSeconds"`
}

type Node struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Status     string       `json:"status"`
	StatusAt   time.Time    `json:"statusAt"`
	Connected  bool         `json:"connected"`
	LastSeenAt *time.Time   `json:"lastSeenAt"`
	CreatedAt  time.Time    `json:"createdAt"`
	Info       NodeInfo     `json:"info"`
	Metrics    *NodeMetrics `json:"metrics"`
}

type JoinToken struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	SingleUse   bool      `json:"singleUse"`
	Uses        int       `json:"uses"`
	Token       string    `json:"token,omitempty"`
}

type JoinRequest struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	CSR   string `json:"csr"`
}

type JoinResponse struct {
	NodeID         string `json:"nodeId"`
	Name           string `json:"name"`
	Certificate    string `json:"certificate"`
	CACertificate  string `json:"caCertificate"`
	GatewayAddress string `json:"gatewayAddress"`
}

// JoinNode needs no credentials: the join token authorizes it.
func (c *Client) JoinNode(ctx context.Context, req JoinRequest) (JoinResponse, error) {
	var out JoinResponse
	return out, c.Do(ctx, "POST", "/api/v1/nodes/join", req, &out)
}

func (c *Client) ListNodes(ctx context.Context) ([]Node, error) {
	var out list[Node]
	return out.Items, c.Do(ctx, "GET", "/api/v1/nodes", nil, &out)
}

func (c *Client) DeleteNode(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/nodes/"+id, nil, nil)
}

func (c *Client) ListJoinTokens(ctx context.Context) ([]JoinToken, error) {
	var out list[JoinToken]
	return out.Items, c.Do(ctx, "GET", "/api/v1/nodes/join-tokens", nil, &out)
}

func (c *Client) CreateJoinToken(ctx context.Context, description string, ttlMinutes int, singleUse bool) (JoinToken, error) {
	var out JoinToken
	return out, c.Do(ctx, "POST", "/api/v1/nodes/join-tokens",
		map[string]any{"description": description, "ttlMinutes": ttlMinutes, "singleUse": singleUse}, &out)
}

func (c *Client) DeleteJoinToken(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/nodes/join-tokens/"+id, nil, nil)
}

type SystemTask struct {
	TaskID      string     `json:"taskId"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Image       string     `json:"image"`
	Node        string     `json:"node"`
	State       string     `json:"state"`
	Health      string     `json:"health"`
	Error       string     `json:"error"`
	ContainerID string     `json:"containerId"`
	StartedAt   *time.Time `json:"startedAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func (c *Client) ListSystemTasks(ctx context.Context) ([]SystemTask, error) {
	var out list[SystemTask]
	return out.Items, c.Do(ctx, "GET", "/api/v1/system/tasks", nil, &out)
}

type DomainSettings struct {
	BaseDomain    string   `json:"baseDomain"`
	DashboardURL  string   `json:"dashboardUrl"`
	RegistryHost  string   `json:"registryHost"`
	PublicIP      string   `json:"publicIp"`
	PublicIPError string   `json:"publicIpError"`
	Suggestions   []string `json:"suggestions"`
	ACME          struct {
		Enabled      bool   `json:"enabled"`
		DirectoryURL string `json:"directoryUrl"`
		Email        string `json:"email"`
	} `json:"acme"`
	Warnings []string `json:"warnings"`
}

func (c *Client) GetDomainSettings(ctx context.Context) (DomainSettings, error) {
	var out DomainSettings
	return out, c.Do(ctx, "GET", "/api/v1/settings/domain", nil, &out)
}

func (c *Client) SetBaseDomain(ctx context.Context, baseDomain string) (DomainSettings, error) {
	var out DomainSettings
	return out, c.Do(ctx, "PUT", "/api/v1/settings/domain", map[string]string{"baseDomain": baseDomain}, &out)
}

type Certificate struct {
	Host          string     `json:"host"`
	Issuer        string     `json:"issuer"`
	Status        string     `json:"status"`
	NotAfter      time.Time  `json:"notAfter"`
	LastError     string     `json:"lastError"`
	Failures      int        `json:"failures"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func (c *Client) ListCertificates(ctx context.Context) ([]Certificate, error) {
	var out list[Certificate]
	return out.Items, c.Do(ctx, "GET", "/api/v1/certificates", nil, &out)
}

func (c *Client) RenewCertificate(ctx context.Context, host string) error {
	return c.Do(ctx, "POST", "/api/v1/certificates/"+url.PathEscape(host)+"/renew", nil, nil)
}
