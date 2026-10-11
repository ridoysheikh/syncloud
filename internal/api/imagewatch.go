package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// imageHookPrefix starts every deploy webhook token.
const imageHookPrefix = "SYN-DEPLOY-"

// autoDeployView is a service's auto-deploy of new image pushes.
type autoDeployView struct {
	// Watchable is false for services built from Git or pinned to a digest
	// (Reason says why); nothing else applies then.
	Watchable bool   `json:"watchable"`
	Reason    string `json:"reason,omitempty"`
	// Git: the service is built from Git (its builds deploy it).
	Git bool `json:"git"`
	// BuiltIn: the image is in the built-in registry, watched at once.
	BuiltIn      bool       `json:"builtIn"`
	AutoDeploy   bool       `json:"autoDeploy"`
	PollSeconds  int        `json:"pollSeconds"`
	Webhook      bool       `json:"webhook"`
	CheckedAt    *time.Time `json:"checkedAt"`
	LastDigest   string     `json:"lastDigest"`
	LastError    string     `json:"lastError"`
	LastDeployAt *time.Time `json:"lastDeployAt"`
}

func (s *Server) autoDeployView(r *http.Request, sv store.Service) (autoDeployView, error) {
	v := autoDeployView{Watchable: true, PollSeconds: workload.DefaultImagePoll}
	if err := s.workloads.Watchable(r.Context(), sv); err != nil {
		var nw workload.ErrNotWatchable
		if !errors.As(err, &nw) {
			return v, err
		}
		v.Watchable, v.Reason = false, nw.Reason
		_, gerr := s.store.GitSourceByService(r.Context(), sv.ID)
		v.Git = gerr == nil
	}
	if spec, err := s.workloads.SpecFor(r.Context(), sv.ID, sv.Revision); err == nil {
		v.BuiltIn = strings.HasPrefix(spec.Image, "@registry/")
	}
	w, err := s.store.ImageWatch(r.Context(), sv.ID)
	if errors.Is(err, store.ErrNotFound) {
		return v, nil
	} else if err != nil {
		return v, err
	}
	v.AutoDeploy, v.PollSeconds, v.Webhook = w.AutoDeploy, w.PollSeconds, w.WebhookHash != ""
	v.CheckedAt, v.LastDigest, v.LastError, v.LastDeployAt = w.CheckedAt, w.LastDigest, w.LastError, w.LastDeployAt
	return v, nil
}

func (s *Server) handleGetAutoDeploy(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	v, err := s.autoDeployView(r, sv)
	if err != nil {
		s.internalError(w, "auto-deploy", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleSetAutoDeploy turns watching the registry on or off.
func (s *Server) handleSetAutoDeploy(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var in struct {
		AutoDeploy  bool `json:"autoDeploy"`
		PollSeconds int  `json:"pollSeconds"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.PollSeconds == 0 {
		in.PollSeconds = workload.DefaultImagePoll
	}
	if in.PollSeconds < workload.MinImagePoll || in.PollSeconds > workload.MaxImagePoll {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "pollSeconds must be between 60 and 86400")
		return
	}
	if in.AutoDeploy {
		if err := s.workloads.Watchable(r.Context(), sv); err != nil {
			s.watchError(w, err)
			return
		}
	}
	if err := s.store.PutImageWatchSettings(r.Context(), sv.ID, in.AutoDeploy, in.PollSeconds, s.now()); err != nil {
		s.internalError(w, "auto-deploy", err)
		return
	}
	if in.AutoDeploy {
		s.workloads.KickImageWatch() // first check now
	}
	s.handleGetAutoDeploy(w, r)
}

// handleCreateDeployWebhook makes a new deploy webhook URL (replacing any).
// The token is shown once.
func (s *Server) handleCreateDeployWebhook(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := s.workloads.Watchable(r.Context(), sv); err != nil {
		s.watchError(w, err)
		return
	}
	tok := auth.NewToken(imageHookPrefix)
	if err := s.store.SetImageWebhook(r.Context(), sv.ID, auth.HashToken(tok), s.now()); err != nil {
		s.internalError(w, "deploy webhook", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": "/api/v1/hooks/image/" + tok})
}

func (s *Server) handleDeleteDeployWebhook(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := s.store.SetImageWebhook(r.Context(), sv.ID, "", s.now()); err != nil {
		s.internalError(w, "deploy webhook", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCheckImage checks the registry now and deploys a newer push.
func (s *Server) handleCheckImage(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	u, _ := currentUser(r.Context())
	s.runImageUpdate(w, r, sv.ID, u.ID)
}

// handleImageWebhook is the deploy webhook CI calls after pushing an image.
func (s *Server) handleImageWebhook(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !strings.HasPrefix(tok, imageHookPrefix) {
		writeError(w, http.StatusNotFound, CodeNotFound, "unknown hook")
		return
	}
	iw, err := s.store.ImageWatchByWebhook(r.Context(), auth.HashToken(tok))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "unknown hook")
		return
	} else if err != nil {
		s.internalError(w, "deploy webhook", err)
		return
	}
	s.runImageUpdate(w, r, iw.ServiceID, "deploy-webhook")
}

func (s *Server) runImageUpdate(w http.ResponseWriter, r *http.Request, serviceID, actor string) {
	up, err := s.workloads.DeployNewImage(r.Context(), serviceID, actor)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = s.store.RecordImageCheck(r.Context(), serviceID, up.Digest, msg, up.Deployed, s.now())
	if err != nil {
		s.watchError(w, err)
		return
	}
	status := http.StatusOK
	if up.Deployed {
		status = http.StatusAccepted
	}
	writeJSON(w, status, up)
}

func (s *Server) watchError(w http.ResponseWriter, err error) {
	var nw workload.ErrNotWatchable
	var byHand workload.ErrDeploysByHand
	switch {
	case errors.As(err, &nw):
		writeError(w, http.StatusConflict, CodeConflict, nw.Reason)
		return
	case errors.As(err, &byHand):
		writeError(w, http.StatusConflict, CodeConflict, byHand.Error())
		return
	}
	var inv workload.ErrInvalid
	var locked workload.ErrLocked
	switch {
	case errors.As(err, &inv), errors.As(err, &locked):
		s.workloadError(w, "image update", err)
	default:
		writeError(w, http.StatusBadGateway, CodeInternal, err.Error())
	}
}
