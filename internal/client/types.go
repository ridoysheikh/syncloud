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
}

// LogQuery selects log lines; empty fields match everything.
type LogQuery struct {
	Project, Environment, Service, Task, Node, Text string
	Since                                           string // e.g. "1h"
	Limit                                           int
}

func (q LogQuery) values() url.Values {
	v := url.Values{}
	for k, val := range map[string]string{"project": q.Project, "environment": q.Environment, "service": q.Service, "task": q.Task, "node": q.Node, "q": q.Text, "since": q.Since} {
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
