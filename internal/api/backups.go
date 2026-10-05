package api

import (
	"errors"
	"net/http"
	"time"

	"syncloud/internal/backup"
)

type backupSettings struct {
	Configured bool           `json:"configured"`
	Config     *backup.Config `json:"config"`
	Status     backup.Status  `json:"status"`
}

func (s *Server) backupSettings() backupSettings {
	out := backupSettings{Status: s.backups.Status()}
	if c, ok := s.backups.Config(); ok {
		out.Configured, out.Config = true, &c
	}
	return out
}

func (s *Server) requireBackups(w http.ResponseWriter) bool {
	if s.backups == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "backups are not available")
		return false
	}
	return true
}

func (s *Server) handleGetBackupConfig(w http.ResponseWriter, _ *http.Request) {
	if s.requireBackups(w) {
		writeJSON(w, http.StatusOK, s.backupSettings())
	}
}

func (s *Server) handleSetBackupConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackups(w) {
		return
	}
	var c backup.Config
	if !decodeJSON(w, r, &c) {
		return
	}
	if err := s.backups.SetConfig(r.Context(), c); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "backups:SetConfig", "srn:syncloud:settings/backups", map[string]any{"endpoint": c.Endpoint, "bucket": c.Bucket})
	writeJSON(w, http.StatusOK, s.backupSettings())
}

func (s *Server) handleDeleteBackupConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackups(w) {
		return
	}
	if err := s.backups.Disable(r.Context()); err != nil {
		s.internalError(w, "disable backups", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "backups:Disable", "srn:syncloud:settings/backups", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackups(w) {
		return
	}
	items, err := s.backups.List(r.Context())
	if errors.Is(err, backup.ErrNotConfigured) {
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusBadGateway, CodeInternal, "list backups: "+err.Error())
		return
	}
	if items == nil {
		items = []backup.Object{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackups(w) {
		return
	}
	obj, err := s.backups.RunNow(r.Context())
	if errors.Is(err, backup.ErrNotConfigured) {
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusBadGateway, CodeInternal, "backup failed: "+err.Error())
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "backups:Run", "srn:syncloud:backup/"+obj.Name, nil)
	writeJSON(w, http.StatusCreated, obj)
}

// handleDownloadBackup streams a fresh encrypted bundle. It is useless
// without the recovery key, but still audited.
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackups(w) {
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "backups:Download", "srn:syncloud:backup/download", nil)
	name := "syncloud-" + s.now().UTC().Format("20060102T150405Z") + backup.Ext
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if err := s.backups.WriteBundle(r.Context(), w); err != nil {
		s.log.Error("download backup", "err", err, "after", time.Since(s.now()))
	}
}
