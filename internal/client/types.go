package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
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
	// Schedulable allows new tasks on the node.
	Schedulable bool `json:"schedulable"`
	Draining    bool `json:"draining"`
}

func (c *Client) DrainNode(ctx context.Context, id string) (Node, error) {
	var out Node
	return out, c.Do(ctx, "POST", "/api/v1/nodes/"+url.PathEscape(id)+"/drain", nil, &out)
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

type BackupConfig struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Retain          int    `json:"retain"`
}

type BackupSettings struct {
	Configured bool          `json:"configured"`
	Config     *BackupConfig `json:"config"`
	Status     struct {
		LastRunAt     *time.Time `json:"lastRunAt"`
		LastSuccessAt *time.Time `json:"lastSuccessAt"`
		LastError     string     `json:"lastError"`
		LastObject    string     `json:"lastObject"`
		LastSize      int64      `json:"lastSize"`
	} `json:"status"`
}

type BackupObject struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
}

func (c *Client) GetBackupConfig(ctx context.Context) (BackupSettings, error) {
	var out BackupSettings
	return out, c.Do(ctx, "GET", "/api/v1/backups/config", nil, &out)
}

func (c *Client) SetBackupConfig(ctx context.Context, cfg BackupConfig) (BackupSettings, error) {
	var out BackupSettings
	return out, c.Do(ctx, "PUT", "/api/v1/backups/config", cfg, &out)
}

func (c *Client) DisableBackups(ctx context.Context) error {
	return c.Do(ctx, "DELETE", "/api/v1/backups/config", nil, nil)
}

func (c *Client) ListBackups(ctx context.Context) ([]BackupObject, error) {
	var out list[BackupObject]
	return out.Items, c.Do(ctx, "GET", "/api/v1/backups", nil, &out)
}

func (c *Client) RunBackup(ctx context.Context) (BackupObject, error) {
	var out BackupObject
	return out, c.Do(ctx, "POST", "/api/v1/backups", nil, &out)
}

type MeshPeer struct {
	NodeID        string     `json:"nodeId"`
	Name          string     `json:"name"`
	Endpoint      string     `json:"endpoint"`
	LastHandshake *time.Time `json:"lastHandshake"`
	RxBytes       uint64     `json:"rxBytes"`
	TxBytes       uint64     `json:"txBytes"`
	RTTMillis     float64    `json:"rttMs"`
}

type MeshNode struct {
	NodeID     string     `json:"nodeId"`
	Name       string     `json:"name"`
	Address    string     `json:"address"`
	Subnet     string     `json:"subnet"`
	Endpoint   string     `json:"endpoint"`
	PublicKey  string     `json:"publicKey"`
	Mode       string     `json:"mode"`
	Generation uint64     `json:"generation"`
	AppliedGen uint64     `json:"appliedGeneration"`
	Error      string     `json:"error"`
	Peers      []MeshPeer `json:"peers"`
}

type Mesh struct {
	MeshCIDR      string     `json:"meshCidr"`
	ContainerCIDR string     `json:"containerCidr"`
	ServiceCIDR   string     `json:"serviceCidr"`
	Items         []MeshNode `json:"items"`
}

func (c *Client) GetMesh(ctx context.Context) (Mesh, error) {
	var out Mesh
	return out, c.Do(ctx, "GET", "/api/v1/network/mesh", nil, &out)
}

type FirewallRule struct {
	ID          string   `json:"id,omitempty"`
	Protocol    string   `json:"protocol"`
	Ports       string   `json:"ports"`
	Sources     []string `json:"sources"`
	Description string   `json:"description"`
}

type FirewallPolicy struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Targets     []string       `json:"targets"`
	Rules       []FirewallRule `json:"rules"`
	UpdatedAt   time.Time      `json:"updatedAt,omitzero"`
}

type EffectiveFirewall struct {
	Enabled        bool           `json:"enabled"`
	Rules          []FirewallRule `json:"rules"`
	ClusterSources []string       `json:"clusterSources"`
	Ruleset        string         `json:"ruleset"`
}

func (c *Client) ListFirewallPolicies(ctx context.Context) ([]FirewallPolicy, error) {
	var out list[FirewallPolicy]
	return out.Items, c.Do(ctx, "GET", "/api/v1/firewall/policies", nil, &out)
}

func (c *Client) CreateFirewallPolicy(ctx context.Context, p FirewallPolicy) (FirewallPolicy, error) {
	var out FirewallPolicy
	return out, c.Do(ctx, "POST", "/api/v1/firewall/policies", p, &out)
}

func (c *Client) UpdateFirewallPolicy(ctx context.Context, id string, p FirewallPolicy) (FirewallPolicy, error) {
	var out FirewallPolicy
	return out, c.Do(ctx, "PUT", "/api/v1/firewall/policies/"+url.PathEscape(id), p, &out)
}

func (c *Client) DeleteFirewallPolicy(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/firewall/policies/"+url.PathEscape(id), nil, nil)
}

