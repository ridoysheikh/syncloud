package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ridoysheikh/syncloud/internal/shell"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Cloud Shell (§7.1): a terminal in the dashboard with synctl signed in as
// the user, through temporary credentials (1 hour) that carry the user's
// own permissions.

const shellTTL = time.Hour

func (s *Server) requireShell(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if s.shell == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "Cloud Shell is not enabled")
		return Principal{}, false
	}
	p, _ := principal(r.Context())
	if p.CredType != CredSession {
		writeError(w, http.StatusForbidden, CodeForbidden, "Cloud Shell is opened from the dashboard")
		return p, false
	}
	return p, true
}

func (s *Server) handleStartShell(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireShell(w, r)
	if !ok {
		return
	}
	sess, err := s.shell.Start(r.Context(), p.User.ID, func() (shell.Credentials, time.Time, error) {
		c, err := s.issueTemp(r.Context(), store.TempShell, p.User.ID, "", p.MFA, clientIP(r), shellTTL)
		return shell.Credentials{AccessKeyID: c.AccessKeyID, Secret: c.SecretAccessKey, SessionToken: c.SessionToken}, c.Expiration, err
	})
	if errors.Is(err, shell.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
		return
	} else if err != nil {
		s.internalError(w, "start shell", err)
		return
	}
	s.audit(r, p.User.ID, "shell:StartShell", "srn:syncloud:user/"+p.User.ID, nil)
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleGetShell(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireShell(w, r)
	if !ok {
		return
	}
	sess, found := s.shell.Get(p.User.ID)
	if !found {
		writeError(w, http.StatusNotFound, CodeNotFound, "no shell running")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleStopShell(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireShell(w, r)
	if !ok {
		return
	}
	s.shell.Stop(p.User.ID)
	s.audit(r, p.User.ID, "shell:StopShell", "srn:syncloud:user/"+p.User.ID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleExecShell attaches a terminal to the caller's shell (WebSocket, as
// task exec).
func (s *Server) handleExecShell(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireShell(w, r)
	if !ok {
		return
	}
	sess, found := s.shell.Get(p.User.ID)
	if !found || sess.State != "running" {
		writeError(w, http.StatusConflict, CodeConflict, "the shell is not running yet")
		return
	}
	s.shell.Touch(p.User.ID)
	q := r.URL.Query()
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, _ := strconv.Atoi(q.Get("rows"))
	s.audit(r, p.User.ID, "shell:ExecShell", "srn:syncloud:user/"+p.User.ID, nil)
	s.relayExec(w, r, sess.NodeID, sess.TaskID, []string{"sh", "-l"}, true, cols, rows)
}
