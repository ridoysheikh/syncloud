package api

import (
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
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

// clientIP returns the caller's address. X-Forwarded-For is only trusted when
// the direct peer is loopback, i.e. Traefik on the controller host (D20);
// Traefik appends the real client address as the last entry.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if fromLocalProxy(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	return host
}

func fromLocalProxy(peer string) bool {
	ip := net.ParseIP(peer)
	return ip != nil && ip.IsLoopback()
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return fromLocalProxy(host) && r.Header.Get("X-Forwarded-Proto") == "https"
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
