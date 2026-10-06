package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/sigv"
	"syncloud/internal/store"
)

// Credential types a request can authenticate with (§7.1).
const (
	CredSession   = "session"
	CredToken     = "token"
	CredAccessKey = "access_key"
	// CredRole is an assumed role (sts); CredTemporary a device-login or
	// Cloud Shell credential acting as the user.
	CredRole      = "role_session"
	CredTemporary = "temporary"
)

// HeaderSessionToken carries the session token of temporary credentials.
const HeaderSessionToken = "X-Syncloud-Session-Token"

// Principal is who is making a request and with which credential.
type Principal struct {
	User       store.User
	CredType   string
	CredID     string // access key ID or token ID; empty for sessions
	RoleID     string // set for role sessions: the role's policies apply
	MFA        bool   // the credential was obtained with a TOTP code
	MFAEnabled bool   // the user has MFA set up
	Kind       string // user | service
}

type ctxKey int

const principalKey ctxKey = iota

const (
	sessionCookie = "syncloud_session"
	sessionTTL    = 12 * time.Hour
	// Sessions and credentials record use at most this often, to avoid a write per request.
	touchEvery = time.Minute
)

func principal(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

func currentUser(ctx context.Context) (store.User, bool) {
	p, ok := principal(ctx)
	return p.User, ok
}

// authFailure is a client-facing authentication error.
type authFailure struct{ msg string }

func (e authFailure) Error() string { return e.msg }

// requireAuth accepts, in order: a signed access-key request, a bearer
// personal access token, or the dashboard session cookie.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			p   Principal
			err error
		)
		authz := r.Header.Get("Authorization")
		switch {
		case sigv.IsSigned(authz):
			p, err = s.authAccessKey(w, r, authz)
		case strings.HasPrefix(authz, "Bearer "):
			p, err = s.authToken(r, strings.TrimPrefix(authz, "Bearer "))
		case authz != "":
			err = authFailure{"unsupported Authorization scheme"}
		default:
			p, err = s.authSession(r)
		}
		var af authFailure
		if errors.As(err, &af) {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, af.msg)
			return
		} else if err != nil {
			s.internalError(w, "authenticate", err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	}
}

func (s *Server) authSession(r *http.Request) (Principal, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return Principal{}, authFailure{"not signed in"}
	}
	now := s.now()
	hash := auth.HashToken(c.Value)
	sess, err := s.store.SessionByHash(r.Context(), hash, now)
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, authFailure{"session expired"}
	} else if err != nil {
		return Principal{}, err
	}
	u, err := s.userFor(r.Context(), sess.UserID)
	if err != nil {
		return Principal{}, err
	}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		if err := s.store.TouchSession(r.Context(), hash, now, now.Add(sessionTTL)); err != nil {
			s.log.Warn("touch session", "err", err)
		}
	}
	p := s.principalFor(u, CredSession, "")
	p.MFA = sess.MFA
	return p, nil
}

func (s *Server) authToken(r *http.Request, token string) (Principal, error) {
	now := s.now()
	t, err := s.store.APITokenByHash(r.Context(), auth.HashToken(strings.TrimSpace(token)), now)
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, authFailure{"invalid or expired token"}
	} else if err != nil {
		return Principal{}, err
	}
	u, err := s.userFor(r.Context(), t.UserID)
	if err != nil {
		return Principal{}, err
	}
	if !ipAllowed(t.AllowedIPs, clientIP(r)) {
		return Principal{}, authFailure{"this token cannot be used from " + clientIP(r)}
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= touchEvery {
		if err := s.store.TouchAPIToken(r.Context(), t.ID, clientIP(r), now); err != nil {
			s.log.Warn("touch token", "err", err)
		}
	}
	return s.principalFor(u, CredToken, t.ID), nil
}

