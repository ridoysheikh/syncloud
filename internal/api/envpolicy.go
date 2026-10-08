package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/ridoysheikh/syncloud/internal/jobs"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// Project settings (Phase 15d): cloning environments, deploy policy and
// deleting projects with everything in them.

// cloneEnvironment creates environment name as a copy of from: shared
// variables, services (stopped unless start), their jobs and security
// group attachments.
func (s *Server) cloneEnvironment(w http.ResponseWriter, r *http.Request, p store.Project, from, name string, start bool) {
	if !s.requireWorkloads(w) {
		return
	}
	src, err := s.store.EnvironmentByName(r.Context(), p.ID, from)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no environment "+from+" in project "+p.Name)
		return
	} else if err != nil {
		s.internalError(w, "get environment", err)
		return
	}
	u, _ := currentUser(r.Context())
	res, err := s.workloads.CloneEnvironment(r.Context(), src, name, start, u.ID)
	if err != nil {
		s.workloadError(w, "clone environment", err)
		return
	}
	// Jobs, by name, running the copied services.
	jobsCopied := []string{}
	if s.jobs != nil {
		if js, err := s.store.ListJobsIn(r.Context(), src.ID); err == nil {
			for _, j := range js {
				var spec jobs.Spec
				if json.Unmarshal([]byte(j.Spec), &spec) != nil {
					continue
				}
				if _, err := s.jobs.Apply(r.Context(), res.Environment, j.Name, spec); err == nil {
					jobsCopied = append(jobsCopied, j.Name)
				}
			}
		}
	}
	// The copies join their originals' security groups.
	if gs, err := s.store.ListSecurityGroups(r.Context()); err == nil {
		changed := false
		for _, g := range gs {
			if g.ProjectID != p.ID {
				continue
			}
			add := []string{}
			for _, id := range g.ServiceIDs {
				if c, ok := res.ServiceIDs[id]; ok && !slices.Contains(g.ServiceIDs, c) {
					add = append(add, c)
				}
			}
			if len(add) > 0 {
				g.ServiceIDs = append(g.ServiceIDs, add...)
				if err := s.store.PutSecurityGroup(r.Context(), g); err == nil {
					changed = true
				}
			}
		}
		if changed {
			s.securityChanged()
		}
	}
	s.audit(r, u.ID, "project:CreateEnvironment", "srn:syncloud:project/"+p.Name+"/"+name,
		map[string]any{"cloneFrom": from, "services": res.Services, "jobs": jobsCopied, "startServices": start})
	writeJSON(w, http.StatusCreated, map[string]any{"environment": res.Environment, "services": res.Services, "jobs": jobsCopied})
}

// handleSetEnvironmentPolicy sets an environment's auto-deploy switch and
// deploy lock; fields left out keep their value.
func (s *Server) handleSetEnvironmentPolicy(w http.ResponseWriter, r *http.Request) {
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	var req struct {
		AutoDeploy *bool   `json:"autoDeploy"`
		Locked     *bool   `json:"locked"`
		Reason     *string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, _ := currentUser(r.Context())
	auto, lock := e.AutoDeploy, e.Lock
	if req.AutoDeploy != nil {
		auto = *req.AutoDeploy
	}
	if req.Locked != nil {
		switch {
		case !*req.Locked:
			lock = nil
		case req.Reason == nil || len(*req.Reason) < 3 || len(*req.Reason) > 300:
			writeError(w, http.StatusBadRequest, CodeBadRequest, "say why deploys are locked (3–300 characters)")
			return
		default:
			by := u.Email
			if by == "" {
				by = u.ID
			}
			lock = &store.DeployLock{Reason: *req.Reason, By: by, At: s.now().UTC().Truncate(1e9)}
		}
	} else if req.Reason != nil && lock != nil {
		lock.Reason = *req.Reason
	}
	if err := s.store.SetEnvironmentPolicy(r.Context(), e.ID, auto, lock); err != nil {
		s.internalError(w, "set environment policy", err)
		return
	}
	s.audit(r, u.ID, "project:SetEnvironmentPolicy", "srn:syncloud:project/"+r.PathValue("project")+"/"+e.Name,
		map[string]any{"autoDeploy": auto, "locked": lock != nil})
	out, err := s.store.EnvironmentByID(r.Context(), e.ID)
	if err != nil {
		s.internalError(w, "get environment", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteProject deletes a project; with force its services go first and
// the project after them.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request, p store.Project) {
	force := r.URL.Query().Get("force") == "true"
	pending, err := s.workloads.DeleteProject(r.Context(), p, force)
	var inv workload.ErrInvalid
	if errors.As(err, &inv) {
		writeError(w, http.StatusConflict, CodeConflict, inv.Error())
		return
	} else if err != nil {
		s.internalError(w, "delete project", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:Delete", "srn:syncloud:project/"+p.Name, map[string]any{"force": force})
	if pending {
		writeJSON(w, http.StatusAccepted, map[string]any{"deleting": true})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
