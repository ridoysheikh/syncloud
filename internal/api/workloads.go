package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/jobs"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

// ── projects and environments ───────────────────────────────────────────────

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListProjects(r.Context())
	if err != nil {
		s.internalError(w, "list projects", err)
		return
	}
	type projectView struct {
		store.Project
		Environments []string `json:"environments"`
	}
	out := make([]projectView, 0, len(ps))
	for _, p := range ps {
		envs, err := s.store.ListEnvironments(r.Context(), p.ID)
		if err != nil {
			s.internalError(w, "list environments", err)
			return
		}
		v := projectView{Project: p, Environments: []string{}}
		for _, e := range envs {
			v.Environments = append(v.Environments, e.Name)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, out, itemProject)})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Environment string `json:"environment"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Environment == "" {
		req.Environment = "production"
	}
	if err := workload.ValidName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "project name "+err.Error())
		return
	}
	if err := workload.ValidName(req.Environment); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "environment name "+err.Error())
		return
	}
	if d := s.decide(r, "project:CreateProject", projectSRN(req.Name)); !d.Allowed {
		s.denied(w, r, "project:CreateProject", projectSRN(req.Name), d.Reason)
		return
	}
	now := s.now().UTC().Truncate(1e9)
	p := store.Project{ID: auth.NewID("prj_"), Name: req.Name, Description: req.Description, CreatedAt: now}
	env := store.Environment{ID: auth.NewID("env_"), ProjectID: p.ID, Name: req.Environment, CreatedAt: now}
	if err := s.store.CreateProject(r.Context(), p, env); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "project "+req.Name+" already exists")
		return
	} else if err != nil {
		s.internalError(w, "create project", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:Create", "srn:syncloud:project/"+p.Name, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"id": p.ID, "name": p.Name, "description": p.Description, "nodes": []string{}, "createdAt": p.CreatedAt, "environments": []string{env.Name}})
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	p, err := s.store.ProjectByName(r.Context(), r.PathValue("project"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no project "+r.PathValue("project"))
		return p, false
	} else if err != nil {
		s.internalError(w, "get project", err)
		return p, false
	}
	return p, true
}

func (s *Server) environment(w http.ResponseWriter, r *http.Request) (store.Environment, bool) {
	p, ok := s.project(w, r)
	if !ok {
		return store.Environment{}, false
	}
	e, err := s.store.EnvironmentByName(r.Context(), p.ID, r.PathValue("env"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no environment "+r.PathValue("env")+" in project "+p.Name)
		return e, false
	} else if err != nil {
		s.internalError(w, "get environment", err)
		return e, false
	}
	return e, true
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	if s.workloads != nil {
		s.deleteProject(w, r, p)
		return
	}
	if err := s.store.DeleteProject(r.Context(), p.ID); errors.Is(err, store.ErrNotEmpty) {
		writeError(w, http.StatusConflict, CodeConflict, "delete the project's services and databases first")
		return
	} else if err != nil {
		s.internalError(w, "delete project", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:Delete", "srn:syncloud:project/"+p.Name, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleSetProjectNodes limits the nodes a project's services and jobs run
// on (empty = any). Tasks on nodes no longer allowed are replaced elsewhere,
// new task first (§6.3).
func (s *Server) handleSetProjectNodes(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		Nodes []string `json:"nodes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	nodes := []string{}
	for _, n := range req.Nodes {
		n = strings.TrimSpace(n)
		if !workload.ValidNodeName(n) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("%q is not a node name", n))
			return
		}
		if !slices.Contains(nodes, n) {
			nodes = append(nodes, n)
		}
	}
	slices.Sort(nodes)
	if len(nodes) > 64 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "at most 64 nodes")
		return
	}
	// Services that name a node outside the new list would never place.
	svcs, err := s.store.ListServices(r.Context())
	if err != nil {
		s.internalError(w, "list services", err)
		return
	}
	for _, sv := range svcs {
		if sv.Project != p.Name || len(nodes) == 0 {
			continue
		}
		spec, err := s.workloads.SpecFor(r.Context(), sv.ID, sv.Revision)
		if err != nil {
			continue
		}
		for _, n := range append(slices.Clone(spec.Placement.Nodes), spec.Placement.Node) {
			if n != "" && !slices.Contains(nodes, n) {
				writeError(w, http.StatusConflict, CodeConflict, fmt.Sprintf("service %s/%s runs only on %s, which this list leaves out; change its placement first", sv.Environment, sv.Name, n))
				return
			}
		}
	}
	if err := s.store.SetProjectNodes(r.Context(), p.ID, nodes); err != nil {
		s.internalError(w, "set project nodes", err)
		return
	}
	for _, sv := range svcs {
		if sv.Project == p.Name {
			s.workloads.Enqueue(sv.ID)
		}
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:SetNodes", projectSRN(p.Name), map[string]any{"nodes": nodes})
	p.Nodes = nodes
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	envs, err := s.store.ListEnvironments(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, "list environments", err)
		return
	}
	if envs == nil {
		envs = []store.Environment{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": envs})
}

func (s *Server) handleCreateEnvironment(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
		// CloneFrom copies another environment (Phase 15d); StartServices
		// keeps the copies' task counts instead of 0.
		CloneFrom     string `json:"cloneFrom"`
		StartServices bool   `json:"startServices"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := workload.ValidName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "environment name "+err.Error())
		return
	}
	if req.CloneFrom != "" {
		s.cloneEnvironment(w, r, p, req.CloneFrom, req.Name, req.StartServices)
		return
	}
	e := store.Environment{ID: auth.NewID("env_"), ProjectID: p.ID, Name: req.Name, SharedEnv: map[string]string{}, CreatedAt: s.now().UTC().Truncate(1e9)}
	if err := s.store.CreateEnvironment(r.Context(), e); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "environment "+req.Name+" already exists")
		return
	} else if err != nil {
		s.internalError(w, "create environment", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:CreateEnvironment", "srn:syncloud:project/"+p.Name+"/"+e.Name, nil)
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	if !s.requireWorkloads(w) {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	pending, err := s.workloads.DeleteEnvironment(r.Context(), e, force)
	var inv workload.ErrInvalid
	if errors.As(err, &inv) {
		writeError(w, http.StatusConflict, CodeConflict, inv.Error())
		return
	} else if err != nil {
		s.internalError(w, "delete environment", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:DeleteEnvironment", "srn:syncloud:project/"+r.PathValue("project")+"/"+e.Name, map[string]any{"force": force})
	if pending {
		// Its services are being deleted; the environment goes after them.
		writeJSON(w, http.StatusAccepted, map[string]any{"deleting": true})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetSharedEnv(w http.ResponseWriter, r *http.Request) {
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	if e.SharedEnv == nil {
		e.SharedEnv = map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"variables": e.SharedEnv})
}

// handleSetSharedEnv replaces the shared variables and redeploys the
// services whose variables change.
func (s *Server) handleSetSharedEnv(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	var req struct {
		Variables map[string]string `json:"variables"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, _ := currentUser(r.Context())
	redeployed, err := s.workloads.SetSharedEnv(r.Context(), e, req.Variables, u.ID)
	if err != nil {
		s.workloadError(w, "set shared variables", err)
		return
	}
	names := make([]string, 0, len(req.Variables))
	for k := range req.Variables {
		names = append(names, k)
	}
	s.audit(r, u.ID, "project:SetSharedVariables", "srn:syncloud:project/"+r.PathValue("project")+"/"+e.Name, map[string]any{"names": names})
	if req.Variables == nil {
		req.Variables = map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"variables": req.Variables, "redeployed": redeployed})
}