func (c *Client) EffectiveFirewall(ctx context.Context, nodeID string) (EffectiveFirewall, error) {
	var out EffectiveFirewall
	return out, c.Do(ctx, "GET", "/api/v1/firewall/nodes/"+url.PathEscape(nodeID)+"/effective", nil, &out)
}

type Project struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Environments []string  `json:"environments"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Environment struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type ServicePort struct {
	Name      string `json:"name,omitempty"`
	Container int    `json:"container"`
	Protocol  string `json:"protocol,omitempty"`
}

type ServiceSpec struct {
	Image     string            `json:"image"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Ports     []ServicePort     `json:"ports,omitempty"`
	Resources struct {
		CPU         float64 `json:"cpu,omitempty"`
		Memory      int     `json:"memory,omitempty"`
		CPULimit    float64 `json:"cpuLimit,omitempty"`
		MemoryLimit int     `json:"memoryLimit,omitempty"`
	} `json:"resources"`
	Placement struct {
		Strategy string `json:"strategy,omitempty"`
	} `json:"placement"`
	Health       *HealthCheck   `json:"health,omitempty"`
	Deployment   map[string]any `json:"deployment,omitempty"`
	DesiredCount *int           `json:"desiredCount,omitempty"`
}

type HealthCheck struct {
	Type        string   `json:"type"`
	Path        string   `json:"path,omitempty"`
	Port        string   `json:"port,omitempty"`
	Command     []string `json:"command,omitempty"`
	Interval    int      `json:"interval,omitempty"`
	Timeout     int      `json:"timeout,omitempty"`
	Retries     int      `json:"retries,omitempty"`
	StartPeriod int      `json:"startPeriod,omitempty"`
}

