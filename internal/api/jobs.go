package api

import (
	"errors"
	"net/http"

	"github.com/ridoysheikh/syncloud/internal/jobs"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func (s *Server) jobError(w http.ResponseWriter, what string, err error) {
	var inv jobs.ErrInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "not found")
	default:
		s.workloadError(w, what, err)
	}
}

func (s *Server) requireJobs(w http.ResponseWriter) bool {
	if s.jobs == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "jobs are not enabled")
		return false
	}
	return true
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) (store.Job, bool) {
	e, ok := s.environment(w, r)
	if !ok {
		return store.Job{}, false
	}
	j, err := s.store.JobByName(r.Context(), e.ID, r.PathValue("job"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no job "+r.PathValue("job"))
		return j, false
	} else if err != nil {
		s.internalError(w, "get job", err)
		return j, false
	}
	return j, true
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	js, err := s.store.ListJobsIn(r.Context(), e.ID)
	if err != nil {
		s.internalError(w, "list jobs", err)
		return
	}
	out := make([]jobs.JobView, 0, len(js))
	for _, j := range js {
		out = append(out, s.jobs.JobView(r.Context(), j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, out, itemJob)})
}

func (s *Server) handleListAllJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	js, err := s.store.ListJobs(r.Context())
	if err != nil {
		s.internalError(w, "list jobs", err)
		return
	}
	out := make([]jobs.JobView, 0, len(js))
	for _, j := range js {
		out = append(out, s.jobs.JobView(r.Context(), j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleApplyJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	var spec jobs.Spec
	if !decodeJSON(w, r, &spec) {
		return
	}
	j, err := s.jobs.Apply(r.Context(), e, r.PathValue("job"), spec)
	if err != nil {
		s.jobError(w, "apply job", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "job:Create", "srn:syncloud:job/"+j.Project+"/"+j.Environment+"/"+j.Name, nil)
	writeJSON(w, http.StatusOK, s.jobs.JobView(r.Context(), j))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	j, ok := s.job(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.jobs.JobView(r.Context(), j))
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.job(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteJob(r.Context(), j.ID); err != nil {
		s.internalError(w, "delete job", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "job:Delete", "srn:syncloud:job/"+j.Project+"/"+j.Environment+"/"+j.Name, nil)
	w.WriteHeader(http.StatusNoContent)
}

type runRequest struct {
	Command []string `json:"command"`
}

func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	j, ok := s.job(w, r)
	if !ok {
		return
	}
	var req runRequest
	if r.ContentLength > 0 && !decodeJSON(w, r, &req) {
		return
	}
	v, err := s.jobs.RunJob(r.Context(), j, req.Command)
	if err != nil {
		s.jobError(w, "run job", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "job:Run", "srn:syncloud:job/"+j.Project+"/"+j.Environment+"/"+j.Name, map[string]any{"run": v.ID})
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleJobRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	j, ok := s.job(w, r)
	if !ok {
		return
	}
	limit, before, ok := pageParams(w, r, 50, 500)
	if !ok {
		return
	}
	runs, err := s.store.JobRuns(r.Context(), j.ID, limit, before)
	if err != nil {
		s.internalError(w, "list runs", err)
		return
	}
	out := make([]jobs.RunView, 0, len(runs))
	for _, x := range runs {
		out = append(out, s.jobs.View(r.Context(), x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next": nextCursor(runs, limit, func(x store.JobRun) string { return x.ID })})
}

// handleRunService starts an ad-hoc run of a service's task definition.
func (s *Server) handleRunService(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req runRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	v, err := s.jobs.RunService(r.Context(), sv, req.Command)
	if err != nil {
		s.jobError(w, "run", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "job:Run", "srn:syncloud:service/"+sv.Project+"/"+sv.Environment+"/"+sv.Name, map[string]any{"run": v.ID, "command": req.Command})
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	x, err := s.store.RunByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such run")
		return
	} else if err != nil {
		s.internalError(w, "get run", err)
		return
	}
	writeJSON(w, http.StatusOK, s.jobs.View(r.Context(), x))
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.jobs.Cancel(r.Context(), id); err != nil {
		s.jobError(w, "cancel run", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "job:Cancel", "srn:syncloud:run/"+id, nil)
	w.WriteHeader(http.StatusAccepted)
}