// ── services ────────────────────────────────────────────────────────────────

func (s *Server) requireWorkloads(w http.ResponseWriter) bool {
	if s.workloads == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "workloads are not enabled")
		return false
	}
	return true
}

func (s *Server) service(w http.ResponseWriter, r *http.Request) (store.Service, bool) {
	e, ok := s.environment(w, r)
	if !ok {
		return store.Service{}, false
	}
	sv, err := s.store.ServiceByName(r.Context(), e.ID, r.PathValue("service"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no service "+r.PathValue("service"))
		return sv, false
	} else if err != nil {
		s.internalError(w, "get service", err)
		return sv, false
	}
	return sv, true
}

func (s *Server) workloadError(w http.ResponseWriter, what string, err error) {
	var inv workload.ErrInvalid
	var quota workload.ErrQuota
	var locked workload.ErrLocked
	switch {
	case errors.As(err, &quota):
		writeError(w, http.StatusForbidden, CodeQuotaExceeded, quota.Error())
	case errors.As(err, &locked):
		writeError(w, http.StatusLocked, CodeLocked, locked.Error())
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "not found")
	default:
		s.internalError(w, what, err)
	}
}

func (s *Server) handleListAllServices(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	items, err := s.workloads.ListServices(r.Context(), "")
	if err != nil {
		s.internalError(w, "list services", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, items, itemService)})
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	items, err := s.workloads.ListServices(r.Context(), e.ID)
	if err != nil {
		s.internalError(w, "list services", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type applyRequest struct {
	workload.Spec
	DesiredCount *int `json:"desiredCount"`
	// ReleaseCommands become the service's deploy hooks when it is created;
	// the first tasks start once the pre-deploy ones pass (Phase 15b).
	ReleaseCommands *releaseCommands `json:"releaseCommands,omitempty"`
}

// releaseCommands are shell commands run in the service's image.
type releaseCommands struct {
	PreDeploy  []hookCommand `json:"preDeploy"`
	PostDeploy []hookCommand `json:"postDeploy"`
}

type hookCommand struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds
	Retries int    `json:"retries,omitempty"`
}

// hookJobName names a service's nth hook job of a kind within the 32
// characters of a name.
func hookJobName(service, kind string, n int) string {
	suffix := fmt.Sprintf("-%s-%d", strings.TrimSuffix(kind, "-deploy"), n)
	if len(service)+len(suffix) > 32 {
		service = strings.TrimRight(service[:32-len(suffix)], "-")
	}
	return service + suffix
}

// handleApplyService creates or updates a service (kubectl apply semantics).
func (s *Server) handleApplyService(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	var req applyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	desired := -1
	if req.DesiredCount != nil {
		desired = *req.DesiredCount
	}
	u, _ := currentUser(r.Context())
	ctx := r.Context()
	var hooks []jobs.Spec
	if rc := req.ReleaseCommands; rc != nil && (len(rc.PreDeploy) > 0 || len(rc.PostDeploy) > 0) {
		if s.jobs == nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "release commands need the jobs subsystem")
			return
		}
		if _, err := s.store.ServiceByName(ctx, e.ID, r.PathValue("service")); err == nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "releaseCommands only apply when creating a service; change them on its Deploy tab or with the jobs API")
			return
		}
		for kind, list := range map[string][]hookCommand{jobs.KindPreDeploy: rc.PreDeploy, jobs.KindPostDeploy: rc.PostDeploy} {
			for i, c := range list {
				spec := jobs.Spec{Kind: kind, Service: r.PathValue("service"), Entrypoint: []string{"sh", "-c"}, Command: []string{strings.TrimSpace(c.Command)},
					Timeout: c.Timeout, Retries: c.Retries, Order: i + 1}
				if strings.TrimSpace(c.Command) == "" {
					writeError(w, http.StatusBadRequest, CodeBadRequest, kind+" command "+fmt.Sprint(i+1)+" is empty")
					return
				}
				if err := spec.Normalize(); err != nil {
					writeError(w, http.StatusBadRequest, CodeBadRequest, kind+" command "+fmt.Sprint(i+1)+": "+err.Error())
					return
				}
				hooks = append(hooks, spec)
			}
		}
		// A service waiting for its first build has nothing to run yet: the
		// build's deployment runs the pre-deploy jobs like any other.
		if len(rc.PreDeploy) > 0 && req.Image != workload.AwaitingBuild {
			ctx = workload.WithHeldFirstDeploy(ctx)
		}
	}
	v, created, err := s.workloads.Apply(ctx, e, r.PathValue("service"), req.Spec, desired, u.ID)
	if err != nil {
		s.workloadError(w, "apply service", err)
		return
	}
	if len(hooks) > 0 {
		var problem error
		for _, spec := range hooks {
			name := hookJobName(v.Name, spec.Kind, spec.Order)
			if _, err := s.jobs.Apply(r.Context(), e, name, spec); err != nil && problem == nil {
				problem = fmt.Errorf("job %s: %w", name, err)
			}
		}
		if sv, err := s.store.ServiceByID(r.Context(), v.ID); err == nil && sv.Held && v.Deployment != nil {
			// Whatever jobs exist now run; none at all releases the service.
			s.jobs.RunPreDeploy(r.Context(), sv, v.Revision, v.Deployment.ID)
		}
		if problem != nil {
			s.workloadError(w, "release commands", workload.ErrInvalid{Err: problem})
			return
		}
		if fresh, err := s.workloads.ServiceView(r.Context(), v.ID); err == nil {
			v = fresh
		}
	}
	action, status := "service:Update", http.StatusOK
	if created {
		action, status = "service:Create", http.StatusCreated
	}
	s.audit(r, u.ID, action, srnService(v), map[string]any{"revision": v.Revision, "image": v.Spec.Image})
	writeJSON(w, status, v)
}

