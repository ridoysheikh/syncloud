// Package api is the controller's public HTTP API (§5.1). The dashboard,
// synctl and CI all use these same endpoints; there are no UI-only routes.
package api

import (
	"context"
	_ "embed"
	"io/fs"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"syncloud/internal/alerts"
	"syncloud/internal/autoscale"
	"syncloud/internal/backup"
	"syncloud/internal/builds"
	"syncloud/internal/certs"
	"syncloud/internal/dbs"
	"syncloud/internal/discovery"
	"syncloud/internal/domain"
	"syncloud/internal/edge"
	"syncloud/internal/events"
	"syncloud/internal/execrelay"
	"syncloud/internal/fwstats"
	"syncloud/internal/gitconn"
	"syncloud/internal/gitserver"
	"syncloud/internal/health"
	"syncloud/internal/jobs"
	"syncloud/internal/logs"
	"syncloud/internal/mesh"
	"syncloud/internal/metrics"
	"syncloud/internal/nodepool"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	"syncloud/internal/quota"
	"syncloud/internal/registry"
	"syncloud/internal/regmaint"
	"syncloud/internal/s3"
	"syncloud/internal/secrets"
	"syncloud/internal/shell"
	"syncloud/internal/store"
	"syncloud/internal/system"
	"syncloud/internal/traefik"
	"syncloud/internal/upgrade"
	"syncloud/internal/upgrade/rollout"
	"syncloud/internal/upstream"
	"syncloud/internal/workload"
)

// OpenAPISpec is the API contract, served at /api/v1/openapi.json.
// A test keeps it in sync with Routes.
//
//go:embed openapi.json
var OpenAPISpec []byte

type Server struct {
	store                 *store.Store
	secrets               *secrets.Box
	ca                    *pki.CA
	nodes                 *nodes.Registry
	gatewayAddr           string
	system                *system.Manager
	registry              *registry.Issuer
	internal              map[string]http.Handler
	domains               *domain.Service
	detector              *domain.Detector
	certs                 *certs.Manager
	acme                  ACMEInfo
	onSetup               func()
	backups               *backup.Manager
	mesh                  *mesh.Manager
	downloadsDir          string
	workloads             *workload.Manager
	databases             *dbs.Manager
	logs                  *logs.Store
	exec                  *execrelay.Relay
	jobs                  *jobs.Manager
	health                *health.Monitor
	registryBrowser       *registry.Browser
	builds                *builds.Manager
	metrics               *metrics.Store
	regMaint              *regmaint.Manager
	upstreams             *upstream.Manager
	registryHosts         func() []string
	autoscaler            *autoscale.Manager
	alerts                *alerts.Manager
	securityGroups        bool
	onSecurityChange      func()
	fwStats               *fwstats.Stats
	quotas                *quota.Manager
	shell                 *shell.Manager
	pools                 *nodepool.Manager
	edges                 *edge.Manager
	upgrades              *upgrade.Service
	s3                    *s3.Manager
	agentRollout          *rollout.Manager
	discovery             *discovery.Manager
	traefik               *traefik.Provider
	traefikExtras         *traefik.Extras
	gitConns              *gitconn.Manager
	gitServer             *gitserver.Manager
	onTraefikSettings     func()
	controllerSchedulable bool
	bus                   *events.Bus
	log                   *slog.Logger
	web                   fs.FS // built dashboard (may be empty in development)
	now                   func() time.Time
	startedAt             time.Time

	iamCache   stmtCache
	requireMFA atomic.Bool // every human must use MFA (§14)

	loginLimiter *attemptLimiter
	setupLimiter *attemptLimiter
	joinLimiter  *attemptLimiter
}