func (s *Server) authAccessKey(w http.ResponseWriter, r *http.Request, authz string) (Principal, error) {
	keyID, sig, err := sigv.ParseAuthorization(authz)
	if err != nil {
		return Principal{}, authFailure{err.Error()}
	}
	if strings.HasPrefix(keyID, auth.TempKeyPrefix) {
		return s.authTemporary(w, r, keyID, sig)
	}
	k, err := s.store.AccessKeyByID(r.Context(), keyID)
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, authFailure{"unknown access key"}
	} else if err != nil {
		return Principal{}, err
	}
	if k.ExpiresAt != nil && !s.now().Before(*k.ExpiresAt) {
		return Principal{}, authFailure{"access key expired"}
	}
	if !ipAllowed(k.AllowedIPs, clientIP(r)) {
		return Principal{}, authFailure{"this access key cannot be used from " + clientIP(r)}
	}
	secret, err := s.secrets.Open(k.SecretEnc, []byte(k.ID))
	if err != nil {
		return Principal{}, err
	}
	// The signature covers the body, so read it here and hand handlers a fresh reader.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return Principal{}, authFailure{"request body too large or unreadable"}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	now := s.now()
	if err := sigv.Verify(r, string(secret), sig, body, now); err != nil {
		return Principal{}, authFailure{err.Error()}
	}
	u, err := s.userFor(r.Context(), k.UserID)
	if err != nil {
		return Principal{}, err
	}
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) >= touchEvery {
		if err := s.store.TouchAccessKey(r.Context(), k.ID, clientIP(r), now); err != nil {
			s.log.Warn("touch access key", "err", err)
		}
	}
	return s.principalFor(u, CredAccessKey, k.ID), nil
}

// authTemporary verifies a request signed with temporary credentials (role
// sessions, synctl login, Cloud Shell): the key must be live and the
// request must carry its session token.
func (s *Server) authTemporary(w http.ResponseWriter, r *http.Request, keyID, sig string) (Principal, error) {
	now := s.now()
	c, err := s.store.TempCredentialByID(r.Context(), keyID, now)
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, authFailure{"temporary credentials expired or unknown"}
	} else if err != nil {
		return Principal{}, err
	}
	tok := r.Header.Get(HeaderSessionToken)
	if tok == "" || subtle.ConstantTimeCompare([]byte(auth.HashToken(tok)), []byte(c.TokenHash)) != 1 {
		return Principal{}, authFailure{"missing or wrong " + HeaderSessionToken}
	}
	secret, err := s.secrets.Open(c.SecretEnc, []byte(c.ID))
	if err != nil {
		return Principal{}, err
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return Principal{}, authFailure{"request body too large or unreadable"}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := sigv.Verify(r, string(secret), sig, body, now); err != nil {
		return Principal{}, authFailure{err.Error()}
	}
	u, err := s.userFor(r.Context(), c.UserID)
	if err != nil {
		return Principal{}, err
	}
	if c.LastUsedAt == nil || now.Sub(*c.LastUsedAt) >= touchEvery {
		_ = s.store.TouchTempCredential(r.Context(), c.ID, now)
	}
	p := s.principalFor(u, CredTemporary, c.ID)
	if c.Kind == store.TempRole {
		p.CredType, p.RoleID = CredRole, c.RoleID
	}
	p.MFA = c.MFA
	return p, nil
}

func (s *Server) principalFor(u store.IAMUser, cred, id string) Principal {
	return Principal{User: u.User, CredType: cred, CredID: id, MFAEnabled: u.MFAEnabled, Kind: u.Kind}
}

func (s *Server) userFor(ctx context.Context, id string) (store.IAMUser, error) {
	u, err := s.store.IAMUser(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return u, authFailure{"account no longer exists"}
	}
	if err == nil && u.Disabled {
		return u, authFailure{"account disabled"}
	}
	return u, err
}

// ipAllowed checks a credential's IP allow-list (empty: anywhere).
func ipAllowed(allowed []string, ip string) bool {
	if len(allowed) == 0 {
		return true
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, c := range allowed {
		if p, err := netip.ParsePrefix(c); err == nil && p.Contains(a.Unmap()) {
			return true
		}
		if x, err := netip.ParseAddr(c); err == nil && x == a.Unmap() {
			return true
		}
	}
	return false
}
