package api

import (
	"errors"
	"net/http"

	"github.com/ridoysheikh/syncloud/internal/regmaint"
	"github.com/ridoysheikh/syncloud/internal/regstorage"
	"github.com/ridoysheikh/syncloud/internal/s3"
)

func (s *Server) requireRegStorage(w http.ResponseWriter) bool {
	if s.regStorage == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "the registry is not enabled")
		return false
	}
	return true
}

// handleGetRegistryStorage shows where the registry keeps its images and
// the running or last storage switch (Phase 19).
func (s *Server) handleGetRegistryStorage(w http.ResponseWriter, r *http.Request) {
	if !s.requireRegStorage(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.regStorage.Status(r.Context()))
}

// handleSetRegistryStorage starts switching the registry to another
// storage: its images are copied first, and pushes pause meanwhile.
func (s *Server) handleSetRegistryStorage(w http.ResponseWriter, r *http.Request) {
	if !s.requireRegStorage(w) {
		return
	}
	var in regstorage.Target
	if !decodeJSON(w, r, &in) {
		return
	}
	u, _ := currentUser(r.Context())
	st, err := s.regStorage.Start(r.Context(), in, u.ID)
	var inv regstorage.ErrInvalid
	var s3inv s3.ErrInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
	case errors.As(err, &s3inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, s3inv.Error())
	case errors.Is(err, regmaint.ErrBusy), errors.Is(err, regmaint.ErrSwitching):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, regmaint.ErrNoRegistry):
		writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
	case err != nil:
		s.internalError(w, "switch registry storage", err)
	default:
		s.audit(r, u.ID, "registry:SetStorage", "srn:syncloud:registry", map[string]any{"driver": in.Driver, "endpoint": st.Switch.To.Endpoint, "bucket": st.Switch.To.Bucket, "prefix": st.Switch.To.Prefix})
		writeJSON(w, http.StatusAccepted, st)
	}
}