type Deployment struct {
	ID           string     `json:"id"`
	FromRevision int        `json:"fromRevision"`
	ToRevision   int        `json:"toRevision"`
	Status       string     `json:"status"`
	FailedTasks  int        `json:"failedTasks"`
	Message      string     `json:"message"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
}

func (c *Client) ServiceDeployments(ctx context.Context, project, env, name string) ([]Deployment, error) {
	var out list[Deployment]
	return out.Items, c.Do(ctx, "GET", svcPath(project, env, name)+"/deployments", nil, &out)
}

type Service struct {
	ID           string      `json:"id"`
	Project      string      `json:"project"`
	Environment  string      `json:"environment"`
	Name         string      `json:"name"`
	Revision     int         `json:"revision"`
	DesiredCount int         `json:"desiredCount"`
	Running      int         `json:"running"`
	Pending      int         `json:"pending"`
	Status       string      `json:"status"`
	Deleting     bool        `json:"deleting"`
	Spec         ServiceSpec `json:"spec"`
	Endpoints    []string    `json:"endpoints"`
	VIP          string      `json:"vip"`
	DNSName      string      `json:"dnsName"`
	Deployment   *Deployment `json:"deployment"`
	CreatedAt    time.Time   `json:"createdAt"`
	UpdatedAt    time.Time   `json:"updatedAt"`
}

type Task struct {
	ID          string     `json:"id"`
	ServiceID   string     `json:"serviceId"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Service     string     `json:"service"`
	Revision    int        `json:"revision"`
	NodeID      string     `json:"nodeId"`
	Node        string     `json:"node"`
	Desired     string     `json:"desired"`
	State       string     `json:"state"`
	IP          string     `json:"ip"`
	ContainerID string     `json:"containerId"`
	Health      string     `json:"health"`
	ExitCode    int        `json:"exitCode"`
	Error       string     `json:"error"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

type Revision struct {
	Revision  int         `json:"revision"`
	Current   bool        `json:"current"`
	Spec      ServiceSpec `json:"spec"`
	CreatedAt time.Time   `json:"createdAt"`
	CreatedBy string      `json:"createdBy"`
}

func svcPath(project, env, name string) string {
	return "/api/v1/projects/" + url.PathEscape(project) + "/environments/" + url.PathEscape(env) + "/services/" + url.PathEscape(name)
}

func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var out list[Project]
	return out.Items, c.Do(ctx, "GET", "/api/v1/projects", nil, &out)
}

func (c *Client) CreateProject(ctx context.Context, name, description, environment string) (Project, error) {
	var out Project
	return out, c.Do(ctx, "POST", "/api/v1/projects", map[string]string{"name": name, "description": description, "environment": environment}, &out)
}

func (c *Client) DeleteProject(ctx context.Context, name string) error {
	return c.Do(ctx, "DELETE", "/api/v1/projects/"+url.PathEscape(name), nil, nil)
}

func (c *Client) ListEnvironments(ctx context.Context, project string) ([]Environment, error) {
	var out list[Environment]
	return out.Items, c.Do(ctx, "GET", "/api/v1/projects/"+url.PathEscape(project)+"/environments", nil, &out)
}

func (c *Client) CreateEnvironment(ctx context.Context, project, name string) (Environment, error) {
	var out Environment
	return out, c.Do(ctx, "POST", "/api/v1/projects/"+url.PathEscape(project)+"/environments", map[string]string{"name": name}, &out)
}

func (c *Client) DeleteEnvironment(ctx context.Context, project, name string) error {
	return c.Do(ctx, "DELETE", "/api/v1/projects/"+url.PathEscape(project)+"/environments/"+url.PathEscape(name), nil, nil)
}

// SharedVariables returns an environment's shared variables.
func (c *Client) SharedVariables(ctx context.Context, project, env string) (map[string]string, error) {
	var out struct {
		Variables map[string]string `json:"variables"`
	}
	return out.Variables, c.Do(ctx, "GET", "/api/v1/projects/"+url.PathEscape(project)+"/environments/"+url.PathEscape(env)+"/variables", nil, &out)
}

// SetSharedVariables replaces them and returns the redeployed services.
func (c *Client) SetSharedVariables(ctx context.Context, project, env string, vars map[string]string) ([]string, error) {
	var out struct {
		Redeployed []string `json:"redeployed"`
	}
	return out.Redeployed, c.Do(ctx, "PUT", "/api/v1/projects/"+url.PathEscape(project)+"/environments/"+url.PathEscape(env)+"/variables",
		map[string]any{"variables": vars}, &out)
}

func (c *Client) ListServices(ctx context.Context, project, env string) ([]Service, error) {
	var out list[Service]
	if project == "" {
		return out.Items, c.Do(ctx, "GET", "/api/v1/services", nil, &out)
	}
	return out.Items, c.Do(ctx, "GET", "/api/v1/projects/"+url.PathEscape(project)+"/environments/"+url.PathEscape(env)+"/services", nil, &out)
}

func (c *Client) GetService(ctx context.Context, project, env, name string) (Service, error) {
	var out Service
	return out, c.Do(ctx, "GET", svcPath(project, env, name), nil, &out)
}

func (c *Client) ApplyService(ctx context.Context, project, env, name string, spec ServiceSpec) (Service, error) {
	var out Service
	return out, c.Do(ctx, "PUT", svcPath(project, env, name), spec, &out)
}

func (c *Client) DeleteService(ctx context.Context, project, env, name string) error {
	return c.Do(ctx, "DELETE", svcPath(project, env, name), nil, nil)
}

func (c *Client) ScaleService(ctx context.Context, project, env, name string, desired int) (Service, error) {
	var out Service
	return out, c.Do(ctx, "POST", svcPath(project, env, name)+"/scale", map[string]int{"desiredCount": desired}, &out)
}

func (c *Client) RollbackService(ctx context.Context, project, env, name string, revision int) (Service, error) {
	var out Service
	return out, c.Do(ctx, "POST", svcPath(project, env, name)+"/rollback", map[string]int{"revision": revision}, &out)
}

func (c *Client) ServiceTasks(ctx context.Context, project, env, name string) ([]Task, error) {
	var out list[Task]
	return out.Items, c.Do(ctx, "GET", svcPath(project, env, name)+"/tasks", nil, &out)
}

func (c *Client) ServiceRevisions(ctx context.Context, project, env, name string) ([]Revision, error) {
	var out list[Revision]
	return out.Items, c.Do(ctx, "GET", svcPath(project, env, name)+"/revisions", nil, &out)
}

func (c *Client) ListTasks(ctx context.Context) ([]Task, error) {
	var out list[Task]
	return out.Items, c.Do(ctx, "GET", "/api/v1/tasks", nil, &out)
}

func (c *Client) RestartTask(ctx context.Context, id string) error {
	return c.Do(ctx, "POST", "/api/v1/tasks/"+url.PathEscape(id)+"/restart", nil, nil)
}

func (c *Client) SetNodeSchedulable(ctx context.Context, id string, on bool) (Node, error) {
	var out Node
	return out, c.Do(ctx, "PUT", "/api/v1/nodes/"+url.PathEscape(id)+"/schedulable", map[string]bool{"schedulable": on}, &out)
}

type LogLine struct {
	Time        time.Time `json:"time"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Service     string    `json:"service"`
	TaskID      string    `json:"taskId"`
	Revision    string    `json:"revision"`
	Node        string    `json:"node"`
	Stream      string    `json:"stream"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	// Fields of request lines (stream "access").
	Fields map[string]string `json:"fields,omitempty"`
}

// LogQuery selects log lines; empty fields match everything.
type LogQuery struct {
	Project, Environment, Service, Task, Node, Text string
	Since                                           string // e.g. "1h"
	Limit                                           int
	// Stream "access" selects request lines, which take Status ("5xx") and Client.
	Stream, Status, Client string
}

func (q LogQuery) values() url.Values {
	v := url.Values{}
	for k, val := range map[string]string{"project": q.Project, "environment": q.Environment, "service": q.Service, "task": q.Task, "node": q.Node, "q": q.Text, "since": q.Since,
		"stream": q.Stream, "status": q.Status, "client": q.Client} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if q.Limit > 0 {
		v.Set("limit", fmt.Sprint(q.Limit))
	}
	return v
}

func (c *Client) QueryLogs(ctx context.Context, q LogQuery) ([]LogLine, error) {
	var out list[LogLine]
	return out.Items, c.Do(ctx, "GET", "/api/v1/logs?"+q.values().Encode(), nil, &out)
}

// TailLogs calls fn for every new line until ctx ends or the stream breaks.
func (c *Client) TailLogs(ctx context.Context, q LogQuery, fn func(LogLine)) error {
	q.Since, q.Limit = "", 0
	body, err := c.Stream(ctx, "/api/v1/logs/tail?"+q.values().Encode())
	if err != nil {
		return err
	}
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var l LogLine
		if json.Unmarshal([]byte(data), &l) == nil {
			fn(l)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return sc.Err()
}

type DomainCheck struct {
	Host      string   `json:"host"`
	Expected  string   `json:"expected"`
	Addresses []string `json:"addresses"`
	Ready     bool     `json:"ready"`
	Record    string   `json:"record"`
}

type ServiceDomain struct {
	ID        string      `json:"id"`
	Host      string      `json:"host"`
	Port      string      `json:"port"`
	CreatedAt time.Time   `json:"createdAt"`
	DNS       DomainCheck `json:"dns"`
}

func (c *Client) ListServiceDomains(ctx context.Context, project, env, name string) ([]ServiceDomain, error) {
	var out list[ServiceDomain]
	return out.Items, c.Do(ctx, "GET", svcPath(project, env, name)+"/domains", nil, &out)
}

func (c *Client) AddServiceDomain(ctx context.Context, project, env, name, host, port string) (DomainCheck, error) {
	var out struct {
		DNS DomainCheck `json:"dns"`
	}
	return out.DNS, c.Do(ctx, "POST", svcPath(project, env, name)+"/domains", map[string]string{"host": host, "port": port}, &out)
}

func (c *Client) RemoveServiceDomain(ctx context.Context, project, env, name, host string) error {
	return c.Do(ctx, "DELETE", svcPath(project, env, name)+"/domains/"+url.PathEscape(host), nil, nil)
}

func (c *Client) CheckDomain(ctx context.Context, host string) (DomainCheck, error) {
	var out DomainCheck
	return out, c.Do(ctx, "GET", "/api/v1/domains/check?host="+url.QueryEscape(host), nil, &out)
}

type JobRun struct {
	ID          string     `json:"id"`
	Job         string     `json:"job"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Service     string     `json:"service"`
	Revision    int        `json:"revision"`
	Trigger     string     `json:"trigger"`
	Attempt     int        `json:"attempt"`
	Status      string     `json:"status"`
	Node        string     `json:"node"`
	ExitCode    *int       `json:"exitCode"`
	Message     string     `json:"message"`
	Command     []string   `json:"command"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

type Job struct {
	ID          string         `json:"id"`
	Project     string         `json:"project"`
	Environment string         `json:"environment"`
	Name        string         `json:"name"`
	Spec        map[string]any `json:"spec"`
	NextRunAt   *time.Time     `json:"nextRunAt"`
	LastRun     *JobRun        `json:"lastRun"`
}

func jobPath(project, env, name string) string {
	return "/api/v1/projects/" + url.PathEscape(project) + "/environments/" + url.PathEscape(env) + "/jobs/" + url.PathEscape(name)
}

func (c *Client) ListJobs(ctx context.Context, project, env string) ([]Job, error) {
	var out list[Job]
	if project == "" {
		return out.Items, c.Do(ctx, "GET", "/api/v1/jobs", nil, &out)
	}
	return out.Items, c.Do(ctx, "GET", "/api/v1/projects/"+url.PathEscape(project)+"/environments/"+url.PathEscape(env)+"/jobs", nil, &out)
}

func (c *Client) GetJob(ctx context.Context, project, env, name string) (Job, error) {
	var out Job
	return out, c.Do(ctx, "GET", jobPath(project, env, name), nil, &out)
}

func (c *Client) ApplyJob(ctx context.Context, project, env, name string, spec map[string]any) (Job, error) {
	var out Job
	return out, c.Do(ctx, "PUT", jobPath(project, env, name), spec, &out)
}

func (c *Client) DeleteJob(ctx context.Context, project, env, name string) error {
	return c.Do(ctx, "DELETE", jobPath(project, env, name), nil, nil)
}

func (c *Client) RunJob(ctx context.Context, project, env, name string, command []string) (JobRun, error) {
	var out JobRun
	return out, c.Do(ctx, "POST", jobPath(project, env, name)+"/runs", map[string]any{"command": command}, &out)
}

func (c *Client) JobRuns(ctx context.Context, project, env, name string) ([]JobRun, error) {
	var out list[JobRun]
	return out.Items, c.Do(ctx, "GET", jobPath(project, env, name)+"/runs", nil, &out)
}

func (c *Client) RunService(ctx context.Context, project, env, service string, command []string) (JobRun, error) {
	var out JobRun
	return out, c.Do(ctx, "POST", svcPath(project, env, service)+"/run", map[string]any{"command": command}, &out)
}

func (c *Client) GetRun(ctx context.Context, id string) (JobRun, error) {
	var out JobRun
	return out, c.Do(ctx, "GET", "/api/v1/runs/"+url.PathEscape(id), nil, &out)
}

func (c *Client) CancelRun(ctx context.Context, id string) error {
	return c.Do(ctx, "POST", "/api/v1/runs/"+url.PathEscape(id)+"/cancel", nil, nil)
}

type Incident struct {
	ID          string     `json:"id"`
	ServiceID   string     `json:"serviceId"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Service     string     `json:"service"`
	State       string     `json:"state"`
	Cause       string     `json:"cause"`
	OpenedAt    time.Time  `json:"openedAt"`
	ClosedAt    *time.Time `json:"closedAt"`
}

