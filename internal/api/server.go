// Package api is the controller's public HTTP API (§5.1). The dashboard,
// synctl and CI all use these same endpoints; there are no UI-only routes.
package api

import (
	_ "embed"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"syncloud/internal/events"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
	"syncloud/internal/system"
)

// OpenAPISpec is the API contract, served at /api/v1/openapi.json.
// A test keeps it in sync with Routes.
//
//go:embed openapi.json
var OpenAPISpec []byte

type Server struct {
	store       *store.Store
	secrets     *secrets.Box
	ca          *pki.CA
	nodes       *nodes.Registry
	gatewayAddr string
	system      *system.Manager
	internal    map[string]http.Handler
	bus         *events.Bus
	log         *slog.Logger
	web         fs.FS // built dashboard (may be empty in development)
	now         func() time.Time

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
	// Internal handlers are mounted as-is outside the public API (e.g. the
	// Traefik config endpoint) and carry their own authentication.
	Internal map[string]http.Handler
	Bus      *events.Bus
	Log      *slog.Logger
	Web      fs.FS
	// Now is overridable for tests.
	Now func() time.Time
}

func New(o Options) *Server {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Server{
		store:        o.Store,
		secrets:      o.Secrets,
		ca:           o.CA,
		nodes:        o.Nodes,
		gatewayAddr:  o.GatewayAddr,
		system:       o.System,
		internal:     o.Internal,
		bus:          o.Bus,
		log:          o.Log,
		web:          o.Web,
		now:          o.Now,
		loginLimiter: newAttemptLimiter(10, 5*time.Minute),
		setupLimiter: newAttemptLimiter(10, 5*time.Minute),
		joinLimiter:  newAttemptLimiter(20, 5*time.Minute),
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
