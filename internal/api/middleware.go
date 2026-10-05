package api

import (
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"time"

	"syncloud/internal/store"
)

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController (used by the WebSocket upgrade) reach the real writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// checkOrigin rejects cross-site state-changing requests (CSRF protection for
// cookie sessions). Requests without an Origin header (CLI, curl) pass; they
// can't carry the session cookie from a browser on another site.
func checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, CodeInvalidOrigin, "cross-origin request rejected")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the direct peer address. Once Traefik fronts the controller
// (Phase 0b) this must honor X-Forwarded-For from trusted proxies only.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// audit records an action. The credential used (session, token or access key)
// is added automatically for authenticated requests (§7.1).
func (s *Server) audit(r *http.Request, actorID, action, resource string, detail map[string]any) {
	if p, ok := principal(r.Context()); ok {
		if detail == nil {
			detail = map[string]any{}
		}
		detail["credType"] = p.CredType
		if p.CredID != "" {
			detail["credId"] = p.CredID
		}
	}
	err := s.store.WriteAudit(r.Context(), store.AuditEvent{
		At: s.now(), ActorID: actorID, Action: action, Resource: resource,
		IP: clientIP(r), UserAgent: r.UserAgent(), Detail: detail,
	})
	if err != nil {
		s.log.Error("write audit event", "action", action, "err", err)
	}
}