type ServiceHealth struct {
	ServiceID   string   `json:"serviceId"`
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Service     string   `json:"service"`
	State       string   `json:"state"`
	Reason      string   `json:"reason"`
	Serving     int      `json:"serving"`
	Desired     int      `json:"desired"`
	Uptime24h   *float64 `json:"uptime24h"`
	Uptime7d    *float64 `json:"uptime7d"`
	LastCheck   *struct {
		OK        bool    `json:"ok"`
		Status    int     `json:"status"`
		LatencyMs float64 `json:"latencyMs"`
		Error     string  `json:"error"`
	} `json:"lastCheck"`
	Incident *Incident `json:"incident"`
}

func (c *Client) ServiceHealth(ctx context.Context) ([]ServiceHealth, error) {
	var out list[ServiceHealth]
	return out.Items, c.Do(ctx, "GET", "/api/v1/health/services", nil, &out)
}

func (c *Client) Incidents(ctx context.Context, openOnly bool) ([]Incident, error) {
	var out list[Incident]
	q := ""
	if openOnly {
		q = "?open=1"
	}
	return out.Items, c.Do(ctx, "GET", "/api/v1/health/incidents"+q, nil, &out)
}

type Repository struct {
	Name         string     `json:"name"`
	Tags         int        `json:"tags"`
	Lifecycle    bool       `json:"lifecycle"`
	Pulls        int        `json:"pulls"`
	LastPushedAt *time.Time `json:"lastPushedAt"`
	LastPulledAt *time.Time `json:"lastPulledAt"`
}

