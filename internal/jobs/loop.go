package jobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"syncloud/internal/store"
	"syncloud/internal/workload"
)

// JobView is a job as the API shows it.
type JobView struct {
	ID          string     `json:"id"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Name        string     `json:"name"`
	Spec        Spec       `json:"spec"`
	NextRunAt   *time.Time `json:"nextRunAt"`
	LastRun     *RunView   `json:"lastRun"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func (m *Manager) JobView(ctx context.Context, j store.Job) JobView {
	spec, _ := parseSpec(j.Spec)
	v := JobView{ID: j.ID, Project: j.Project, Environment: j.Environment, Name: j.Name, Spec: spec, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt}
	if spec.Kind == KindScheduled {
		if s, err := ParseSchedule(spec.Schedule, spec.Timezone); err == nil {
			from := m.now()
			if j.LastScheduledAt != nil && j.LastScheduledAt.After(from) {
				from = *j.LastScheduledAt
			}
			next := s.Next(from).UTC()
			v.NextRunAt = &next
		}
	}
	if runs, err := m.st.JobRuns(ctx, j.ID, 1, ""); err == nil && len(runs) == 1 {
		rv := m.View(ctx, runs[0])
		v.LastRun = &rv
	}
	return v
}

// Run starts due scheduled jobs and watches active runs until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.schedule(ctx)
		m.watch(ctx)
	}
}

// schedule starts runs for cron slots that are due. A slot missed by more
// than startingDeadline (e.g. the controller was down) is recorded as skipped.
func (m *Manager) schedule(ctx context.Context) {
	jobs, err := m.st.ListJobs(ctx)
	if err != nil {
		return
	}
	now := m.now()
	for _, j := range jobs {
		spec, err := parseSpec(j.Spec)
		if err != nil || spec.Kind != KindScheduled {
			continue
		}
		sched, err := ParseSchedule(spec.Schedule, spec.Timezone)
		if err != nil {
			continue
		}
		last := j.CreatedAt
		if j.LastScheduledAt != nil {
			last = *j.LastScheduledAt
		}
		due := sched.Next(last)
		if due.IsZero() || due.After(now) {
			continue
		}
		// Only the latest due slot matters; older missed ones are skipped.
		for next := sched.Next(due); !next.After(now) && !next.IsZero(); next = sched.Next(next) {
			due = next
		}
		if err := m.st.SetJobScheduled(ctx, j.ID, due); err != nil {
			continue
		}
		if now.Sub(due) > time.Duration(spec.StartingDeadline)*time.Second {
			m.skip(ctx, j, fmt.Sprintf("missed the %s run (starting deadline passed)", due.UTC().Format(time.RFC3339)))
			continue
		}
		active := m.activeRuns(ctx, j.ID)
		switch {
		case len(active) > 0 && spec.ConcurrencyPolicy == "forbid":
			m.skip(ctx, j, "previous run still active (concurrencyPolicy: forbid)")
			continue
		case len(active) > 0 && spec.ConcurrencyPolicy == "replace":
			for _, r := range active {
				m.finish(ctx, r, store.RunCancelled, nil, "replaced by the next scheduled run")
				m.stopContainer(r, 0)
			}
		}
		t, err := m.target(ctx, store.Environment{ID: j.EnvironmentID}, j.Project, j.Environment, &j, spec, 0)
		if err != nil {
			m.skip(ctx, j, err.Error())
			continue
		}
		if _, err := m.start(ctx, t, "schedule", 1, ""); err != nil {
			m.log.Error("start scheduled run", "job", j.Name, "err", err)
		}
	}
}

func (m *Manager) activeRuns(ctx context.Context, jobID string) []store.JobRun {
	runs, _ := m.st.ActiveRuns(ctx)
	var out []store.JobRun
	for _, r := range runs {
		if r.JobID == jobID {
			out = append(out, r)
		}
	}
	return out
}

func (m *Manager) skip(ctx context.Context, j store.Job, msg string) {
	now := m.now().UTC().Truncate(time.Second)
	run := store.JobRun{ID: "run_" + fmt.Sprint(now.UnixNano()), JobID: j.ID, EnvironmentID: j.EnvironmentID, Trigger: "schedule", Attempt: 1,
		Spec: "{}", Status: store.RunSkipped, Message: msg, CreatedAt: now, FinishedAt: &now}
	if err := m.st.CreateRun(ctx, run); err == nil {
		m.publish(ctx, run)
	}
	m.log.Warn("scheduled run skipped", "job", j.Name, "reason", msg)
}

// watch enforces timeouts, places runs that waited for a node, and re-sends
// runs that never reported.
func (m *Manager) watch(ctx context.Context) {
	runs, err := m.st.ActiveRuns(ctx)
	if err != nil {
		return
	}
	now := m.now()
	for _, r := range runs {
		timeout := 3600
		if r.JobID != "" {
			if j, err := m.st.JobByID(ctx, r.JobID); err == nil {
				if s, err := parseSpec(j.Spec); err == nil {
					timeout = s.Timeout
				}
			}
		}
		switch {
		case r.Status == store.RunRunning && r.StartedAt != nil && now.Sub(*r.StartedAt) > time.Duration(timeout)*time.Second:
			m.finish(ctx, r, store.RunTimedOut, nil, fmt.Sprintf("timed out after %ds", timeout))
			m.stopContainer(r, 0)
		case r.Status == store.RunPending && r.NodeID == "":
			spec, err := parseRunSpec(r)
			if err != nil {
				continue
			}
			if nodeID, why := m.place(ctx, r.Trigger, r.EnvironmentID, spec); nodeID != "" {
				r.NodeID, r.Message = nodeID, ""
				_ = m.st.UpdateRun(ctx, r)
				m.send(ctx, r)
				m.publish(ctx, r)
			} else if r.Message != "waiting for a node: "+why {
				r.Message = "waiting for a node: " + why
				_ = m.st.UpdateRun(ctx, r)
			}
		case r.Status == store.RunPending && now.Sub(r.CreatedAt) > 30*time.Second && int(now.Sub(r.CreatedAt).Seconds())%30 < 5:
			if n, ok := m.nodes.Get(r.NodeID); ok && n.Connected {
				m.send(ctx, r)
			}
		}
	}
}

