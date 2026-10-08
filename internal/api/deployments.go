package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"syncloud/internal/jobs"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

// Deployment history and actions (Phase 15a).

// deploymentView is a deployment as the API shows it.
type deploymentView struct {
	store.Deployment
	Service     string            `json:"service,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Changes     []workload.Change `json:"changes"`
	ActorName   string            `json:"actorName,omitempty"`
	Commit      *deployCommit     `json:"commit,omitempty"`
	Hooks       []deployHook      `json:"hooks"`
}

type deployCommit struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
}

type deployHook struct {
	RunID   string `json:"runId"`
	Trigger string `json:"trigger"`
	Status  string `json:"status"`
}

// deploymentViews enriches deployments with names, commits and hook runs,
// looking each user and build up once.
type deploymentViews struct {
	s      *Server
	ctx    context.Context
	users  map[string]string
	builds map[string]*deployCommit
}

func (s *Server) deploymentViews(ctx context.Context) *deploymentViews {
	return &deploymentViews{s: s, ctx: ctx, users: map[string]string{}, builds: map[string]*deployCommit{}}
}

func (dv *deploymentViews) view(d store.Deployment) deploymentView {
	v := deploymentView{Deployment: d, Changes: workload.DecodeChanges(d.Changes), Hooks: []deployHook{}}
	if d.Actor != "" && d.Actor != "system" {
		name, ok := dv.users[d.Actor]
		if !ok {
			if u, err := dv.s.store.UserByID(dv.ctx, d.Actor); err == nil {
				name = u.Email
			}
			dv.users[d.Actor] = name
		}
		v.ActorName = name
	}
	if d.BuildID != "" {
		c, ok := dv.builds[d.BuildID]
		if !ok {
			if b, err := dv.s.store.BuildByID(dv.ctx, d.BuildID); err == nil {
				c = &deployCommit{SHA: b.SHA, Ref: b.Ref}
			}
			dv.builds[d.BuildID] = c
		}
		v.Commit = c
	}
	if runs, err := dv.s.store.DeploymentRuns(dv.ctx, d.ID); err == nil {
		for _, r := range runs {
			v.Hooks = append(v.Hooks, deployHook{RunID: r.ID, Trigger: r.Trigger, Status: r.Status})
		}
	}
	return v
}

func (s *Server) handleServiceDeployments(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	limit, before, ok := pageParams(w, r, 50, 500)
	if !ok {
		return
	}
	ds, err := s.store.ListDeployments(r.Context(), sv.ID, limit, before)
	if err != nil {
		s.internalError(w, "list deployments", err)
		return
	}
	dv := s.deploymentViews(r.Context())
	out := make([]deploymentView, 0, len(ds))
	for _, d := range ds {
		out = append(out, dv.view(d))
	}
	writePage(w, out, limit, func(d deploymentView) string { return d.ID })
}

func (s *Server) handleProjectDeployments(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	limit, before, ok := pageParams(w, r, 50, 500)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.DeploymentFilter{Service: q.Get("service"), Status: q.Get("status")}
	if env := q.Get("environment"); env != "" {
		e, err := s.store.EnvironmentByName(r.Context(), p.ID, env)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "no environment "+env+" in project "+p.Name)
			return
		} else if err != nil {
			s.internalError(w, "get environment", err)
			return
		}
		f.EnvironmentID = e.ID
	}
	ds, err := s.store.ListProjectDeployments(r.Context(), p.ID, f, limit, before)
	if err != nil {
		s.internalError(w, "list deployments", err)
		return
	}
	dv := s.deploymentViews(r.Context())
	out := make([]deploymentView, 0, len(ds))
	for _, d := range ds {
		v := dv.view(d.Deployment)
		v.Service, v.Environment = d.Service, d.Environment
		out = append(out, v)
	}
	writePage(w, out, limit, func(d deploymentView) string { return d.ID })
}

// deploymentDetail is one deployment with its timeline and hook runs.
type deploymentDetail struct {
	deploymentView
	Events []store.DeploymentEvent `json:"events"`
	Runs   []jobs.RunView          `json:"runs"`
}

// serviceDeployment resolves {id} within the service of the path.
func (s *Server) serviceDeployment(w http.ResponseWriter, r *http.Request) (store.Service, store.Deployment, bool) {
	sv, ok := s.service(w, r)
	if !ok {
		return sv, store.Deployment{}, false
	}
	d, err := s.store.DeploymentByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || err == nil && d.ServiceID != sv.ID {
		writeError(w, http.StatusNotFound, CodeNotFound, "no deployment "+r.PathValue("id"))
		return sv, d, false
	} else if err != nil {
		s.internalError(w, "get deployment", err)
		return sv, d, false
	}
	return sv, d, true
}

func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	_, d, ok := s.serviceDeployment(w, r)
	if !ok {
		return
	}
	detail, err := s.workloads.Detail(r.Context(), d)
	if err != nil {
		s.internalError(w, "deployment events", err)
		return
	}
	out := deploymentDetail{deploymentView: s.deploymentViews(r.Context()).view(d), Events: detail.Events, Runs: []jobs.RunView{}}
	if s.jobs != nil {
		if runs, err := s.store.DeploymentRuns(r.Context(), d.ID); err == nil {
			for _, run := range runs {
				out.Runs = append(out.Runs, s.jobs.View(r.Context(), run))
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCancelDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, d, ok := s.serviceDeployment(w, r)
	if !ok {
		return
	}
	u, _ := currentUser(r.Context())
	v, err := s.workloads.CancelDeployment(r.Context(), sv.ID, d.ID, u.ID)
	if err != nil {
		s.workloadError(w, "cancel deployment", err)
		return
	}
	s.audit(r, u.ID, "service:CancelDeployment", srnService(v), map[string]any{"deployment": d.ID, "fromRevision": d.FromRev, "toRevision": d.ToRev})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleRedeployService(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	req := struct {
		RunHooks *bool `json:"runHooks"`
	}{}
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	runHooks := req.RunHooks == nil || *req.RunHooks
	u, _ := currentUser(r.Context())
	v, err := s.workloads.Redeploy(r.Context(), sv.ID, u.ID, runHooks)
	if err != nil {
		s.workloadError(w, "redeploy service", err)
		return
	}
	s.audit(r, u.ID, "service:Redeploy", srnService(v), map[string]any{"newRevision": v.Revision, "runHooks": runHooks})
	writeJSON(w, http.StatusOK, v)
}

// handleUpdateProject changes a project's settings; fields left out keep
// their value.
func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		Description    *string `json:"description"`
		RollbackWindow *int    `json:"rollbackWindow"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Description != nil {
		if len(*req.Description) > 500 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "the description is at most 500 characters")
			return
		}
		p.Description = *req.Description
	}
	if req.RollbackWindow != nil {
		if *req.RollbackWindow < 1 || *req.RollbackWindow > store.MaxRollbackWindow {
			writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("rollbackWindow must be 1–%d revisions", store.MaxRollbackWindow))
			return
		}
		p.RollbackWindow = *req.RollbackWindow
	}
	if err := s.store.UpdateProjectSettings(r.Context(), p.ID, p.Description, p.RollbackWindow); err != nil {
		s.internalError(w, "update project", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:UpdateProject", projectSRN(p.Name), map[string]any{"rollbackWindow": p.RollbackWindow})
	writeJSON(w, http.StatusOK, p)
}