type Image struct {
	Tag          string     `json:"tag"`
	Digest       string     `json:"digest"`
	SizeBytes    int64      `json:"sizeBytes"`
	Platforms    []string   `json:"platforms"`
	Created      *time.Time `json:"created"`
	Pulls        int        `json:"pulls"`
	LastPulledAt *time.Time `json:"lastPulledAt"`
	InUseBy      []struct {
		Project     string `json:"project"`
		Environment string `json:"environment"`
		Service     string `json:"service"`
	} `json:"inUseBy"`
}

type RegistryEvent struct {
	At         time.Time `json:"at"`
	Action     string    `json:"action"`
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	Digest     string    `json:"digest"`
	Actor      string    `json:"actor"`
	Addr       string    `json:"addr"`
}

func (c *Client) ListRegistryEvents(ctx context.Context, repo string, limit int) ([]RegistryEvent, error) {
	var out list[RegistryEvent]
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if repo != "" {
		q.Set("repository", repo)
	}
	return out.Items, c.Do(ctx, "GET", "/api/v1/registry/events?"+q.Encode(), nil, &out)
}

func (c *Client) RegistryInfo(ctx context.Context) (map[string]string, error) {
	var out map[string]string
	return out, c.Do(ctx, "GET", "/api/v1/registry/info", nil, &out)
}

func (c *Client) ListRepositories(ctx context.Context) ([]Repository, error) {
	var out list[Repository]
	return out.Items, c.Do(ctx, "GET", "/api/v1/registry/repositories", nil, &out)
}

func (c *Client) ListImages(ctx context.Context, repo string) ([]Image, error) {
	var out list[Image]
	return out.Items, c.Do(ctx, "GET", "/api/v1/registry/images?repository="+url.QueryEscape(repo), nil, &out)
}

func (c *Client) DeleteImage(ctx context.Context, repo, tag string) error {
	return c.Do(ctx, "DELETE", "/api/v1/registry/images?"+url.Values{"repository": {repo}, "tag": {tag}}.Encode(), nil, nil)
}

type GitSource struct {
	URL           string            `json:"url"`
	Branch        string            `json:"branch"`
	Tags          string            `json:"tags,omitempty"`
	Paths         []string          `json:"paths,omitempty"`
	Builder       string            `json:"builder,omitempty"`
	Dockerfile    string            `json:"dockerfile"`
	Context       string            `json:"context"`
	Token         string            `json:"token,omitempty"`
	HasToken      bool              `json:"hasToken"`
	AutoDeploy    *bool             `json:"autoDeploy,omitempty"`
	PollSeconds   int               `json:"pollSeconds,omitempty"`
	WebhookPath   string            `json:"webhookPath,omitempty"`
	WebhookSecret string            `json:"webhookSecret,omitempty"`
	LastSHA       string            `json:"lastSha,omitempty"`
	LastCheckedAt *time.Time        `json:"lastCheckedAt,omitempty"`
	LastError     string            `json:"lastError,omitempty"`
	Refs          map[string]string `json:"refs,omitempty"`
}