func parseRunSpec(r store.JobRun) (workload.Spec, error) { return workload.ParseSpec(r.Spec) }

// ── deploy hooks (workload.DeployHooks) ─────────────────────────────────────

func (m *Manager) hookJobs(ctx context.Context, sv store.Service, kind string) []store.Job {
	jobs, err := m.st.ListJobsIn(ctx, sv.EnvironmentID)
	if err != nil {
		return nil
	}
	var out []store.Job
	for _, j := range jobs {
		if s, err := parseSpec(j.Spec); err == nil && s.Kind == kind && s.Service == sv.Name {
			out = append(out, j)
		}
	}
	return out
}

func (m *Manager) HasPreDeploy(ctx context.Context, sv store.Service) bool {
	return len(m.hookJobs(ctx, sv, KindPreDeploy)) > 0
}

// RunPreDeploy runs the service's pre-deploy jobs, one at a time in their
// order, with the revision being deployed; the outcome comes back through
// workload.Manager.PreDeployDone.
func (m *Manager) RunPreDeploy(ctx context.Context, _ store.Service, _ int, depID string) {
	m.advanceHooks(ctx, depID)
}

// RunPostDeploy runs post-deploy jobs one at a time after a deployment
// succeeded; a failure stops the rest but does not affect the deployment.
func (m *Manager) RunPostDeploy(ctx context.Context, _ store.Service, dep store.Deployment) {
	m.advanceHooks(ctx, dep.ID)
}

// CancelHooks stops the hook runs of a cancelled deployment.
func (m *Manager) CancelHooks(ctx context.Context, depID string) {
	runs, err := m.st.DeploymentRuns(ctx, depID)
	if err != nil {
		m.log.Warn("cancel hooks", "deployment", depID, "err", err)
		return
	}
	for _, r := range runs {
		if r.Status == store.RunPending || r.Status == store.RunRunning {
			_ = m.Cancel(ctx, r.ID)
		}
	}
}

// orderedHookJobs is the service's hook jobs of a kind in the order they
// run: by their order field, then oldest first.
func (m *Manager) orderedHookJobs(ctx context.Context, sv store.Service, kind string) []store.Job {
	jobs := m.hookJobs(ctx, sv, kind)
	order := func(j store.Job) int {
		s, _ := parseSpec(j.Spec)
		return s.Order
	}
	sort.SliceStable(jobs, func(a, b int) bool {
		oa, ob := order(jobs[a]), order(jobs[b])
		if oa != ob {
			return oa < ob
		}
		if !jobs[a].CreatedAt.Equal(jobs[b].CreatedAt) {
			return jobs[a].CreatedAt.Before(jobs[b].CreatedAt)
		}
		return jobs[a].Name < jobs[b].Name
	})
	return jobs
}

// advanceHooks moves a deployment's hooks along: it starts the first job
// without a run, waits on a running one, and finishes the pre-deploy phase
// when all succeeded or one failed. It reads everything from the store, so
// it picks up where it left off after a controller restart.
func (m *Manager) advanceHooks(ctx context.Context, depID string) {
	dep, err := m.st.DeploymentByID(ctx, depID)
	if err != nil {
		return
	}
	var kind string
	switch dep.Status {
	case store.DeployWaitingHook:
		kind = KindPreDeploy
	case store.DeploySucceeded:
		kind = KindPostDeploy
	default:
		return // cancelled, superseded or failed meanwhile
	}
	sv, err := m.st.ServiceByID(ctx, dep.ServiceID)
	if err != nil {
		return
	}
	pre := kind == KindPreDeploy
	done := func(ok bool, msg string) {
		if pre {
			m.wl.PreDeployDone(ctx, depID, ok, msg)
		}
	}
	runs, _ := m.st.DeploymentRuns(ctx, depID)
	latest := map[string]store.JobRun{} // job ID -> its newest run (runs are oldest first)
	for _, r := range runs {
		if r.Trigger == kind && r.JobID != "" {
			latest[r.JobID] = r
		}
	}
	for _, j := range m.orderedHookJobs(ctx, sv, kind) {
		r, ran := latest[j.ID]
		switch {
		case !ran:
			spec, _ := parseSpec(j.Spec)
			t, err := m.target(ctx, store.Environment{ID: sv.EnvironmentID}, sv.Project, sv.Environment, &j, spec, dep.ToRev)
			if err == nil {
				_, err = m.start(ctx, t, kind, 1, depID)
			}
			if err != nil {
				m.wl.DeploymentEvent(ctx, depID, "hook-failed", fmt.Sprintf("%s job %s could not start: %v", kind, j.Name, err))
				done(false, fmt.Sprintf("job %s: %v", j.Name, err))
				return
			}
			m.wl.DeploymentEvent(ctx, depID, "hook-started", fmt.Sprintf("%s job %s started", kind, j.Name))
			return
		case r.Status == store.RunPending || r.Status == store.RunRunning:
			return
		case r.Status == store.RunSucceeded:
			continue
		default:
			msg := fmt.Sprintf("job %s %s", j.Name, strings.ReplaceAll(r.Status, "_", " "))
			if r.Message != "" {
				msg += ": " + r.Message
			}
			done(false, msg)
			return
		}
	}
	done(true, "")
}
