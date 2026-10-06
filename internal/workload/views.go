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
	ID           string   `json:"id"`
	Project      string   `json:"project"`
	Environment  string   `json:"environment"`
	Name         string   `json:"name"`
	Revision     int      `json:"revision"`
	DesiredCount int      `json:"desiredCount"`
	Running      int      `json:"running"`
	Pending      int      `json:"pending"`
	Status       string   `json:"status"`
	Deleting     bool     `json:"deleting"`
	Spec         Spec     `json:"spec"`
	Endpoints    []string `json:"endpoints"`
	VIP          string   `json:"vip"`
	// Deployment is the latest rollout.
	Deployment *store.Deployment `json:"deployment"`
	DNSName    string            `json:"dnsName"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
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
var Endpoints = func(sv store.Service, spec Spec, domains []store.Domain) []string { return nil }

// Discovery returns a service's VIP and internal DNS name (set by the
// discovery layer).
var Discovery = func(sv store.Service) (vip, dnsName string) { return "", "" }

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
	domains, err := m.st.ListDomains(ctx, sv.ID)
	if err != nil {
		return ServiceView{}, err
	}
	v := ServiceView{
		ID: sv.ID, Project: sv.Project, Environment: sv.Environment, Name: sv.Name, Revision: sv.Revision,
		DesiredCount: sv.DesiredCount, Status: sv.Status, Deleting: sv.Deleting, Spec: spec,
		Endpoints: Endpoints(sv, spec, domains), CreatedAt: sv.CreatedAt, UpdatedAt: sv.UpdatedAt,
	}
	if v.Endpoints == nil {
		v.Endpoints = []string{}
	}
	v.VIP, v.DNSName = Discovery(sv)
	if ds, err := m.st.ListDeployments(ctx, sv.ID, 1); err == nil && len(ds) == 1 {
		v.Deployment = &ds[0]
	}
	tasks, err := m.st.ServiceTasks(ctx, sv.ID, 0)
	if err != nil {
		return v, err
	}
	for _, t := range tasks {
		if t.Desired != "running" {
			continue
		}
		if m.serving(ctx, t) {
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
		m.startDeployment(ctx, sv.ID, 0, 1, "")
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
		rev, err := m.st.UpdateService(ctx, sv.ID, spec.Canonical(), desired, actor, now)
		if err != nil {
			return ServiceView{}, false, err
		}
		if rev != sv.Revision {
			m.startDeployment(ctx, sv.ID, sv.Revision, rev, "")
		}
	}
	m.routesDirty()
	if m.OnChange != nil {
		m.OnChange()
	}
	m.Enqueue(sv.ID)
	v, err := m.ServiceView(ctx, sv.ID)
	return v, created, err
}

// Serving reports whether a task receives traffic (running and healthy).
func (m *Manager) Serving(ctx context.Context, t store.Task) bool { return m.serving(ctx, t) }

// startDeployment records a rollout to toRev.
func (m *Manager) startDeployment(ctx context.Context, serviceID string, fromRev, toRev int, msg string) {
	err := m.st.StartDeployment(ctx, store.Deployment{ID: auth.NewID("dep_"), ServiceID: serviceID, FromRev: fromRev, ToRev: toRev,
		Message: msg, StartedAt: m.now().UTC().Truncate(time.Second)})
	if err != nil {
		m.log.Error("record deployment", "service", serviceID, "err", err)
	}
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
	return m.rollback(ctx, sv, revision, actor, fmt.Sprintf("rollback to revision %d", revision))
}

func (m *Manager) rollback(ctx context.Context, sv store.Service, revision int, actor, msg string) (ServiceView, error) {
	td, err := m.st.TaskDefinition(ctx, sv.ID, revision)
	if err != nil {
		return ServiceView{}, err
	}
	rev, err := m.st.UpdateService(ctx, sv.ID, td.Spec, sv.DesiredCount, actor, m.now())
	if err != nil {
		return ServiceView{}, err
	}
	if rev != sv.Revision {
		m.startDeployment(ctx, sv.ID, sv.Revision, rev, msg)
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

// AddDomain routes host to one of the service's HTTP ports ("" = the first).
// The host must already be validated and normalized by the caller.
func (m *Manager) AddDomain(ctx context.Context, serviceID, host, port string) (store.Domain, error) {
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return store.Domain{}, err
	}
	spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		return store.Domain{}, err
	}
	ports := spec.HTTPPorts()
	if len(ports) == 0 {
		return store.Domain{}, ErrInvalid{errors.New("the service has no http port to route a domain to")}
	}
	if port == "" {
		port = ports[0].Name
	}
	found := false
	for _, p := range ports {
		found = found || p.Name == port
	}
	if !found {
		return store.Domain{}, ErrInvalid{fmt.Errorf("the service has no http port named %q", port)}
	}
	d := store.Domain{ID: auth.NewID("dom_"), ServiceID: sv.ID, Host: host, PortName: port, CreatedAt: m.now().UTC().Truncate(time.Second)}
	if err := m.st.AddDomain(ctx, d); errors.Is(err, store.ErrNameTaken) {
		return store.Domain{}, ErrInvalid{fmt.Errorf("%s is already routed to a service", host)}
	} else if err != nil {
		return store.Domain{}, err
	}
	m.domainsChanged()
	return d, nil
}

// RemoveDomain stops routing host to the service.
func (m *Manager) RemoveDomain(ctx context.Context, serviceID, host string) error {
	if err := m.st.DeleteDomain(ctx, serviceID, host); err != nil {
		return err
	}
	m.domainsChanged()
	return nil
}

func (m *Manager) domainsChanged() {
	m.routesDirty()
	if m.OnChange != nil {
		m.OnChange() // certificates for the new host set
	}
}
