package api

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/store"
)

type ctxKey int

const userKey ctxKey = iota

const (
	sessionCookie = "syncloud_session"
	sessionTTL    = 12 * time.Hour
	// Sessions are extended at most this often, to avoid a write per request.
	sessionTouchEvery = time.Minute
)

func currentUser(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userKey).(store.User)
	return u, ok
}

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

// requireSession authenticates the request from the session cookie.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "not signed in")
			return
		}
		now := s.now()
		hash := auth.HashToken(c.Value)
		sess, err := s.store.SessionByHash(r.Context(), hash, now)
		if err != nil {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "session expired")
			return
		}
		u, err := s.store.UserByID(r.Context(), sess.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "session expired")
			return
		}
		if now.Sub(sess.LastSeenAt) >= sessionTouchEvery {
			if err := s.store.TouchSession(r.Context(), hash, now, now.Add(sessionTTL)); err != nil {
				s.log.Warn("touch session", "err", err)
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	}
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

func (s *Server) audit(r *http.Request, actorID, action, resource string, detail map[string]any) {
	err := s.store.WriteAudit(r.Context(), store.AuditEvent{
		At: s.now(), ActorID: actorID, Action: action, Resource: resource,
		IP: clientIP(r), UserAgent: r.UserAgent(), Detail: detail,
	})
	if err != nil {
		s.log.Error("write audit event", "action", action, "err", err)
	}
}
