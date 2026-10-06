// Package jobs runs tasks to completion (§5.11): one-off runs, scheduled
// (cron) jobs and pre-/post-deploy hooks. Runs reuse the scheduler, agents and
// logs of services; a run is a task whose ID starts with "run_".
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"syncloud/internal/agentgw"
	"syncloud/internal/auth"
	"syncloud/internal/events"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/nodes"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

const (
	// RunPrefix marks job-run tasks on agents.
	RunPrefix = "run_"
	// TopicRun carries a RunView whenever a run changes.
	TopicRun = "job.run"

	KindOneOff     = "oneoff"
	KindScheduled  = "scheduled"
	KindPreDeploy  = "pre-deploy"
	KindPostDeploy = "post-deploy"
)

// Spec is a job definition.
type Spec struct {
	Kind string `json:"kind"`
	// Service runs the service's task definition (its current revision, or
	// the revision being deployed for hooks) with Command.
	Service string `json:"service,omitempty"`
	// Task is a standalone task definition when there is no Service.
	Task              *workload.Spec    `json:"task,omitempty"`
	Command           []string          `json:"command,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Schedule          string            `json:"schedule,omitempty"`
	Timezone          string            `json:"timezone,omitempty"`
	ConcurrencyPolicy string            `json:"concurrencyPolicy,omitempty"` // allow | forbid (default) | replace
	Timeout           int               `json:"timeout,omitempty"`           // seconds (default 3600)
	Retries           int               `json:"retries,omitempty"`
	StartingDeadline  int               `json:"startingDeadline,omitempty"` // seconds (default 600)
	HistoryLimit      int               `json:"historyLimit,omitempty"`     // runs kept (default 20)
}

// Normalize validates and fills defaults.
func (s *Spec) Normalize() error {
	switch s.Kind {
	case "":
		s.Kind = KindOneOff
		if s.Schedule != "" {
			s.Kind = KindScheduled
		}
	case KindOneOff, KindScheduled, KindPreDeploy, KindPostDeploy:
	default:
		return errors.New("kind must be oneoff, scheduled, pre-deploy or post-deploy")
	}
	if s.Kind == KindScheduled {
		if _, err := ParseSchedule(s.Schedule, s.Timezone); err != nil {
			return err
		}
	} else if s.Schedule != "" {
		return errors.New("only scheduled jobs have a schedule")
	}
	if (s.Kind == KindPreDeploy || s.Kind == KindPostDeploy) && s.Service == "" {
		return errors.New("deploy hooks need a service")
	}
	if (s.Service == "") == (s.Task == nil) {
		return errors.New("give either service (to run its task definition) or task")
	}
	if s.Task != nil {
		if err := s.Task.Normalize(); err != nil {
			return fmt.Errorf("task: %w", err)
		}
	}
	switch s.ConcurrencyPolicy {
	case "":
		s.ConcurrencyPolicy = "forbid"
	case "allow", "forbid", "replace":
	default:
		return errors.New("concurrencyPolicy must be allow, forbid or replace")
	}
	if s.Timeout == 0 {
		s.Timeout = 3600
	}
	if s.StartingDeadline == 0 {
		s.StartingDeadline = 600
	}
	if s.HistoryLimit == 0 {
		s.HistoryLimit = 20
	}
	if s.Timeout < 1 || s.Timeout > 7*24*3600 || s.Retries < 0 || s.Retries > 10 || s.HistoryLimit < 1 || s.HistoryLimit > 500 {
		return errors.New("timeout must be 1s–7d, retries 0–10, historyLimit 1–500")
	}
	return nil
}

// RunView is a run as the API shows it.
type RunView struct {
	ID          string     `json:"id"`
	Job         string     `json:"job"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Service     string     `json:"service,omitempty"`
	Revision    int        `json:"revision,omitempty"`
	Trigger     string     `json:"trigger"`
	Attempt     int        `json:"attempt"`
	Status      string     `json:"status"`
	Node        string     `json:"node"`
	ExitCode    *int       `json:"exitCode"`
	Message     string     `json:"message"`
	Command     []string   `json:"command"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

type Manager struct {
	st    *store.Store
	gw    *agentgw.Gateway
	wl    *workload.Manager
	nodes *nodes.Registry
	bus   *events.Bus
	log   *slog.Logger
	now   func() time.Time

	mu    sync.Mutex
	hooks map[string]map[string]bool // deployment ID -> pending hook run IDs

	// AdmitCount checks a quota before a new job is stored (§7.2).
	AdmitCount func(ctx context.Context, envID, what string) error
	// OnRunAddress is called when a run's container address becomes known.
	OnRunAddress func()
	// OnFinished runs after any run reaches its final status (builds use it).
	OnFinished func(ctx context.Context, run store.JobRun)
}

// TriggerBuild marks BuildKit runs (§5.8): they run privileged on the host
// network, which user jobs never can.
const TriggerBuild = "build"

// StartBuild runs a platform build task for a service.
func (m *Manager) StartBuild(ctx context.Context, sv store.Service, spec workload.Spec) (store.JobRun, error) {
	t := runTarget{envID: sv.EnvironmentID, project: sv.Project, environment: sv.Environment, service: &sv, revision: 0, spec: spec}
	return m.start(ctx, t, TriggerBuild, 1, "")
}

func NewManager(st *store.Store, gw *agentgw.Gateway, wl *workload.Manager, reg *nodes.Registry, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{st: st, gw: gw, wl: wl, nodes: reg, bus: bus, log: log, now: time.Now, hooks: map[string]map[string]bool{}}
}

func (m *Manager) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnTaskStatus: m.onTaskStatus, OnConnect: m.onConnect}
}

// ── starting runs ───────────────────────────────────────────────────────────

// runTarget is everything needed to start a run.
type runTarget struct {
	envID       string
	project     string
	environment string
	jobID       string
	jobName     string
	service     *store.Service
	revision    int
	spec        workload.Spec // the task definition (command applied)
}

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

// target resolves what a job runs. revision overrides the service's current
// revision (pre-deploy hooks run the revision being deployed).
func (m *Manager) target(ctx context.Context, env store.Environment, project, envName string, job *store.Job, spec Spec, revision int) (runTarget, error) {
	t := runTarget{envID: env.ID, project: project, environment: envName}
	if job != nil {
		t.jobID, t.jobName = job.ID, job.Name
	}
	if spec.Service != "" {
		sv, err := m.st.ServiceByName(ctx, env.ID, spec.Service)
		if err != nil {
			return t, ErrInvalid{fmt.Errorf("service %s: %w", spec.Service, err)}
		}
		if revision == 0 {
			revision = sv.Revision
		}
		td, err := m.wl.SpecFor(ctx, sv.ID, revision)
		if err != nil {
			return t, err
		}
		t.service, t.revision, t.spec = &sv, revision, td
	} else {
		t.spec = *spec.Task
	}
	if len(spec.Command) > 0 {
		t.spec.Command = spec.Command
	}
	if len(spec.Env) > 0 {
		env := map[string]string{}
		for k, v := range t.spec.Env {
			env[k] = v
		}
		for k, v := range spec.Env {
			env[k] = v
		}
		t.spec.Env = env
	}
	t.spec.Health = nil // runs exit; health checks do not apply
	t.spec.Ports = nil  // and are not routed
	return t, nil
}

// start creates a run and sends it to a node.
func (m *Manager) start(ctx context.Context, t runTarget, trigger string, attempt int, depID string) (store.JobRun, error) {
	now := m.now().UTC().Truncate(time.Second)
	run := store.JobRun{
		ID: auth.NewID(RunPrefix), JobID: t.jobID, EnvironmentID: t.envID, Revision: t.revision, Trigger: trigger, Attempt: attempt,
		Spec: t.spec.Canonical(), Status: store.RunPending, DeploymentID: depID, CreatedAt: now,
	}
	if t.service != nil {
		run.ServiceID = t.service.ID
	}
	nodeID, why := m.wl.PlaceSpec(ctx, t.spec)
	if nodeID == "" {
		run.Message = "waiting for a node: " + why
	}
	run.NodeID = nodeID
	if err := m.st.CreateRun(ctx, run); err != nil {
		return run, err
	}
	if nodeID != "" {
		m.send(ctx, run)
	}
	m.publish(ctx, run)
	return run, nil
}

// svcFor names the run's container and labels it.
func (m *Manager) svcFor(ctx context.Context, run store.JobRun) store.Service {
	project, envName := m.envNames(ctx, run.EnvironmentID)
	name := "run"
	if run.JobID != "" {
		if j, err := m.st.JobByID(ctx, run.JobID); err == nil {
			name = "job-" + j.Name
		}
	} else if run.ServiceID != "" {
		if sv, err := m.st.ServiceByID(ctx, run.ServiceID); err == nil {
			name = "run-" + sv.Name
		}
	}
	return store.Service{ID: run.ServiceID, Project: project, Environment: envName, Name: name}
}

func (m *Manager) envNames(ctx context.Context, envID string) (string, string) {
	var p, e string
	_ = m.st.R.QueryRowContext(ctx, `SELECT p.name, e.name FROM environments e JOIN projects p ON p.id = e.project_id WHERE e.id = ?`, envID).Scan(&p, &e)
	return p, e
}

func (m *Manager) send(ctx context.Context, run store.JobRun) {
	spec, err := workload.ParseSpec(run.Spec)
	if err != nil {
		return
	}
	sv := m.svcFor(ctx, run)
	ts := m.wl.RunSpec(sv, spec, store.Task{ID: run.ID, NodeID: run.NodeID, Revision: run.Revision})
	ts.Labels["syncloud.job_run"] = run.ID
	if run.Trigger == TriggerBuild {
		ts.Privileged, ts.NetworkMode = true, "host"
		ts.DnsServers, ts.DnsSearch = nil, nil
		ts.Name = strings.Replace(ts.Name, "-run-", "-build-", 1)
	}
	err = m.gw.Send(run.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: ts}}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("send job run", "run", run.ID, "err", err)
	}
}

func (m *Manager) stopContainer(run store.JobRun, after time.Duration) {
	if run.NodeID == "" {
		return
	}
	time.AfterFunc(after, func() { // let the log shipper read the last lines first
		_ = m.gw.Send(run.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
			TaskId: run.ID, TimeoutSeconds: 10, Remove: true,
		}}})
	})
}

// ── public operations ───────────────────────────────────────────────────────

// Apply creates or updates a job.
func (m *Manager) Apply(ctx context.Context, env store.Environment, name string, spec Spec) (store.Job, error) {
	if err := workload.ValidName(name); err != nil {
		return store.Job{}, ErrInvalid{fmt.Errorf("job name %w", err)}
	}
	if err := spec.Normalize(); err != nil {
		return store.Job{}, ErrInvalid{err}
	}
	if spec.Service != "" {
		if _, err := m.st.ServiceByName(ctx, env.ID, spec.Service); err != nil {
			return store.Job{}, ErrInvalid{fmt.Errorf("no service %s in this environment", spec.Service)}
		}
	}
	if _, err := m.st.JobByName(ctx, env.ID, name); errors.Is(err, store.ErrNotFound) && m.AdmitCount != nil {
		if err := m.AdmitCount(ctx, env.ID, "job"); err != nil {
			return store.Job{}, err
		}
	}
	raw, _ := json.Marshal(spec)
	now := m.now().UTC().Truncate(time.Second)
	j := store.Job{ID: auth.NewID("job_"), EnvironmentID: env.ID, Name: name, Spec: string(raw), CreatedAt: now, UpdatedAt: now, LastScheduledAt: &now}
	if err := m.st.PutJob(ctx, j); err != nil {
		return store.Job{}, err
	}
	return m.st.JobByName(ctx, env.ID, name)
}

// RunJob starts a job now (manual trigger), optionally with another command.
func (m *Manager) RunJob(ctx context.Context, job store.Job, command []string) (RunView, error) {
	spec, err := parseSpec(job.Spec)
	if err != nil {
		return RunView{}, err
	}
	if len(command) > 0 {
		spec.Command = command
	}
	env := store.Environment{ID: job.EnvironmentID}
	t, err := m.target(ctx, env, job.Project, job.Environment, &job, spec, 0)
	if err != nil {
		return RunView{}, err
	}
	run, err := m.start(ctx, t, "manual", 1, "")
	if err != nil {
		return RunView{}, err
	}
	return m.View(ctx, run), nil
}

// RunService starts an ad-hoc run of a service's task definition with
// command (`synctl run service/api -- rails db:migrate`).
func (m *Manager) RunService(ctx context.Context, sv store.Service, command []string) (RunView, error) {
	if len(command) == 0 {
		return RunView{}, ErrInvalid{errors.New("a command is required")}
	}
	spec := Spec{Kind: KindOneOff, Service: sv.Name, Command: command}
	t, err := m.target(ctx, store.Environment{ID: sv.EnvironmentID}, sv.Project, sv.Environment, nil, spec, 0)
	if err != nil {
		return RunView{}, err
	}
	run, err := m.start(ctx, t, "manual", 1, "")
	if err != nil {
		return RunView{}, err
	}
	return m.View(ctx, run), nil
}

// Cancel stops an active run.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	run, err := m.st.RunByID(ctx, id)
	if err != nil {
		return err
	}
	if run.Status != store.RunPending && run.Status != store.RunRunning {
		return ErrInvalid{errors.New("the run has already finished")}
	}
	m.finish(ctx, run, store.RunCancelled, nil, "cancelled")
	m.stopContainer(run, 0)
	return nil
}

func parseSpec(raw string) (Spec, error) {
	var s Spec
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}

// ── agent reports ───────────────────────────────────────────────────────────

func (m *Manager) onTaskStatus(node store.Node, s *agentv1.TaskStatus) {
	if !strings.HasPrefix(s.GetTaskId(), RunPrefix) {
		return
	}
	ctx := context.Background()
	run, err := m.st.RunByID(ctx, s.GetTaskId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) && s.GetState() != agentv1.TaskState_TASK_STATE_REMOVED {
			m.stopContainer(store.JobRun{ID: s.GetTaskId(), NodeID: node.ID}, 0)
		}
		return
	}
	if run.Status != store.RunPending && run.Status != store.RunRunning {
		return // finished; late reports (e.g. removal) change nothing
	}
	switch s.GetState() {
	case agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_STARTING, agentv1.TaskState_TASK_STATE_PULLING:
		if ip := s.GetIp(); ip != "" {
			if changed, err := m.st.SetRunIP(ctx, run.ID, ip); err == nil && changed {
				sv := m.svcFor(ctx, run)
				_ = m.st.AssignAddress(ctx, ip, run.ID, sv.Project+"/"+sv.Environment+"/"+sv.Name, node.ID, m.now().UTC())
				if m.OnRunAddress != nil {
					m.OnRunAddress() // security group sets include job runs (§8.3)
				}
			}
		}
		if run.Status == store.RunPending && s.GetState() == agentv1.TaskState_TASK_STATE_RUNNING {
			now := m.now().UTC()
			if s.GetStartedAtUnix() > 0 {
				now = time.Unix(s.GetStartedAtUnix(), 0).UTC()
			}
			run.Status, run.StartedAt, run.Message = store.RunRunning, &now, ""
			_ = m.st.UpdateRun(ctx, run)
			m.publish(ctx, run)
		}
	case agentv1.TaskState_TASK_STATE_EXITED:
		code := int(s.GetExitCode())
		if code == 0 {
			m.finish(ctx, run, store.RunSucceeded, &code, "")
		} else {
			m.finish(ctx, run, store.RunFailed, &code, fmt.Sprintf("exited with code %d", code))
		}
		m.stopContainer(run, 10*time.Second)
	case agentv1.TaskState_TASK_STATE_FAILED:
		m.finish(ctx, run, store.RunFailed, nil, s.GetError())
		m.stopContainer(run, 0)
	case agentv1.TaskState_TASK_STATE_REMOVED:
		m.finish(ctx, run, store.RunFailed, nil, "the container disappeared")
	}
}

// finish records the outcome, retries failures and reports hook results.
func (m *Manager) finish(ctx context.Context, run store.JobRun, status string, code *int, msg string) {
	now := m.now().UTC()
	if run.StartedAt == nil && status == store.RunSucceeded {
		run.StartedAt = &now
	}
	run.Status, run.ExitCode, run.Message, run.FinishedAt = status, code, msg, &now
	if err := m.st.UpdateRun(ctx, run); err != nil {
		m.log.Error("update run", "run", run.ID, "err", err)
		return
	}
	m.publish(ctx, run)
	m.log.Info("job run finished", "run", run.ID, "status", status, "message", msg)
	_ = m.st.ReleaseAddress(ctx, run.ID, now)
	if m.OnRunAddress != nil {
		m.OnRunAddress() // its address leaves the security group sets
	}

	var spec Spec
	if run.JobID != "" {
		if j, err := m.st.JobByID(ctx, run.JobID); err == nil {
			spec, _ = parseSpec(j.Spec)
			defer func() { _ = m.st.PruneRuns(ctx, run.JobID, spec.HistoryLimit) }()
		}
	}
	if m.OnFinished != nil {
		defer m.OnFinished(ctx, run)
	}
	if (status == store.RunFailed || status == store.RunTimedOut) && run.Attempt <= spec.Retries {
		delay := time.Duration(10<<min(run.Attempt-1, 5)) * time.Second
		m.log.Info("retrying job run", "run", run.ID, "attempt", run.Attempt+1, "in", delay)
		time.AfterFunc(delay, func() { m.retry(context.Background(), run) })
		return
	}
	if run.DeploymentID != "" {
		m.hookDone(ctx, run.DeploymentID, run.ID, status == store.RunSucceeded, msg)
	}
}

func (m *Manager) retry(ctx context.Context, prev store.JobRun) {
	spec, err := workload.ParseSpec(prev.Spec)
	if err != nil {
		return
	}
	project, envName := m.envNames(ctx, prev.EnvironmentID)
	t := runTarget{envID: prev.EnvironmentID, project: project, environment: envName, jobID: prev.JobID, revision: prev.Revision, spec: spec}
	if prev.ServiceID != "" {
		if sv, err := m.st.ServiceByID(ctx, prev.ServiceID); err == nil {
			t.service = &sv
		}
	}
	run, err := m.start(ctx, t, "retry", prev.Attempt+1, prev.DeploymentID)
	if err != nil {
		m.log.Error("retry job run", "run", prev.ID, "err", err)
		return
	}
	if prev.DeploymentID != "" {
		m.mu.Lock()
		if set := m.hooks[prev.DeploymentID]; set != nil {
			delete(set, prev.ID)
			set[run.ID] = true
		}
		m.mu.Unlock()
	}
}

// onConnect resolves runs whose node reconnected.
func (m *Manager) onConnect(c agentgw.Conn) {
	ctx := context.Background()
	have := map[string]*agentv1.TaskStatus{}
	for _, s := range c.Hello.GetTasks() {
		if strings.HasPrefix(s.GetTaskId(), RunPrefix) {
			have[s.GetTaskId()] = s
		}
	}
	runs, err := m.st.ActiveRuns(ctx)
	if err != nil {
		return
	}
	for _, r := range runs {
		if r.NodeID != c.Node.ID {
			continue
		}
		if s, ok := have[r.ID]; ok {
			m.onTaskStatus(c.Node, s)
			delete(have, r.ID)
		} else if r.Status == store.RunRunning {
			m.finish(ctx, r, store.RunFailed, nil, "lost while the node was disconnected")
		} else {
			m.send(ctx, r)
		}
	}
	for _, s := range have { // finished or unknown runs still on the node
		m.onTaskStatus(c.Node, s)
	}
}

// ── views ───────────────────────────────────────────────────────────────────

func (m *Manager) View(ctx context.Context, r store.JobRun) RunView {
	project, envName := m.envNames(ctx, r.EnvironmentID)
	v := RunView{ID: r.ID, Project: project, Environment: envName, Revision: r.Revision, Trigger: r.Trigger, Attempt: r.Attempt,
		Status: r.Status, ExitCode: r.ExitCode, Message: r.Message, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}
	if r.JobID != "" {
		if j, err := m.st.JobByID(ctx, r.JobID); err == nil {
			v.Job = j.Name
		}
	}
	if r.ServiceID != "" {
		if sv, err := m.st.ServiceByID(ctx, r.ServiceID); err == nil {
			v.Service = sv.Name
		}
	}
	if n, ok := m.nodes.Get(r.NodeID); ok {
		v.Node = n.Name
	}
	if s, err := workload.ParseSpec(r.Spec); err == nil {
		v.Command = s.Command
	}
	if v.Command == nil {
		v.Command = []string{}
	}
	return v
}

func (m *Manager) publish(ctx context.Context, r store.JobRun) {
	m.bus.Publish(TopicRun, m.View(ctx, r))
}