type Build struct {
	ID          string     `json:"id"`
	ServiceID   string     `json:"serviceId"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Service     string     `json:"service"`
	SHA         string     `json:"sha"`
	Ref         string     `json:"ref"`
	Trigger     string     `json:"trigger"`
	Status      string     `json:"status"`
	Image       string     `json:"image"`
	RunID       string     `json:"runId"`
	Message     string     `json:"message"`
	Deployed    bool       `json:"deployed"`
	BaseSHA     string     `json:"baseSha"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

// GitSourceSummary is a Git source with the service it builds.
type GitSourceSummary struct {
	GitSource
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
}

func (c *Client) ListGitSources(ctx context.Context) ([]GitSourceSummary, error) {
	var out list[GitSourceSummary]
	return out.Items, c.Do(ctx, "GET", "/api/v1/git/sources", nil, &out)
}

func (c *Client) GetGitSource(ctx context.Context, project, env, service string) (GitSource, error) {
	var out GitSource
	return out, c.Do(ctx, "GET", svcPath(project, env, service)+"/git", nil, &out)
}

func (c *Client) SetGitSource(ctx context.Context, project, env, service string, src GitSource) (GitSource, error) {
	var out GitSource
	return out, c.Do(ctx, "PUT", svcPath(project, env, service)+"/git", src, &out)
}

func (c *Client) DeleteGitSource(ctx context.Context, project, env, service string) error {
	return c.Do(ctx, "DELETE", svcPath(project, env, service)+"/git", nil, nil)
}

// ListBuilds lists a service's builds, or recent builds everywhere when service is "".
func (c *Client) ListBuilds(ctx context.Context, project, env, service string) ([]Build, error) {
	var out list[Build]
	if service == "" {
		return out.Items, c.Do(ctx, "GET", "/api/v1/builds", nil, &out)
	}
	return out.Items, c.Do(ctx, "GET", svcPath(project, env, service)+"/builds", nil, &out)
}

// StartBuild builds the branch head, or sha when given.
// StartBuild builds the head of ref (a branch or tag; "" = the source's
// branch) or sha.
func (c *Client) StartBuild(ctx context.Context, project, env, service, ref, sha string) (Build, error) {
	var out Build
	return out, c.Do(ctx, "POST", svcPath(project, env, service)+"/builds", map[string]string{"ref": ref, "sha": sha}, &out)
}

func (c *Client) DeployBuild(ctx context.Context, id string) (Build, error) {
	var out Build
	return out, c.Do(ctx, "POST", "/api/v1/builds/"+url.PathEscape(id)+"/deploy", nil, &out)
}

type MetricSeries struct {
	Key    string       `json:"key"`
	Node   string       `json:"node"`
	Points [][2]float64 `json:"points"`
}

type Metrics struct {
	Start  time.Time                 `json:"start"`
	End    time.Time                 `json:"end"`
	Step   int                       `json:"stepSeconds"`
	Charts map[string][]MetricSeries `json:"charts"`
}

// Metrics charts a service (a series per task) or, with service "", a whole
// environment (a series per service). rng is 15m, 1h, 6h, 24h or 7d.
func (c *Client) Metrics(ctx context.Context, project, env, service, rng string) (Metrics, error) {
	var out Metrics
	path := "/api/v1/projects/" + url.PathEscape(project) + "/environments/" + url.PathEscape(env)
	if service != "" {
		path = svcPath(project, env, service)
	}
	return out, c.Do(ctx, "GET", path+"/metrics?range="+url.QueryEscape(rng), nil, &out)
}

// RouteTraffic is one routed service's traffic over the last minutes.
type RouteTraffic struct {
	ServiceID   string  `json:"serviceId"`
	Project     string  `json:"project"`
	Environment string  `json:"environment"`
	Service     string  `json:"service"`
	RPS         float64 `json:"rps"`
	Errors4xx   float64 `json:"errors4xx"`
	Errors5xx   float64 `json:"errors5xx"`
	P50Ms       float64 `json:"p50Ms"`
	P95Ms       float64 `json:"p95Ms"`
	BytesIn     float64 `json:"bytesIn"`
	BytesOut    float64 `json:"bytesOut"`
}

type Traffic struct {
	Metrics
	Routes        []RouteTraffic `json:"routes"`
	WindowSeconds int            `json:"windowSeconds"`
}

// Traffic returns request charts and per-route numbers of everything
// (project ""), an environment (service "") or one service.
func (c *Client) Traffic(ctx context.Context, project, env, service, rng string) (Traffic, error) {
	var out Traffic
	path := "/api/v1/traffic"
	switch {
	case service != "":
		path = svcPath(project, env, service) + "/traffic"
	case project != "":
		path = "/api/v1/projects/" + url.PathEscape(project) + "/environments/" + url.PathEscape(env) + "/traffic"
	}
	return out, c.Do(ctx, "GET", path+"?range="+url.QueryEscape(rng), nil, &out)
}

type TrafficMapTask struct {
	ID       string  `json:"id"`
	Node     string  `json:"node"`
	IP       string  `json:"ip"`
	State    string  `json:"state"`
	Health   string  `json:"health"`
	Revision int     `json:"revision"`
	RPS      float64 `json:"rps"`
}

type TrafficMapRoute struct {
	RouteTraffic
	Hosts []string         `json:"hosts"`
	Tasks []TrafficMapTask `json:"tasks"`
}

func (c *Client) TrafficMap(ctx context.Context) ([]TrafficMapRoute, error) {
	var out struct {
		Routes []TrafficMapRoute `json:"routes"`
	}
	return out.Routes, c.Do(ctx, "GET", "/api/v1/traffic/map", nil, &out)
}

// ScalingPolicy is a target tracking policy (§5.5).
type ScalingPolicy struct {
	Enabled          bool      `json:"enabled"`
	Min              int       `json:"min"`
	Max              int       `json:"max"`
	Metric           string    `json:"metric"`
	Target           float64   `json:"target"`
	ScaleOutCooldown int       `json:"scaleOutCooldown,omitempty"`
	ScaleInCooldown  int       `json:"scaleInCooldown,omitempty"`
	ScaleInChecks    int       `json:"scaleInChecks,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt,omitempty"`
	UpdatedBy        string    `json:"updatedBy,omitempty"`
}

type Autoscaling struct {
	Policy  *ScalingPolicy `json:"policy"`
	Current struct {
		Value       *float64   `json:"value"`
		EvaluatedAt *time.Time `json:"evaluatedAt"`
	} `json:"current"`
	Units map[string]string `json:"units"`
}

type ScalingEvent struct {
	At     time.Time `json:"at"`
	From   int       `json:"from"`
	To     int       `json:"to"`
	Metric string    `json:"metric"`
	Value  *float64  `json:"value"`
	Target float64   `json:"target"`
	Reason string    `json:"reason"`
}

func (c *Client) GetAutoscaling(ctx context.Context, project, env, service string) (Autoscaling, error) {
	var out Autoscaling
	return out, c.Do(ctx, "GET", svcPath(project, env, service)+"/autoscaling", nil, &out)
}

func (c *Client) PutAutoscaling(ctx context.Context, project, env, service string, p ScalingPolicy) (Autoscaling, error) {
	var out Autoscaling
	return out, c.Do(ctx, "PUT", svcPath(project, env, service)+"/autoscaling", p, &out)
}

func (c *Client) DeleteAutoscaling(ctx context.Context, project, env, service string) error {
	return c.Do(ctx, "DELETE", svcPath(project, env, service)+"/autoscaling", nil, nil)
}

func (c *Client) ScalingEvents(ctx context.Context, project, env, service string, limit int) ([]ScalingEvent, error) {
	var out list[ScalingEvent]
	return out.Items, c.Do(ctx, "GET", svcPath(project, env, service)+"/scaling-events?limit="+fmt.Sprint(limit), nil, &out)
}

// AlertChannel is a notification channel (secrets are never returned).
type AlertChannel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// AlertChannelConfig holds the settings of one channel type.
type AlertChannelConfig struct {
	URL      string   `json:"url,omitempty"`
	BotToken string   `json:"botToken,omitempty"`
	ChatID   string   `json:"chatId,omitempty"`
	SMTPHost string   `json:"smtpHost,omitempty"`
	SMTPPort int      `json:"smtpPort,omitempty"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from,omitempty"`
	To       []string `json:"to,omitempty"`
}

func (c *Client) ListAlertChannels(ctx context.Context) ([]AlertChannel, error) {
	var out list[AlertChannel]
	return out.Items, c.Do(ctx, "GET", "/api/v1/alerts/channels", nil, &out)
}

// PutAlertChannel creates (id "") or replaces a channel.
func (c *Client) PutAlertChannel(ctx context.Context, id, name, typ string, cfg AlertChannelConfig) (AlertChannel, error) {
	var out AlertChannel
	body := map[string]any{"name": name, "type": typ, "config": cfg}
	if id == "" {
		return out, c.Do(ctx, "POST", "/api/v1/alerts/channels", body, &out)
	}
	return out, c.Do(ctx, "PUT", "/api/v1/alerts/channels/"+url.PathEscape(id), body, &out)
}

func (c *Client) DeleteAlertChannel(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/alerts/channels/"+url.PathEscape(id), nil, nil)
}

func (c *Client) TestAlertChannel(ctx context.Context, id string) error {
	return c.Do(ctx, "POST", "/api/v1/alerts/channels/"+url.PathEscape(id)+"/test", nil, nil)
}

func (c *Client) ListAlertRules(ctx context.Context) ([]map[string]any, error) {
	var out list[map[string]any]
	return out.Items, c.Do(ctx, "GET", "/api/v1/alerts/rules", nil, &out)
}

// PutAlertRule creates (id "") or replaces a rule given as JSON fields.
func (c *Client) PutAlertRule(ctx context.Context, id string, rule map[string]any) (map[string]any, error) {
	var out map[string]any
	if id == "" {
		return out, c.Do(ctx, "POST", "/api/v1/alerts/rules", rule, &out)
	}
	return out, c.Do(ctx, "PUT", "/api/v1/alerts/rules/"+url.PathEscape(id), rule, &out)
}

func (c *Client) DeleteAlertRule(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/alerts/rules/"+url.PathEscape(id), nil, nil)
}

type AlertState struct {
	Rule     string    `json:"rule"`
	Severity string    `json:"severity"`
	Label    string    `json:"label"`
	State    string    `json:"state"`
	Since    time.Time `json:"since"`
	Message  string    `json:"message"`
}

func (c *Client) ActiveAlerts(ctx context.Context) ([]AlertState, error) {
	var out list[AlertState]
	return out.Items, c.Do(ctx, "GET", "/api/v1/alerts/active", nil, &out)
}

type AlertEvent struct {
	Rule     string    `json:"rule"`
	Severity string    `json:"severity"`
	Kind     string    `json:"kind"`
	Label    string    `json:"label"`
	Message  string    `json:"message"`
	Delivery string    `json:"delivery"`
	At       time.Time `json:"at"`
}

func (c *Client) AlertEvents(ctx context.Context, limit int) ([]AlertEvent, error) {
	var out list[AlertEvent]
	return out.Items, c.Do(ctx, "GET", "/api/v1/alerts/events?limit="+fmt.Sprint(limit), nil, &out)
}

type LifecycleRule struct {
	Priority      int    `json:"priority"`
	Description   string `json:"description,omitempty"`
	TagPrefix     string `json:"tagPrefix"`
	KeepLast      int    `json:"keepLast,omitempty"`
	OlderThanDays int    `json:"olderThanDays,omitempty"`
}

type LifecyclePolicy struct {
	Repository string          `json:"repository"`
	Rules      []LifecycleRule `json:"rules"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

type LifecycleDecision struct {
	Image
	Expire bool   `json:"expire"`
	InUse  bool   `json:"inUse"`
	Rule   int    `json:"rule"`
	Reason string `json:"reason"`
}

type GCRun struct {
	ID             string     `json:"id"`
	Trigger        string     `json:"trigger"`
	Status         string     `json:"status"`
	Expired        int        `json:"expired"`
	ReclaimedBytes int64      `json:"reclaimedBytes"`
	Message        string     `json:"message"`
	StartedAt      time.Time  `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
	Details        []struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
		Reason     string `json:"reason"`
	} `json:"details"`
}

