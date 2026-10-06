// Package api is the controller's public HTTP API (§5.1). The dashboard,
// synctl and CI all use these same endpoints; there are no UI-only routes.
package api

import (
	_ "embed"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"syncloud/internal/backup"
	"syncloud/internal/builds"
	"syncloud/internal/certs"
	"syncloud/internal/domain"
	"syncloud/internal/events"
	"syncloud/internal/execrelay"
	"syncloud/internal/health"
	"syncloud/internal/jobs"
	"syncloud/internal/logs"
	"syncloud/internal/mesh"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	"syncloud/internal/registry"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
	"syncloud/internal/system"
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
	logs                  *logs.Store
	exec                  *execrelay.Relay
	jobs                  *jobs.Manager
	health                *health.Monitor
	registryBrowser       *registry.Browser
	builds                *builds.Manager
	controllerSchedulable bool
	bus                   *events.Bus
	log                   *slog.Logger
	web                   fs.FS // built dashboard (may be empty in development)
	now                   func() time.Time

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
	return &Server{
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
		logs:                  o.Logs,
		exec:                  o.Exec,
		jobs:                  o.Jobs,
		health:                o.Health,
		registryBrowser:       o.RegistryBrowser,
		builds:                o.Builds,
		controllerSchedulable: o.ControllerSchedulable,
		bus:                   o.Bus,
		log:                   o.Log,
		web:                   o.Web,
		now:                   o.Now,
		loginLimiter:          newAttemptLimiter(10, 5*time.Minute),
		setupLimiter:          newAttemptLimiter(10, 5*time.Minute),
		joinLimiter:           newAttemptLimiter(20, 5*time.Minute),
	}
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
		{Method: "POST", Path: "/api/v1/nodes/join", Public: true, h: s.handleJoin},
		{Method: "GET", Path: "/api/v1/nodes", h: s.handleListNodes},
		{Method: "DELETE", Path: "/api/v1/nodes/{id}", h: s.handleDeleteNode},
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
		{Method: "GET", Path: "/api/v1/projects/{project}/environments", h: s.handleListEnvironments},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments", h: s.handleCreateEnvironment},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}", h: s.handleDeleteEnvironment},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/variables", h: s.handleGetSharedEnv},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/variables", h: s.handleSetSharedEnv},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services", h: s.handleListServices},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}", h: s.handleGetService},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}", h: s.handleApplyService},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}", h: s.handleDeleteService},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/scale", h: s.handleScaleService},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/rollback", h: s.handleRollbackService},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/tasks", h: s.handleServiceTasks},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/revisions", h: s.handleServiceRevisions},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/deployments", h: s.handleServiceDeployments},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/domains", h: s.handleListServiceDomains},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/domains", h: s.handleAddServiceDomain},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/domains/{host}", h: s.handleRemoveServiceDomain},
		{Method: "GET", Path: "/api/v1/domains/check", h: s.handleCheckDomain},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/run", h: s.handleRunService},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/git", h: s.handleGetGitSource},
		{Method: "PUT", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/git", h: s.handleSetGitSource},
		{Method: "DELETE", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/git", h: s.handleDeleteGitSource},
		{Method: "GET", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/builds", h: s.handleServiceBuilds},
		{Method: "POST", Path: "/api/v1/projects/{project}/environments/{env}/services/{service}/builds", h: s.handleStartBuild},
		{Method: "GET", Path: "/api/v1/builds", h: s.handleRecentBuilds},
		{Method: "POST", Path: "/api/v1/builds/{id}/deploy", h: s.handleDeployBuild},
		{Method: "POST", Path: "/api/v1/hooks/git/{id}", Public: true, h: s.handleGitWebhook},
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
		{Method: "GET", Path: "/api/v1/logs", h: s.handleQueryLogs},
		{Method: "GET", Path: "/api/v1/logs/tail", h: s.handleTailLogs},
		{Method: "POST", Path: "/api/v1/tasks/{id}/restart", h: s.handleRestartTask},
		{Method: "GET", Path: "/api/v1/tasks/{id}/exec", h: s.handleExec},
		{Method: "GET", Path: "/api/v1/firewall/policies", h: s.handleListFirewallPolicies},
		{Method: "POST", Path: "/api/v1/firewall/policies", h: s.handleCreateFirewallPolicy},
		{Method: "PUT", Path: "/api/v1/firewall/policies/{id}", h: s.handleUpdateFirewallPolicy},
		{Method: "DELETE", Path: "/api/v1/firewall/policies/{id}", h: s.handleDeleteFirewallPolicy},
		{Method: "GET", Path: "/api/v1/firewall/nodes/{id}/effective", h: s.handleEffectiveFirewall},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.Routes() {
		h := rt.h
		if !rt.Public {
			h = s.requireAuth(h)
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
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
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
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