type Options struct {
	Store   *store.Store
	Secrets *secrets.Box
	CA      *pki.CA
	Nodes   *nodes.Registry
	// GatewayAddr is the agent gateway address returned to joining nodes.
	GatewayAddr string
	// System reports platform components (system tasks); may be nil in tests.
	System *system.Manager
	// Registry issues Docker registry tokens; nil disables the token endpoint.
	Registry *registry.Issuer
	// Internal handlers are mounted as-is outside the public API (e.g. the
	// Traefik config endpoint) and carry their own authentication.
	Internal map[string]http.Handler
	// Domains, Detector and Certs back the domain settings (§5.0.2); may be nil.
	Domains  *domain.Service
	Detector *domain.Detector
	Certs    *certs.Manager
	ACME     ACMEInfo
	// OnSetup runs after the root account is created.
	OnSetup func()
	// Backups manages S3 backups (§13); may be nil.
	Backups *backup.Manager
	// Mesh reports the private network (§8); may be nil.
	Mesh *mesh.Manager
	// DownloadsDir holds agent/CLI binaries served at /downloads/ ("" disables).
	DownloadsDir string
	// Workloads runs services (§5.2); may be nil.
	Workloads *workload.Manager
	// Databases runs managed Valkey (Phase 12); may be nil.
	Databases *dbs.Manager
	// Logs serves container logs (§9.2); may be nil.
	Logs *logs.Store
	// Exec relays interactive commands to tasks; may be nil.
	Exec *execrelay.Relay
	// Jobs runs one-off, scheduled and hook jobs (§5.11); may be nil.
	Jobs *jobs.Manager
	// Health is the central health monitor (§5.6); may be nil.
	Health *health.Monitor
	// RegistryBrowser reads repositories and images (§5.10); may be nil.
	RegistryBrowser *registry.Browser
	// Builds builds Git commits into images (§5.8); may be nil.
	Builds *builds.Manager
	// Metrics reads task resource usage (§9.1); may be nil.
	Metrics *metrics.Store
	// RegistryMaint applies lifecycle policies and runs GC (§5.10); may be nil.
	RegistryMaint *regmaint.Manager
	// Upstreams stores third-party registry credentials (§5.9); may be nil.
	Upstreams *upstream.Manager
	// RegistryHosts are the names images use for the private registry.
	RegistryHosts func() []string
	// Autoscaler runs target tracking policies (§5.5).
	Autoscaler *autoscale.Manager
	// Alerts evaluates alert rules and notifies channels (§9).
	Alerts *alerts.Manager
	// SecurityGroups is on when containers are isolated (§8.3);
	// OnSecurityChange republishes the compiled policy.
	SecurityGroups   bool
	OnSecurityChange func()
	FirewallStats    *fwstats.Stats
	Quotas           *quota.Manager
	Shell            *shell.Manager
	Pools            *nodepool.Manager
	Edges            *edge.Manager
	Upgrades         *upgrade.Service
	S3               *s3.Manager
	AgentRollout     *rollout.Manager
	Discovery        *discovery.Manager
	Traefik          *traefik.Provider
	TraefikExtras    *traefik.Extras
	GitConnections   *gitconn.Manager
	GitServer        *gitserver.Manager
	// OnTraefikSettings re-renders the Traefik replicas after the static
	// settings changed.
	OnTraefikSettings func()
	// ControllerSchedulable lets ctl-0 run services from the moment it joins (D3).
	ControllerSchedulable bool
	Bus                   *events.Bus
	Log                   *slog.Logger
	Web                   fs.FS
	// Now is overridable for tests.
	Now func() time.Time
}

func New(o Options) *Server {
	if o.Now == nil {
		o.Now = time.Now
	}
	srv := &Server{
		store:                 o.Store,
		secrets:               o.Secrets,
		ca:                    o.CA,
		nodes:                 o.Nodes,
		gatewayAddr:           o.GatewayAddr,
		system:                o.System,
		registry:              o.Registry,
		internal:              o.Internal,
		domains:               o.Domains,
		detector:              o.Detector,
		certs:                 o.Certs,
		acme:                  o.ACME,
		onSetup:               o.OnSetup,
		backups:               o.Backups,
		mesh:                  o.Mesh,
		downloadsDir:          o.DownloadsDir,
		workloads:             o.Workloads,
		databases:             o.Databases,
		logs:                  o.Logs,
		exec:                  o.Exec,
		jobs:                  o.Jobs,
		health:                o.Health,
		registryBrowser:       o.RegistryBrowser,
		builds:                o.Builds,
		metrics:               o.Metrics,
		regMaint:              o.RegistryMaint,
		upstreams:             o.Upstreams,
		registryHosts:         o.RegistryHosts,
		autoscaler:            o.Autoscaler,
		alerts:                o.Alerts,
		securityGroups:        o.SecurityGroups,
		onSecurityChange:      o.OnSecurityChange,
		fwStats:               o.FirewallStats,
		quotas:                o.Quotas,
		shell:                 o.Shell,
		pools:                 o.Pools,
		edges:                 o.Edges,
		upgrades:              o.Upgrades,
		s3:                    o.S3,
		startedAt:             time.Now(),
		agentRollout:          o.AgentRollout,
		discovery:             o.Discovery,
		traefik:               o.Traefik,
		traefikExtras:         o.TraefikExtras,
		gitConns:              o.GitConnections,
		gitServer:             o.GitServer,
		onTraefikSettings:     o.OnTraefikSettings,
		controllerSchedulable: o.ControllerSchedulable,
		bus:                   o.Bus,
		log:                   o.Log,
		web:                   o.Web,
		now:                   o.Now,
		loginLimiter:          newAttemptLimiter(10, 5*time.Minute),
		setupLimiter:          newAttemptLimiter(10, 5*time.Minute),
		joinLimiter:           newAttemptLimiter(20, 5*time.Minute),
	}
	if o.Store != nil {
		v, _, _ := o.Store.GetSetting(context.Background(), SettingRequireMFA)
		srv.requireMFA.Store(v == "1")
	}
	return srv

}

// Route is one API endpoint. Path uses OpenAPI-style {param} placeholders,
// which net/http's ServeMux also understands.
type Route struct {
	Method string
	Path   string
	Public bool // no authentication required
	h      http.HandlerFunc
}