func lifecyclePath(repo string) string {
	return "/api/v1/registry/lifecycle?repository=" + url.QueryEscape(repo)
}

func (c *Client) GetLifecyclePolicy(ctx context.Context, repo string) (LifecyclePolicy, error) {
	var out LifecyclePolicy
	return out, c.Do(ctx, "GET", lifecyclePath(repo), nil, &out)
}

func (c *Client) PutLifecyclePolicy(ctx context.Context, repo string, rules []LifecycleRule) (LifecyclePolicy, error) {
	var out LifecyclePolicy
	return out, c.Do(ctx, "PUT", lifecyclePath(repo), map[string]any{"rules": rules}, &out)
}

func (c *Client) DeleteLifecyclePolicy(ctx context.Context, repo string) error {
	return c.Do(ctx, "DELETE", lifecyclePath(repo), nil, nil)
}

// PreviewLifecyclePolicy is a dry run of rules against a repository.
func (c *Client) PreviewLifecyclePolicy(ctx context.Context, repo string, rules []LifecycleRule) ([]LifecycleDecision, error) {
	var out struct {
		Images []LifecycleDecision `json:"images"`
	}
	return out.Images, c.Do(ctx, "POST", "/api/v1/registry/lifecycle/preview", map[string]any{"repository": repo, "rules": rules}, &out)
}

