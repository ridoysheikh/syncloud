package workload

import (
	"context"
	"errors"
	"fmt"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/store"
)

// ServiceView is a service as the API shows it.
type ServiceView struct {
	ID           string    `json:"id"`
	Project      string    `json:"project"`
	Environment  string    `json:"environment"`
	Name         string    `json:"name"`
	Revision     int       `json:"revision"`
	DesiredCount int       `json:"desiredCount"`
	Running      int       `json:"running"`
	Pending      int       `json:"pending"`
	Status       string    `json:"status"`
	Deleting     bool      `json:"deleting"`
	Spec         Spec      `json:"spec"`
	Endpoints    []string  `json:"endpoints"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// TaskView is a task as the API shows it.
type TaskView struct {
	ID          string     `json:"id"`
	ServiceID   string     `json:"serviceId"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Service     string     `json:"service"`
	Revision    int        `json:"revision"`
	NodeID      string     `json:"nodeId"`
	Node        string     `json:"node"`
	Desired     string     `json:"desired"`
	State       string     `json:"state"`
	IP          string     `json:"ip"`
	ContainerID string     `json:"containerId"`
	Health      string     `json:"health"`
	ExitCode    int        `json:"exitCode"`
	Error       string     `json:"error"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

// Endpoints returns the public URLs of a service (set by the routing layer).
var Endpoints = func(sv store.Service, spec Spec) []string { return nil }

func (m *Manager) ServiceView(ctx context.Context, id string) (ServiceView, error) {
	sv, err := m.st.ServiceByID(ctx, id)
	if err != nil {
		return ServiceView{}, err
	}
	return m.serviceView(ctx, sv)
}

func (m *Manager) serviceView(ctx context.Context, sv store.Service) (ServiceView, error) {
	spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		return ServiceView{}, err
	}
	v := ServiceView{
		ID: sv.ID, Project: sv.Project, Environment: sv.Environment, Name: sv.Name, Revision: sv.Revision,
		DesiredCount: sv.DesiredCount, Status: sv.Status, Deleting: sv.Deleting, Spec: spec,
		Endpoints: Endpoints(sv, spec), CreatedAt: sv.CreatedAt, UpdatedAt: sv.UpdatedAt,
	}
	if v.Endpoints == nil {
		v.Endpoints = []string{}
	}
	tasks, err := m.st.ServiceTasks(ctx, sv.ID, 0)
	if err != nil {
		return v, err
	}
	for _, t := range tasks {
		if t.Desired != "running" {
			continue
		}
		if t.State == store.TaskRunning {
			v.Running++
		} else if live(t) {
			v.Pending++
		}
	}
	return v, nil
}

// ListServices returns services in an environment ("" = all).
func (m *Manager) ListServices(ctx context.Context, environmentID string) ([]ServiceView, error) {
	var svcs []store.Service
	var err error
	if environmentID == "" {
		svcs, err = m.st.ListServices(ctx)
	} else {
		svcs, err = m.st.ListServicesIn(ctx, environmentID)
	}
	if err != nil {
		return nil, err
	}
	out := make([]ServiceView, 0, len(svcs))
	for _, sv := range svcs {
		v, err := m.serviceView(ctx, sv)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (m *Manager) taskView(ctx context.Context, t store.Task) (TaskView, error) {
	v := TaskView{
		ID: t.ID, ServiceID: t.ServiceID, Revision: t.Revision, NodeID: t.NodeID, Desired: t.Desired, State: t.State,
		IP: t.IP, ContainerID: t.ContainerID, Health: t.Health, ExitCode: t.ExitCode, Error: t.Error,
		CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
	}
	if n, ok := m.nodes.Get(t.NodeID); ok {
		v.Node = n.Name
	}
	sv, err := m.st.ServiceByID(ctx, t.ServiceID)
	if err == nil {
		v.Project, v.Environment, v.Service = sv.Project, sv.Environment, sv.Name
	}
	return v, nil
}

// ServiceTasks lists a service's tasks (active first, then recent history).
func (m *Manager) ServiceTasks(ctx context.Context, serviceID string) ([]TaskView, error) {
	ts, err := m.st.ServiceTasks(ctx, serviceID, keepStopped)
	if err != nil {
		return nil, err
	}
	out := make([]TaskView, 0, len(ts))
	for _, t := range ts {
		v, _ := m.taskView(ctx, t)
		out = append(out, v)
	}
	return out, nil
}

// ActiveTasks lists every active task in the cluster.
func (m *Manager) ActiveTasks(ctx context.Context) ([]TaskView, error) {
	ts, err := m.st.ActiveTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TaskView, 0, len(ts))
	for _, t := range ts {
		v, _ := m.taskView(ctx, t)
		out = append(out, v)
	}
	return out, nil
}

// ── changes from the API ────────────────────────────────────────────────────

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

const maxDesired = 100

// Apply creates the service or updates it (a spec change creates a revision
// and rolls it out). desired < 0 keeps the current count (1 for new services).
func (m *Manager) Apply(ctx context.Context, env store.Environment, name string, spec Spec, desired int, actor string) (ServiceView, bool, error) {
	if err := ValidName(name); err != nil {
		return ServiceView{}, false, ErrInvalid{fmt.Errorf("service name %w", err)}
	}
	if err := spec.Normalize(); err != nil {
		return ServiceView{}, false, ErrInvalid{err}
	}
	if desired > maxDesired {
		return ServiceView{}, false, ErrInvalid{fmt.Errorf("desiredCount must be at most %d", maxDesired)}
	}
	now := m.now().UTC().Truncate(time.Second)
	sv, err := m.st.ServiceByName(ctx, env.ID, name)
	created := false
	switch {
	case errors.Is(err, store.ErrNotFound):
		if desired < 0 {
			desired = 1
		}
		sv = store.Service{ID: auth.NewID("svc_"), EnvironmentID: env.ID, Name: name, DesiredCount: desired, CreatedAt: now}
		if err := m.st.CreateService(ctx, sv, spec.Canonical(), actor); err != nil {
			if errors.Is(err, store.ErrNameTaken) {
				return ServiceView{}, false, ErrInvalid{fmt.Errorf("service %s already exists", name)}
			}
			return ServiceView{}, false, err
		}
		created = true
	case err != nil:
		return ServiceView{}, false, err
	default:
		if sv.Deleting {
			return ServiceView{}, false, ErrInvalid{errors.New("the service is being deleted")}
		}
		if desired < 0 {
			desired = sv.DesiredCount
		}
		if _, err := m.st.UpdateService(ctx, sv.ID, spec.Canonical(), desired, actor, now); err != nil {
			return ServiceView{}, false, err
		}
	}
	m.Enqueue(sv.ID)
	v, err := m.ServiceView(ctx, sv.ID)
	return v, created, err
}

// Scale sets the desired count.
func (m *Manager) Scale(ctx context.Context, serviceID string, desired int, actor string) (ServiceView, error) {
	if desired < 0 || desired > maxDesired {
		return ServiceView{}, ErrInvalid{fmt.Errorf("desiredCount must be 0–%d", maxDesired)}
	}
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return ServiceView{}, err
	}
	if sv.Deleting {
		return ServiceView{}, ErrInvalid{errors.New("the service is being deleted")}
	}
	if _, err := m.st.UpdateService(ctx, sv.ID, "", desired, actor, m.now()); err != nil {
		return ServiceView{}, err
	}
	m.Enqueue(sv.ID)
	return m.ServiceView(ctx, sv.ID)
}

// Rollback makes an earlier revision current again (as a new revision).
func (m *Manager) Rollback(ctx context.Context, serviceID string, revision int, actor string) (ServiceView, error) {
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return ServiceView{}, err
	}
	td, err := m.st.TaskDefinition(ctx, serviceID, revision)
	if err != nil {
		return ServiceView{}, err
	}
	if _, err := m.st.UpdateService(ctx, sv.ID, td.Spec, sv.DesiredCount, actor, m.now()); err != nil {
		return ServiceView{}, err
	}
	m.Enqueue(sv.ID)
	return m.ServiceView(ctx, sv.ID)
}

// Delete stops all tasks, then removes the service.
func (m *Manager) Delete(ctx context.Context, serviceID string) error {
	if err := m.st.MarkServiceDeleting(ctx, serviceID, m.now()); err != nil {
		return err
	}
	m.Enqueue(serviceID)
	return nil
}

// RestartTask stops one task; the reconciler starts a replacement.
func (m *Manager) RestartTask(ctx context.Context, taskID string) error {
	t, err := m.st.TaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if t.Desired != "running" {
		return ErrInvalid{errors.New("the task is not running")}
	}
	m.stop(ctx, t, m.now())
	m.Enqueue(t.ServiceID)
	return nil
}