func srnService(v workload.ServiceView) string {
	return "srn:syncloud:service/" + v.Project + "/" + v.Environment + "/" + v.Name
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	v, err := s.workloads.ServiceView(r.Context(), sv.ID)
	if err != nil {
		s.internalError(w, "get service", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if err := s.workloads.Delete(r.Context(), sv.ID); err != nil {
		s.workloadError(w, "delete service", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "service:Delete", "srn:syncloud:service/"+sv.Project+"/"+sv.Environment+"/"+sv.Name, nil)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleScaleService(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		DesiredCount int `json:"desiredCount"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, _ := currentUser(r.Context())
	v, err := s.workloads.Scale(r.Context(), sv.ID, req.DesiredCount, u.ID)
	if err != nil {
		s.workloadError(w, "scale service", err)
		return
	}
	s.audit(r, u.ID, "service:Scale", srnService(v), map[string]any{"from": sv.DesiredCount, "to": req.DesiredCount})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleRollbackService(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Revision int  `json:"revision"`
		RunHooks bool `json:"runHooks"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, _ := currentUser(r.Context())
	v, err := s.workloads.Rollback(r.Context(), sv.ID, req.Revision, u.ID, req.RunHooks)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such revision")
		return
	} else if err != nil {
		s.workloadError(w, "rollback service", err)
		return
	}
	s.audit(r, u.ID, "service:Rollback", srnService(v), map[string]any{"toRevision": req.Revision, "newRevision": v.Revision, "runHooks": req.RunHooks})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleServiceTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	items, err := s.workloads.ServiceTasks(r.Context(), sv.ID)
	if err != nil {
		s.internalError(w, "list tasks", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleServiceRevisions(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	tds, err := s.store.ListTaskDefinitions(r.Context(), sv.ID)
	if err != nil {
		s.internalError(w, "list revisions", err)
		return
	}
	type revView struct {
		Revision  int           `json:"revision"`
		Current   bool          `json:"current"`
		Spec      workload.Spec `json:"spec"`
		CreatedAt any           `json:"createdAt"`
		CreatedBy string        `json:"createdBy"`
		// ImageAvailable is false once registry cleanup removed the image,
		// so the revision can no longer be rolled back to (Phase 15a).
		ImageAvailable bool `json:"imageAvailable"`
	}
	out := make([]revView, 0, len(tds))
	images := map[string]bool{}
	for _, td := range tds {
		spec, _ := workload.ParseSpec(td.Spec)
		out = append(out, revView{Revision: td.Revision, Current: td.Revision == sv.Revision, Spec: spec, CreatedAt: td.CreatedAt, CreatedBy: td.CreatedBy, ImageAvailable: true})
		images[spec.Image] = true
	}
	if check := s.workloads.ImageAvailable; check != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for img := range images {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := check(ctx, img)
				mu.Lock()
				images[img] = ok || err != nil // unknown counts as available
				mu.Unlock()
			}()
		}
		wg.Wait()
		for i := range out {
			out[i].ImageAvailable = out[i].Current || images[out[i].Spec.Image]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ── tasks ───────────────────────────────────────────────────────────────────

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	items, err := s.workloads.ActiveTasks(r.Context())
	if err != nil {
		s.internalError(w, "list tasks", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, items, itemServiceField)})
}

func (s *Server) handleRestartTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.workloads.RestartTask(r.Context(), id); err != nil {
		s.workloadError(w, "restart task", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "task:Restart", "srn:syncloud:task/"+id, nil)
	w.WriteHeader(http.StatusAccepted)
}
