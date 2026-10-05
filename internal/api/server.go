// Package api is the controller's public HTTP API (§5.1). The dashboard,
// synctl and CI all use these same endpoints; there are no UI-only routes.
package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"syncloud/internal/events"
	"syncloud/internal/store"
)

type Server struct {
	store *store.Store
	bus   *events.Bus
	log   *slog.Logger
	web   fs.FS // built dashboard (may be empty in development)
	now   func() time.Time

	loginLimiter *attemptLimiter
	setupLimiter *attemptLimiter
}

type Options struct {
	Store *store.Store
	Bus   *events.Bus
	Log   *slog.Logger
	Web   fs.FS
	// Now is overridable for tests.
	Now func() time.Time
}

func New(o Options) *Server {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Server{
		store:        o.Store,
		bus:          o.Bus,
		log:          o.Log,
		web:          o.Web,
		now:          o.Now,
		loginLimiter: newAttemptLimiter(10, 5*time.Minute),
		setupLimiter: newAttemptLimiter(10, 5*time.Minute),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/system/status", s.handleStatus)
	mux.HandleFunc("POST /api/v1/setup", s.handleSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireSession(s.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/me", s.requireSession(s.handleMe))
	mux.HandleFunc("GET /api/v1/stream", s.requireSession(s.handleStream))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
	})
	mux.Handle("/", spaHandler(s.web))

	return s.recoverPanics(s.logRequests(checkOrigin(securityHeaders(mux))))
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