// Routes returns every API endpoint.
func (s *Server) Routes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/v1/system/status", Public: true, h: s.handleStatus},
		{Method: "GET", Path: "/api/v1/openapi.json", Public: true, h: handleOpenAPI},
		{Method: "GET", Path: "/api/v1/system/tasks", h: s.handleSystemTasks},
		{Method: "GET", Path: "/api/v1/registry/token", Public: true, h: s.handleRegistryToken},
		{Method: "GET", Path: "/api/v1/registry/info", h: s.handleRegistryInfo},
		{Method: "GET", Path: "/api/v1/registry/repositories", h: s.handleListRepositories},
		{Method: "GET", Path: "/api/v1/registry/images", h: s.handleListImages},
		{Method: "DELETE", Path: "/api/v1/registry/images", h: s.handleDeleteImage},
		{Method: "GET", Path: "/api/v1/registry/lifecycle", h: s.handleGetLifecycle},
		{Method: "PUT", Path: "/api/v1/registry/lifecycle", h: s.handlePutLifecycle},
		{Method: "DELETE", Path: "/api/v1/registry/lifecycle", h: s.handleDeleteLifecycle},
		{Method: "POST", Path: "/api/v1/registry/lifecycle/preview", h: s.handlePreviewLifecycle},
		{Method: "GET", Path: "/api/v1/registry/gc", h: s.handleGCRuns},
		{Method: "GET", Path: "/api/v1/registry/events", h: s.handleRegistryEvents},
		{Method: "GET", Path: "/api/v1/registry/upstreams", h: s.handleListUpstreams},
		{Method: "PUT", Path: "/api/v1/registry/upstreams", h: s.handlePutUpstream},
		{Method: "DELETE", Path: "/api/v1/registry/upstreams/{id}", h: s.handleDeleteUpstream},
		{Method: "POST", Path: "/api/v1/registry/gc", h: s.handleStartGC},
		{Method: "POST", Path: "/api/v1/setup", Public: true, h: s.handleSetup},
		{Method: "POST", Path: "/api/v1/auth/login", Public: true, h: s.handleLogin},
		{Method: "POST", Path: "/api/v1/auth/logout", h: s.handleLogout},
		{Method: "GET", Path: "/api/v1/auth/me", h: s.handleMe},
		{Method: "GET", Path: "/api/v1/stream", h: s.handleStream},
		{Method: "GET", Path: "/api/v1/iam/access-keys", h: s.handleListAccessKeys},
		{Method: "POST", Path: "/api/v1/iam/access-keys", h: s.handleCreateAccessKey},
		{Method: "DELETE", Path: "/api/v1/iam/access-keys/{id}", h: s.handleDeleteAccessKey},
		{Method: "GET", Path: "/api/v1/iam/tokens", h: s.handleListTokens},
		{Method: "POST", Path: "/api/v1/iam/tokens", h: s.handleCreateToken},
		{Method: "DELETE", Path: "/api/v1/iam/tokens/{id}", h: s.handleDeleteToken},
		{Method: "GET", Path: "/api/v1/iam/users", h: s.handleListUsers},
		{Method: "POST", Path: "/api/v1/iam/users", h: s.handleCreateUser},
		{Method: "PUT", Path: "/api/v1/iam/users/{id}", h: s.handleUpdateUser},
		{Method: "DELETE", Path: "/api/v1/iam/users/{id}", h: s.handleDeleteUser},
		{Method: "GET", Path: "/api/v1/iam/groups", h: s.handleListGroups},
		{Method: "POST", Path: "/api/v1/iam/groups", h: s.handleCreateGroup},
		{Method: "PUT", Path: "/api/v1/iam/groups/{id}", h: s.handleUpdateGroup},
		{Method: "DELETE", Path: "/api/v1/iam/groups/{id}", h: s.handleDeleteGroup},
		{Method: "GET", Path: "/api/v1/iam/policies", h: s.handleListPolicies},
		{Method: "POST", Path: "/api/v1/iam/policies", h: s.handleCreatePolicy},
		{Method: "PUT", Path: "/api/v1/iam/policies/{id}", h: s.handleUpdatePolicy},
		{Method: "DELETE", Path: "/api/v1/iam/policies/{id}", h: s.handleDeletePolicy},
		{Method: "GET", Path: "/api/v1/iam/attachments", h: s.handleListAttachments},
		{Method: "POST", Path: "/api/v1/iam/attachments", h: s.handleAttachPolicy},
		{Method: "DELETE", Path: "/api/v1/iam/attachments", h: s.handleDetachPolicy},
		{Method: "GET", Path: "/api/v1/iam/roles", h: s.handleListRoles},
		{Method: "POST", Path: "/api/v1/iam/roles", h: s.handleCreateRole},
		{Method: "PUT", Path: "/api/v1/iam/roles/{id}", h: s.handleUpdateRole},
		{Method: "DELETE", Path: "/api/v1/iam/roles/{id}", h: s.handleDeleteRole},
		{Method: "POST", Path: "/api/v1/iam/mfa", h: s.handleBeginMFA},
		{Method: "POST", Path: "/api/v1/iam/mfa/enable", h: s.handleEnableMFA},
		{Method: "DELETE", Path: "/api/v1/iam/mfa", h: s.handleDisableMFA},
		{Method: "POST", Path: "/api/v1/iam/password", h: s.handleChangePassword},
		{Method: "GET", Path: "/api/v1/iam/settings", h: s.handleGetIAMSettings},
		{Method: "PUT", Path: "/api/v1/iam/settings", h: s.handlePutIAMSettings},
		{Method: "POST", Path: "/api/v1/iam/simulate", h: s.handleSimulate},
		{Method: "GET", Path: "/api/v1/iam/actions", h: s.handleListActions},
		{Method: "GET", Path: "/api/v1/iam/me/permissions", h: s.handleMyPermissions},
		{Method: "POST", Path: "/api/v1/sts/assume-role", h: s.handleAssumeRole},
		{Method: "GET", Path: "/api/v1/audit", h: s.handleListAudit},
		{Method: "GET", Path: "/api/v1/quotas", h: s.handleListQuotas},
		{Method: "GET", Path: "/api/v1/cloud-providers", h: s.handleListProviders},
		{Method: "POST", Path: "/api/v1/cloud-providers", h: s.handleCreateProvider},
		{Method: "DELETE", Path: "/api/v1/cloud-providers/{id}", h: s.handleDeleteProvider},
		{Method: "GET", Path: "/api/v1/node-pools", h: s.handleListNodePools},
		{Method: "POST", Path: "/api/v1/node-pools", h: s.handleCreateNodePool},
		{Method: "PUT", Path: "/api/v1/node-pools/{pool}", h: s.handleUpdateNodePool},
		{Method: "DELETE", Path: "/api/v1/node-pools/{pool}", h: s.handleDeleteNodePool},
		{Method: "GET", Path: "/api/v1/node-pools/{pool}/events", h: s.handleNodePoolEvents},
		{Method: "POST", Path: "/api/v1/node-pools/{pool}/join-command", h: s.handlePoolJoinCommand},
		{Method: "PUT", Path: "/api/v1/nodes/{id}/pool", h: s.handleSetNodePool},
		{Method: "POST", Path: "/api/v1/nodes/{id}/rejoin-token", h: s.handleRejoinToken},
		{Method: "GET", Path: "/api/v1/edges", h: s.handleListEdges},
		{Method: "GET", Path: "/api/v1/system/health", Public: true, h: s.handleHealth},
		{Method: "GET", Path: "/api/v1/metrics/query", h: s.handleExploreMetrics},
		{Method: "GET", Path: "/api/v1/metrics/names", h: s.handleMetricNames},
		{Method: "GET", Path: "/api/v1/metrics/overview", h: s.handleOverviewMetrics},
		{Method: "GET", Path: "/api/v1/s3/endpoints", h: s.handleListS3Endpoints},
		{Method: "POST", Path: "/api/v1/s3/endpoints", h: s.handleCreateS3Endpoint},
		{Method: "GET", Path: "/api/v1/s3/endpoints/{endpoint}", h: s.handleGetS3Endpoint},
		{Method: "PUT", Path: "/api/v1/s3/endpoints/{endpoint}", h: s.handleUpdateS3Endpoint},
		{Method: "DELETE", Path: "/api/v1/s3/endpoints/{endpoint}", h: s.handleDeleteS3Endpoint},
		{Method: "GET", Path: "/api/v1/s3/endpoints/{endpoint}/buckets", h: s.handleListS3Buckets},
		{Method: "POST", Path: "/api/v1/s3/endpoints/{endpoint}/buckets", h: s.handleCreateS3Bucket},
		{Method: "GET", Path: "/api/v1/s3/endpoints/{endpoint}/buckets/{bucket}/objects", h: s.handleListS3Objects},
		{Method: "GET", Path: "/api/v1/s3/endpoints/{endpoint}/buckets/{bucket}/usage", h: s.handleGetS3Usage},
		{Method: "GET", Path: "/api/v1/s3/endpoints/{endpoint}/buckets/{bucket}/object", h: s.handleDownloadS3Object},
		{Method: "PUT", Path: "/api/v1/s3/endpoints/{endpoint}/buckets/{bucket}/object", h: s.handleUploadS3Object},
		{Method: "DELETE", Path: "/api/v1/s3/endpoints/{endpoint}/buckets/{bucket}/object", h: s.handleDeleteS3Object},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/s3", h: s.handleGetServiceS3},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/s3", h: s.handleSetServiceS3},
		{Method: "GET", Path: "/api/v1/system/upgrade", h: s.handleGetUpgrade},
		{Method: "POST", Path: "/api/v1/system/upgrade", h: s.handleStartUpgrade},
		{Method: "GET", Path: "/api/v1/nodes/agent-upgrade", h: s.handleGetAgentUpgrade},
		{Method: "POST", Path: "/api/v1/nodes/agent-upgrade", h: s.handleStartAgentUpgrade},
		{Method: "GET", Path: "/api/v1/docs/cli", h: s.handleListCommands},
		{Method: "POST", Path: "/api/v1/shell", h: s.handleStartShell},
		{Method: "GET", Path: "/api/v1/shell", h: s.handleGetShell},
		{Method: "DELETE", Path: "/api/v1/shell", h: s.handleStopShell},
		{Method: "GET", Path: "/api/v1/shell/exec", h: s.handleExecShell},
		{Method: "GET", Path: "/api/v1/projects/{project}/quota", h: s.handleGetQuota},
		{Method: "PUT", Path: "/api/v1/projects/{project}/quota", h: s.handlePutQuota},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/quota", h: s.handleDeleteQuota},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/quota", h: s.handlePutEnvironmentQuota},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/quota", h: s.handleDeleteQuota},
		{Method: "GET", Path: "/api/v1/usage", h: s.handleGetUsage},
		{Method: "GET", Path: "/api/v1/usage/export", h: s.handleExportUsage},
		{Method: "GET", Path: "/api/v1/audit/export", h: s.handleExportAudit},
		{Method: "POST", Path: "/api/v1/auth/device", Public: true, h: s.handleStartDevice},
		{Method: "POST", Path: "/api/v1/auth/device/token", Public: true, h: s.handleDeviceToken},
		{Method: "POST", Path: "/api/v1/auth/device/approve", h: s.handleApproveDevice},
		{Method: "POST", Path: "/api/v1/nodes/join", Public: true, h: s.handleJoin},
		{Method: "GET", Path: "/api/v1/nodes", h: s.handleListNodes},
		{Method: "DELETE", Path: "/api/v1/nodes/{id}", h: s.handleDeleteNode},
		{Method: "GET", Path: "/api/v1/nodes/{id}/metrics", h: s.handleNodeMetrics},
		{Method: "GET", Path: "/api/v1/nodes/{id}/shell", h: s.handleNodeShell},
		{Method: "GET", Path: "/api/v1/nodes/join-tokens", h: s.handleListJoinTokens},
		{Method: "POST", Path: "/api/v1/nodes/join-tokens", h: s.handleCreateJoinToken},
		{Method: "DELETE", Path: "/api/v1/nodes/join-tokens/{id}", h: s.handleDeleteJoinToken},
		{Method: "GET", Path: "/api/v1/settings/domain", h: s.handleGetDomain},
		{Method: "PUT", Path: "/api/v1/settings/domain", h: s.handleSetDomain},
		{Method: "GET", Path: "/api/v1/certificates", h: s.handleListCertificates},
		{Method: "POST", Path: "/api/v1/certificates/{host}/renew", h: s.handleRenewCertificate},
		{Method: "GET", Path: "/api/v1/backups/config", h: s.handleGetBackupConfig},
		{Method: "PUT", Path: "/api/v1/backups/config", h: s.handleSetBackupConfig},
		{Method: "DELETE", Path: "/api/v1/backups/config", h: s.handleDeleteBackupConfig},
		{Method: "GET", Path: "/api/v1/backups", h: s.handleListBackups},
		{Method: "POST", Path: "/api/v1/backups", h: s.handleRunBackup},
		{Method: "GET", Path: "/api/v1/backups/download", h: s.handleDownloadBackup},
		{Method: "GET", Path: "/api/v1/network/mesh", h: s.handleMesh},
		{Method: "PUT", Path: "/api/v1/nodes/{id}/schedulable", h: s.handleSetSchedulable},
		{Method: "POST", Path: "/api/v1/nodes/{id}/drain", h: s.handleDrainNode},
		{Method: "GET", Path: "/api/v1/projects", h: s.handleListProjects},
		{Method: "POST", Path: "/api/v1/projects", h: s.handleCreateProject},
		{Method: "DELETE", Path: "/api/v1/projects/{project}", h: s.handleDeleteProject},
		{Method: "PUT", Path: "/api/v1/projects/{project}/nodes", h: s.handleSetProjectNodes},
		{Method: "GET", Path: "/api/v1/databases", h: s.handleListAllDatabases},
		{Method: "POST", Path: "/api/v1/databases", h: s.handleCreateDatabase},
		{Method: "GET", Path: "/api/v1/databases/engines", h: s.handleListDatabaseEngines},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/databases", h: s.handleListDatabases},
		{Method: "GET", Path: "/api/v1/databases/{database}", h: s.handleGetDatabase},
		{Method: "PUT", Path: "/api/v1/databases/{database}", h: s.handleUpdateDatabase},
		{Method: "DELETE", Path: "/api/v1/databases/{database}", h: s.handleDeleteDatabase},
		{Method: "PUT", Path: "/api/v1/databases/{database}/network", h: s.handleSetDatabaseNetwork},
		{Method: "GET", Path: "/api/v1/databases/{database}/credentials", h: s.handleDatabaseCredentials},
		{Method: "POST", Path: "/api/v1/databases/{database}/failover", h: s.handleFailoverDatabase},
		{Method: "GET", Path: "/api/v1/databases/{database}/metrics", h: s.handleDatabaseMetrics},
		{Method: "GET", Path: "/api/v1/databases/{database}/events", h: s.handleDatabaseEvents},
		{Method: "GET", Path: "/api/v1/databases/{database}/keys", h: s.handleScanDatabaseKeys},
		{Method: "GET", Path: "/api/v1/databases/{database}/key", h: s.handleGetDatabaseKey},
		{Method: "PUT", Path: "/api/v1/databases/{database}/key", h: s.handleSetDatabaseKey},
		{Method: "DELETE", Path: "/api/v1/databases/{database}/key", h: s.handleDeleteDatabaseKey},
		{Method: "PUT", Path: "/api/v1/databases/{database}/key/ttl", h: s.handleExpireDatabaseKey},
		{Method: "POST", Path: "/api/v1/databases/{database}/command", h: s.handleDatabaseCommand},
		{Method: "GET", Path: "/api/v1/databases/{database}/info", h: s.handleDatabaseInfo},
		{Method: "GET", Path: "/api/v1/databases/{database}/slowlog", h: s.handleDatabaseSlowlog},
		{Method: "GET", Path: "/api/v1/databases/{database}/backups", h: s.handleListDatabaseBackups},
		{Method: "POST", Path: "/api/v1/databases/{database}/backups", h: s.handleStartDatabaseBackup},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/databases", h: s.handleListPgDatabases},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/databases", h: s.handleCreatePgDatabase},
		{Method: "PUT", Path: "/api/v1/databases/{database}/pg/databases/{db}", h: s.handleAlterPgDatabase},
		{Method: "DELETE", Path: "/api/v1/databases/{database}/pg/databases/{db}", h: s.handleDropPgDatabase},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/roles", h: s.handleListPgRoles},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/roles", h: s.handleCreatePgRole},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/roles/{role}", h: s.handleGetPgRole},
		{Method: "PUT", Path: "/api/v1/databases/{database}/pg/roles/{role}", h: s.handleAlterPgRole},
		{Method: "DELETE", Path: "/api/v1/databases/{database}/pg/roles/{role}", h: s.handleDropPgRole},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/schema", h: s.handleGetPgSchema},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/object", h: s.handleGetPgObject},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/rows", h: s.handleReadPgRows},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/privileges", h: s.handleGetPgPrivileges},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/privileges", h: s.handleChangePgPrivileges},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/extensions", h: s.handleListPgExtensions},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/extensions", h: s.handleChangePgExtension},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/query", h: s.handleRunPgQuery},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/execute", h: s.handleExecutePgQuery},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/sessions", h: s.handleListPgSessions},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/settings", h: s.handleListPgSettings},
		{Method: "GET", Path: "/api/v1/databases/{database}/pg/replication", h: s.handleGetPgReplication},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/sessions/{pid}/cancel", h: s.handleCancelPgSession},
		{Method: "POST", Path: "/api/v1/databases/{database}/pg/sessions/{pid}/terminate", h: s.handleTerminatePgSession},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments", h: s.handleListEnvironments},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments", h: s.handleCreateEnvironment},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}", h: s.handleDeleteEnvironment},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/variables", h: s.handleGetSharedEnv},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/metrics", h: s.handleEnvironmentMetrics},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/metrics", h: s.handleServiceMetrics},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/variables", h: s.handleSetSharedEnv},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services", h: s.handleListServices},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}", h: s.handleGetService},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}", h: s.handleApplyService},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}", h: s.handleDeleteService},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/scale", h: s.handleScaleService},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/autoscaling", h: s.handleGetAutoscaling},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/autoscaling", h: s.handlePutAutoscaling},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/autoscaling", h: s.handleDeleteAutoscaling},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/autoscaling/charts", h: s.handleScalingCharts},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/scaling-events", h: s.handleScalingEvents},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/rollback", h: s.handleRollbackService},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/tasks", h: s.handleServiceTasks},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/revisions", h: s.handleServiceRevisions},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/deployments", h: s.handleServiceDeployments},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/deployments/{id}", h: s.handleGetDeployment},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/deployments/{id}/cancel", h: s.handleCancelDeployment},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/redeploy", h: s.handleRedeployService},
		{Method: "GET", Path: "/api/v1/projects/{project}/deployments", h: s.handleProjectDeployments},
		{Method: "PUT", Path: "/api/v1/projects/{project}", h: s.handleUpdateProject},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/domains", h: s.handleListServiceDomains},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/routing", h: s.handleGetRouting},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/routing", h: s.handleSetRouting},
		{Method: "GET", Path: "/api/v1/projects/{project}/addresses", h: s.handleProjectAddresses},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/domains", h: s.handleAddServiceDomain},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/domains/{host}", h: s.handleRemoveServiceDomain},
		{Method: "GET", Path: "/api/v1/domains/check", h: s.handleCheckDomain},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/run", h: s.handleRunService},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/git", h: s.handleGetGitSource},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/git", h: s.handleSetGitSource},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/git", h: s.handleDeleteGitSource},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/git/build-settings", h: s.handleGetBuildSettings},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/git/build-settings", h: s.handleSetBuildSettings},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/builds", h: s.handleServiceBuilds},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/builds", h: s.handleStartBuild},
		{Method: "GET", Path: "/api/v1/builds", h: s.handleRecentBuilds},
		{Method: "GET", Path: "/api/v1/git/sources", h: s.handleListGitSources},
		{Method: "POST", Path: "/api/v1/builds/{id}/deploy", h: s.handleDeployBuild},
		{Method: "POST", Path: "/api/v1/hooks/git/{id}", Public: true, h: s.handleGitWebhook},
		{Method: "POST", Path: "/api/v1/hooks/github-app/{id}", Public: true, h: s.handleGitHubAppWebhook},
		{Method: "GET", Path: "/api/v1/gitserver", h: s.handleGetGitServer},
		{Method: "PUT", Path: "/api/v1/gitserver", h: s.handleSetGitServer},
		{Method: "GET", Path: "/api/v1/gitserver/credentials", h: s.handleGitServerCredentials},
		{Method: "GET", Path: "/api/v1/integrations/git", h: s.handleListGitConnections},
		{Method: "POST", Path: "/api/v1/integrations/git", h: s.handleCreateGitConnection},
		{Method: "GET", Path: "/api/v1/integrations/git/{connection}", h: s.handleGetGitConnection},
		{Method: "DELETE", Path: "/api/v1/integrations/git/{connection}", h: s.handleDeleteGitConnection},
		{Method: "GET", Path: "/api/v1/integrations/git/{connection}/repos", h: s.handleListGitRepos},
		{Method: "GET", Path: "/api/v1/integrations/git/{connection}/branches", h: s.handleListGitBranches},
		{Method: "POST", Path: "/api/v1/integrations/github/manifest", h: s.handleGitHubManifest},
		{Method: "GET", Path: "/api/v1/integrations/github/callback", h: s.handleGitHubCallback},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/jobs", h: s.handleListJobs},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/jobs/{job}", h: s.handleGetJob},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/jobs/{job}", h: s.handleApplyJob},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/jobs/{job}", h: s.handleDeleteJob},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/jobs/{job}/runs", h: s.handleRunJob},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/jobs/{job}/runs", h: s.handleJobRuns},
		{Method: "GET", Path: "/api/v1/jobs", h: s.handleListAllJobs},
		{Method: "GET", Path: "/api/v1/runs/{id}", h: s.handleGetRun},
		{Method: "POST", Path: "/api/v1/runs/{id}/cancel", h: s.handleCancelRun},
		{Method: "GET", Path: "/api/v1/health/services", h: s.handleServiceHealth},
		{Method: "GET", Path: "/api/v1/health/incidents", h: s.handleIncidents},
		{Method: "GET", Path: "/api/v1/services", h: s.handleListAllServices},
		{Method: "GET", Path: "/api/v1/tasks", h: s.handleListTasks},
		{Method: "GET", Path: "/api/v1/alerts/channels", h: s.handleListAlertChannels},
		{Method: "POST", Path: "/api/v1/alerts/channels", h: s.handleCreateAlertChannel},
		{Method: "PUT", Path: "/api/v1/alerts/channels/{id}", h: s.handleUpdateAlertChannel},
		{Method: "DELETE", Path: "/api/v1/alerts/channels/{id}", h: s.handleDeleteAlertChannel},
		{Method: "POST", Path: "/api/v1/alerts/channels/{id}/test", h: s.handleTestAlertChannel},
		{Method: "GET", Path: "/api/v1/alerts/rules", h: s.handleListAlertRules},
		{Method: "POST", Path: "/api/v1/alerts/rules", h: s.handleCreateAlertRule},
		{Method: "PUT", Path: "/api/v1/alerts/rules/{id}", h: s.handleUpdateAlertRule},
		{Method: "DELETE", Path: "/api/v1/alerts/rules/{id}", h: s.handleDeleteAlertRule},
		{Method: "GET", Path: "/api/v1/alerts/active", h: s.handleActiveAlerts},
		{Method: "GET", Path: "/api/v1/alerts/events", h: s.handleAlertEvents},
		{Method: "GET", Path: "/api/v1/traffic", h: s.handleTraffic},
		{Method: "GET", Path: "/api/v1/traffic/map", h: s.handleTrafficMap},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/traffic", h: s.handleEnvironmentTraffic},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/traffic", h: s.handleServiceTraffic},
		{Method: "GET", Path: "/api/v1/logs", h: s.handleQueryLogs},
		{Method: "GET", Path: "/api/v1/logs/tail", h: s.handleTailLogs},
		{Method: "POST", Path: "/api/v1/tasks/{id}/restart", h: s.handleRestartTask},
		{Method: "GET", Path: "/api/v1/tasks/{id}/exec", h: s.handleExec},
		{Method: "GET", Path: "/api/v1/firewall/policies", h: s.handleListFirewallPolicies},
		{Method: "POST", Path: "/api/v1/firewall/policies", h: s.handleCreateFirewallPolicy},
		{Method: "PUT", Path: "/api/v1/firewall/policies/{id}", h: s.handleUpdateFirewallPolicy},
		{Method: "DELETE", Path: "/api/v1/firewall/policies/{id}", h: s.handleDeleteFirewallPolicy},
		{Method: "GET", Path: "/api/v1/firewall/nodes/{id}/effective", h: s.handleEffectiveFirewall},
		{Method: "GET", Path: "/api/v1/firewall/drops", h: s.handleFirewallDrops},
		{Method: "GET", Path: "/api/v1/firewall/counters", h: s.handleFirewallCounters},
		{Method: "GET", Path: "/api/v1/security-groups", h: s.handleListAllSecurityGroups},
		{Method: "GET", Path: "/api/v1/projects/{project}/security-groups", h: s.handleListSecurityGroups},
		{Method: "POST", Path: "/api/v1/projects/{project}/security-groups", h: s.handleCreateSecurityGroup},
		{Method: "POST", Path: "/api/v1/projects/{project}/security-groups/preview", h: s.handlePreviewSecurityGroup},
		{Method: "GET", Path: "/api/v1/projects/{project}/security-groups/{group}", h: s.handleGetSecurityGroup},
		{Method: "PUT", Path: "/api/v1/projects/{project}/security-groups/{group}", h: s.handleUpdateSecurityGroup},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/security-groups/{group}", h: s.handleDeleteSecurityGroup},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/security", h: s.handleServiceSecurity},
		{Method: "POST", Path: "/api/v1/network/reachability", h: s.handleReachability},
		{Method: "GET", Path: "/api/v1/network/ipam", h: s.handleIPAM},
		{Method: "GET", Path: "/api/v1/middlewares", h: s.handleListAllMiddlewares},
		{Method: "GET", Path: "/api/v1/projects/{project}/middlewares", h: s.handleListMiddlewares},
		{Method: "POST", Path: "/api/v1/projects/{project}/middlewares", h: s.handleCreateMiddleware},
		{Method: "PUT", Path: "/api/v1/projects/{project}/middlewares/{middleware}", h: s.handleUpdateMiddleware},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/middlewares/{middleware}", h: s.handleDeleteMiddleware},
		{Method: "GET", Path: "/api/v1/traefik/config", h: s.handleTraefikConfig},
		{Method: "GET", Path: "/api/v1/traefik/settings", h: s.handleGetTraefikSettings},
		{Method: "PUT", Path: "/api/v1/traefik/settings", h: s.handlePutTraefikSettings},
		{Method: "GET", Path: "/api/v1/traefik/custom", h: s.handleGetTraefikCustom},
		{Method: "PUT", Path: "/api/v1/traefik/custom", h: s.handlePutTraefikCustom},
		{Method: "POST", Path: "/api/v1/traefik/custom/validate", h: s.handleValidateTraefikCustom},
		{Method: "GET", Path: "/api/v1/network/throughput", h: s.handleNetworkThroughput},
		{Method: "GET", Path: "/api/v1/network/ipam/history", h: s.handleIPHistory},
		{Method: "GET", Path: "/api/v1/network/dns", h: s.handleDNSRecords},
		{Method: "GET", Path: "/api/v1/network/dns/lookup", h: s.handleDNSLookup},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.Routes() {
		h := rt.h
		if !rt.Public {
			h = s.requireAuth(s.authorize(rt.Method, rt.Path, h))
		}
		mux.HandleFunc(rt.Method+" "+rt.Path, h)
	}
	mux.HandleFunc("GET /join.sh", s.handleJoinScript)
	mux.HandleFunc("GET /downloads/{file}", s.handleDownload)
	for pattern, h := range s.internal {
		mux.Handle(pattern, h)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
	})
	mux.Handle("/", spaHandler(s.web))

	return s.recoverPanics(s.logRequests(checkOrigin(securityHeaders(mux))))
}

func handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(OpenAPISpec)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSystemTasks(w http.ResponseWriter, _ *http.Request) {
	items := []system.TaskView{}
	if s.system != nil {
		items = s.system.List()
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleMesh(w http.ResponseWriter, r *http.Request) {
	items := []mesh.NodeView{}
	if s.mesh != nil {
		var err error
		if items, err = s.mesh.List(r.Context()); err != nil {
			s.internalError(w, "list mesh", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meshCidr": mesh.MeshCIDR.String(), "containerCidr": mesh.ContainerCIDR.String(), "serviceCidr": mesh.ServiceCIDR.String(),
		"items": items,
	})
}

func (s *Server) handleServiceHealth(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	items, err := s.health.List(r.Context())
	if err != nil {
		s.internalError(w, "service health", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, items, itemServiceField)})
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListIncidents(r.Context(), r.URL.Query().Get("service"), r.URL.Query().Get("open") == "1", 200)
	if err != nil {
		s.internalError(w, "list incidents", err)
		return
	}
	type view struct {
		store.Incident
		Project     string `json:"project"`
		Environment string `json:"environment"`
		Service     string `json:"service"`
	}
	out := make([]view, 0, len(items))
	for _, i := range items {
		v := view{Incident: i}
		if sv, err := s.store.ServiceByID(r.Context(), i.ServiceID); err == nil {
			v.Project, v.Environment, v.Service = sv.Project, sv.Environment, sv.Name
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, out, itemServiceField)})
}
