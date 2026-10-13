package api

import (
	"errors"
	"net/http"

	"github.com/ridoysheikh/syncloud/internal/backup"
	"github.com/ridoysheikh/syncloud/internal/secrets"
)

func (s *Server) requireRoot(w http.ResponseWriter, r *http.Request) bool {
	if p, _ := principal(r.Context()); !p.isRoot() {
		writeError(w, http.StatusForbidden, CodeForbidden, "only the root user can see the recovery key")
		return false
	}
	return true
}

// handleGetRecoveryKey shows the recovery key kept on this instance.
func (s *Server) handleGetRecoveryKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(w, r) {
		return
	}
	key, ok, err := backup.StoredRecoveryKey(r.Context(), s.store, s.secrets)
	if err != nil {
		s.internalError(w, "read recovery key", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "backups:GetRecoveryKey", "srn:syncloud:backup/recovery-key", map[string]any{"stored": ok})
	writeJSON(w, http.StatusOK, map[string]any{"stored": ok, "recoveryKey": key})
}

// handleCreateRecoveryKey gives an install whose recovery key was never
// kept (made before keys were kept) a new, kept key. The stored backups are
// re-keyed so the new key opens them all, and a fresh backup runs.
func (s *Server) handleCreateRecoveryKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(w, r) {
		return
	}
	ctx := r.Context()
	if _, ok, err := backup.StoredRecoveryKey(ctx, s.store, s.secrets); err != nil {
		s.internalError(w, "read recovery key", err)
		return
	} else if ok {
		writeError(w, http.StatusConflict, CodeConflict, "this install already keeps its recovery key; open it instead")
		return
	}
	key := secrets.NewRecoveryKey()
	if err := s.secrets.Rewrap(key); err != nil {
		s.internalError(w, "create recovery key", err)
		return
	}
	out := map[string]any{"stored": true, "recoveryKey": key}
	if err := backup.StoreRecoveryKey(ctx, s.store, s.secrets, key); err != nil {
		// The key is in use: still hand it over so it is not lost.
		out["stored"] = false
		out["storeError"] = err.Error()
	}
	if s.backups != nil {
		if res, err := s.backups.RekeyAll(ctx); err == nil {
			out["rekey"] = res
		} else if !errors.Is(err, backup.ErrNotConfigured) {
			out["rekeyError"] = err.Error()
		}
		if obj, err := s.backups.RunNow(ctx); err == nil {
			out["backup"] = obj
		} else if !errors.Is(err, backup.ErrNotConfigured) {
			out["backupError"] = err.Error()
		}
	}
	u, _ := currentUser(ctx)
	s.audit(r, u.ID, "backups:CreateRecoveryKey", "srn:syncloud:backup/recovery-key", nil)
	s.log.Warn("a new recovery key replaced one that was never kept", "by", u.ID)
	writeJSON(w, http.StatusCreated, out)
}
