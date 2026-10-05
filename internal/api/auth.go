package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/store"
	"syncloud/internal/version"
)

type statusResponse struct {
	Version       string `json:"version"`
	SetupRequired bool   `json:"setupRequired"`
	BaseDomain    string `json:"baseDomain"`
	// RecoveryConfirmRequired asks the setup wizard for the recovery key's last 6 characters.
	RecoveryConfirmRequired bool `json:"recoveryConfirmRequired"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.internalError(w, "count users", err)
		return
	}
	baseDomain, _, err := s.store.GetSetting(r.Context(), store.SettingBaseDomain)
	if err != nil {
		s.internalError(w, "get base domain", err)
		return
	}
	resp := statusResponse{Version: version.Version, SetupRequired: n == 0, BaseDomain: baseDomain}
	if resp.SetupRequired {
		_, resp.RecoveryConfirmRequired, err = s.store.GetSetting(r.Context(), store.SettingRecoverySuffixHash)
		if err != nil {
			s.internalError(w, "get recovery setting", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type userResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	IsRoot    bool      `json:"isRoot"`
	CreatedAt time.Time `json:"createdAt"`
}

func toUserResponse(u store.User) userResponse {
	return userResponse{ID: u.ID, Email: u.Email, Name: u.Name, IsRoot: u.IsRoot, CreatedAt: u.CreatedAt.UTC()}
}

type setupRequest struct {
	SetupToken string `json:"setupToken"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Password   string `json:"password"`
	// RecoveryKeySuffix is the last 6 characters of the recovery key.
	RecoveryKeySuffix string `json:"recoveryKeySuffix"`
}

// handleSetup exchanges the one-time setup token for the root account (§5.0, §7.1)
// and signs the new account in.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	if !s.setupLimiter.Allow(clientIP(r), now) {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts, try again later")
		return
	}
	var req setupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.Name = strings.TrimSpace(req.Name)

	ok, err := auth.CheckSetupToken(r.Context(), s.store, strings.TrimSpace(req.SetupToken), now)
	if err != nil {
		s.internalError(w, "check setup token", err)
		return
	}
	if !ok {
		s.audit(r, "", "setup:CreateRoot", "srn:syncloud:setup", map[string]any{"result": "invalid_token"})
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid or expired setup token; restart the controller to print a new one")
		return
	}
	if msg := validateAccount(req.Email, req.Name); msg != "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, msg)
		return
	}
	if want, ok, err := s.store.GetSetting(r.Context(), store.SettingRecoverySuffixHash); err != nil {
		s.internalError(w, "get recovery setting", err)
		return
	} else if ok && !auth.TokenMatches(strings.ToUpper(strings.TrimSpace(req.RecoveryKeySuffix)), want) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the last 6 characters of the recovery key do not match; it was printed with the setup token")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if errors.Is(err, auth.ErrWeakPassword) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	} else if err != nil {
		s.internalError(w, "hash password", err)
		return
	}

	u := store.User{ID: auth.NewID("usr_"), Email: req.Email, Name: req.Name, PasswordHash: hash, IsRoot: true, CreatedAt: now.Truncate(time.Second)}
	if err := s.store.CreateRootUser(r.Context(), u); errors.Is(err, store.ErrSetupDone) {
		writeError(w, http.StatusConflict, CodeConflict, "setup has already been completed")
		return
	} else if err != nil {
		s.internalError(w, "create root user", err)
		return
	}
	s.audit(r, u.ID, "setup:CreateRoot", "srn:syncloud:user/"+u.ID, nil)
	s.log.Info("setup completed", "user", u.Email)
	if s.onSetup != nil {
		s.onSetup()
	}

	if !s.startSession(w, r, u) {
		return
	}
	writeJSON(w, http.StatusCreated, toUserResponse(u))
}

func validateAccount(email, name string) string {
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return "a valid email address is required"
	}
	if name == "" || len(name) > 100 {
		return "name must be 1–100 characters"
	}
	return ""
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	if !s.loginLimiter.Allow(clientIP(r), now) {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts, try again later")
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.TrimSpace(req.Email)

	u, err := s.store.UserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, "get user", err)
		return
	}
	valid := false
	if err == nil {
		if valid, err = auth.VerifyPassword(req.Password, u.PasswordHash); err != nil {
			s.internalError(w, "verify password", err)
			return
		}
	} else {
		// Spend the same time as a real check so response timing doesn't reveal which emails exist.
		_, _ = auth.VerifyPassword(req.Password, dummyHash())
	}
	if !valid {
		s.audit(r, u.ID, "auth:Login", "srn:syncloud:user/"+email, map[string]any{"result": "denied"})
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "email or password is incorrect")
		return
	}
	s.audit(r, u.ID, "auth:Login", "srn:syncloud:user/"+u.ID, nil)
	if !s.startSession(w, r, u) {
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// dummyHash is a valid argon2id hash of a random password, used for timing equalization.
var dummyHash = sync.OnceValue(func() string {
	h, err := auth.HashPassword(auth.NewToken("dummy_"))
	if err != nil {
		panic(err)
	}
	return h
})

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.DeleteSession(r.Context(), auth.HashToken(c.Value)); err != nil {
			s.internalError(w, "delete session", err)
			return
		}
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "auth:Logout", "srn:syncloud:user/"+u.ID, nil)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r.Context())
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) bool {
	now := s.now()
	token := auth.NewToken("syn_sess_")
	err := s.store.CreateSession(r.Context(), store.Session{
		TokenHash: auth.HashToken(token), UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(sessionTTL),
		LastSeenAt: now, IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	if err != nil {
		s.internalError(w, "create session", err)
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
		// No MaxAge: the browser keeps it for the session; the server enforces expiry.
	})
	return true
}

func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
}