func (c *Client) RegistryCleanups(ctx context.Context) ([]GCRun, bool, error) {
	var out struct {
		Items   []GCRun `json:"items"`
		Running bool    `json:"running"`
	}
	return out.Items, out.Running, c.Do(ctx, "GET", "/api/v1/registry/gc", nil, &out)
}

func (c *Client) StartRegistryCleanup(ctx context.Context) (GCRun, error) {
	var out GCRun
	return out, c.Do(ctx, "POST", "/api/v1/registry/gc", nil, &out)
}

type UpstreamCredential struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (c *Client) ListUpstreamCredentials(ctx context.Context) ([]UpstreamCredential, error) {
	var out list[UpstreamCredential]
	return out.Items, c.Do(ctx, "GET", "/api/v1/registry/upstreams", nil, &out)
}

func (c *Client) PutUpstreamCredential(ctx context.Context, host, username, password string) (UpstreamCredential, error) {
	var out UpstreamCredential
	return out, c.Do(ctx, "PUT", "/api/v1/registry/upstreams", map[string]string{"host": host, "username": username, "password": password}, &out)
}

func (c *Client) DeleteUpstreamCredential(ctx context.Context, id string) error {
	return c.Do(ctx, "DELETE", "/api/v1/registry/upstreams/"+url.PathEscape(id), nil, nil)
}
