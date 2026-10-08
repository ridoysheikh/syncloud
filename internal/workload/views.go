package workload

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
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
	// Central is the controller's probe over the private network (§5.6):
	// ok, failing or unreachable ("" when not probed).
	Central      string `json:"central,omitempty"`
	CentralError string `json:"centralError,omitempty"`
}

// CentralCheck returns the controller's probe state of a task (set by the
// health monitor).
var CentralCheck = func(taskID string) (state, err string) { return "", "" }

// Endpoints returns the public URLs of a service (set by the routing layer).
var Endpoints = func(sv store.Service, spec Spec, domains []store.Domain, routing []store.PortRouting) []string {
	return nil
}

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
	routing, err := m.st.ListRouting(ctx, sv.ID)
	if err != nil {
		return ServiceView{}, err
	}
	v := ServiceView{
		ID: sv.ID, Project: sv.Project, Environment: sv.Environment, Name: sv.Name, Revision: sv.Revision,
		DesiredCount: sv.DesiredCount, Status: sv.Status, Deleting: sv.Deleting, Spec: spec,
		Endpoints: Endpoints(sv, spec, domains, routing), CreatedAt: sv.CreatedAt, UpdatedAt: sv.UpdatedAt,
	}
	if v.Endpoints == nil {
		v.Endpoints = []string{}
	}
	v.VIP, v.DNSName = Discovery(sv)
	if ds, err := m.st.ListDeployments(ctx, sv.ID, 1, ""); err == nil && len(ds) == 1 {
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
	if t.State == store.TaskRunning {
		v.Central, v.CentralError = CentralCheck(t.ID)
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

// AdmitRequest is a change to a service's footprint, checked against quotas
// (§7.2) before it is stored.
type AdmitRequest struct {
	EnvironmentID string
	ServiceID     string // "" for a new service
	Spec          *Spec  // nil: the current spec
	Desired       int
}

// ErrQuota means a change would exceed a quota.
type ErrQuota struct{ Msg string }

func (e ErrQuota) Error() string { return e.Msg }

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
	// Every revision carries the environment's shared variables as they are now.
	e, err := m.st.EnvironmentByID(ctx, env.ID)
	if err != nil {
		return ServiceView{}, false, err
	}
	spec.SharedEnv = nil
	if len(e.SharedEnv) > 0 {
		spec.SharedEnv = e.SharedEnv
	}
	// A service can narrow its project's allowed nodes, never widen them.
	if p, err := m.st.ProjectByID(ctx, e.ProjectID); err == nil && len(p.Nodes) > 0 {
		for _, n := range append(slices.Clone(spec.Placement.Nodes), spec.Placement.Node) {
			if n != "" && !slices.Contains(p.Nodes, n) {
				return ServiceView{}, false, ErrInvalid{fmt.Errorf("node %s is not allowed in project %s (allowed: %s)", n, p.Name, strings.Join(p.Nodes, ", "))}
			}
		}
	}
	if desired > maxDesired {
		return ServiceView{}, false, ErrInvalid{fmt.Errorf("desiredCount must be at most %d", maxDesired)}
	}
	if err := m.checkLock(ctx, env.ID); err != nil {
		return ServiceView{}, false, err
	}
	now := m.now().UTC().Truncate(time.Second)
	sv, err := m.st.ServiceByName(ctx, env.ID, name)
	created := false
	// ...and its S3 bindings (a new service has none yet).
	spec.S3 = nil
	if err == nil && m.S3Bindings != nil {
		refs, berr := m.S3Bindings(ctx, sv.ID)
		if berr != nil {
			return ServiceView{}, false, berr
		}
		spec.S3 = refs
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		if desired < 0 {
			desired = 1
		}
		if m.Admit != nil {
			if err := m.Admit(ctx, AdmitRequest{EnvironmentID: env.ID, Spec: &spec, Desired: desired}); err != nil {
				return ServiceView{}, false, err
			}
		}
		held, _ := ctx.Value(holdKey{}).(bool)
		sv = store.Service{ID: auth.NewID("svc_"), EnvironmentID: env.ID, Name: name, DesiredCount: desired, Held: held, CreatedAt: now}
		if err := m.st.CreateService(ctx, sv, spec.Canonical(), actor); err != nil {
			if errors.Is(err, store.ErrNameTaken) {
				return ServiceView{}, false, ErrInvalid{fmt.Errorf("service %s already exists", name)}
			}
			return ServiceView{}, false, err
		}
		if held {
			if _, err := m.recordDeployment(ctx, sv.ID, 0, 1, actor, store.DeployWaitingHook, ""); err != nil {
				return ServiceView{}, false, err
			}
		} else {
			m.startDeployment(ctx, sv.ID, 0, 1, actor, "")
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
		if m.Admit != nil {
			if err := m.Admit(ctx, AdmitRequest{EnvironmentID: env.ID, ServiceID: sv.ID, Spec: &spec, Desired: desired}); err != nil {
				return ServiceView{}, false, err
			}
		}
		// A redeploy marker survives edits that don't mention it, so applying
		// the same spec again stays a no-op.
		if spec.RedeployedAt == "" {
			if cur, err := m.SpecFor(ctx, sv.ID, sv.Revision); err == nil {
				spec.RedeployedAt = cur.RedeployedAt
			}
		}
		if err := m.rollout(ctx, sv, spec.Canonical(), desired, actor, true, ""); err != nil {
			return ServiceView{}, false, err
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

// rollout makes spec (canonical JSON) the service's next revision and
// deploys it. With hooks and pre-deploy jobs, the new revision becomes
// current only after they succeed (§5.11); until then the old one keeps
// running.
func (m *Manager) rollout(ctx context.Context, sv store.Service, spec string, desired int, actor string, hooks bool, msg string) error {
	now := m.now().UTC().Truncate(time.Second)
	if hooks && m.DeployHooks != nil && m.DeployHooks.HasPreDeploy(ctx, sv) {
		rev, changed, err := m.st.AddRevision(ctx, sv.ID, spec, actor, now)
		if err != nil {
			return err
		}
		if _, err := m.st.UpdateService(ctx, sv.ID, "", desired, actor, now); err != nil {
			return err
		}
		if changed {
			depID, err := m.recordDeployment(ctx, sv.ID, sv.Revision, rev, actor, store.DeployWaitingHook, msg)
			if err != nil {
				return err
			}
			m.DeployHooks.RunPreDeploy(ctx, sv, rev, depID)
		}
		return nil
	}
	rev, err := m.st.UpdateService(ctx, sv.ID, spec, desired, actor, now)
	if err != nil {
		return err
	}
	if rev != sv.Revision {
		m.startDeployment(ctx, sv.ID, sv.Revision, rev, actor, msg)
	}
	if sv.Held {
		return m.st.ReleaseService(ctx, sv.ID) // no pre-deploy jobs left to wait for
	}
	return nil
}

// startDeployment records a rollout to toRev.
func (m *Manager) startDeployment(ctx context.Context, serviceID string, fromRev, toRev int, actor, msg string) {
	if _, err := m.recordDeployment(ctx, serviceID, fromRev, toRev, actor, "", msg); err != nil {
		m.log.Error("record deployment", "service", serviceID, "err", err)
	}
	if fromRev > 0 {
		go m.prePull(context.WithoutCancel(ctx), serviceID, fromRev, toRev)
	}
}

// prePull asks the nodes likely to run the new revision to pull its image
// now (§5.9): the nodes running the service, then other eligible nodes up
// to the desired count.
func (m *Manager) prePull(ctx context.Context, serviceID string, fromRev, toRev int) {
	from, err1 := m.SpecFor(ctx, serviceID, fromRev)
	to, err2 := m.SpecFor(ctx, serviceID, toRev)
	if err1 != nil || err2 != nil || from.Image == to.Image || to.Image == AwaitingBuild || m.gw == nil {
		return
	}
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return
	}
	tasks, _ := m.st.ServiceTasks(ctx, serviceID, 0)
	targets := map[string]bool{}
	for _, t := range tasks {
		if t.Desired == "running" && t.NodeID != "" {
			targets[t.NodeID] = true
		}
	}
	for _, n := range m.nodes.List() {
		if len(targets) >= sv.DesiredCount {
			break
		}
		if n.Status == store.NodeReady && n.Connected && n.Schedulable && n.Info.DockerVersion != "" && m.netReady(n.ID) &&
			(to.Placement.Node == "" || n.Name == to.Placement.Node) {
			targets[n.ID] = true
		}
	}
	image, auth := to.Image, ""
	if m.ResolveImage != nil {
		image, auth = m.ResolveImage(image)
	}
	ca := ""
	if m.RegistryCA != nil {
		ca = m.RegistryCA(image)
	}
	for id := range targets {
		_ = m.gw.Send(id, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_PullImage{PullImage: &agentv1.PullImage{Image: image, RegistryAuth: auth, RegistryCa: ca}}})
	}
	m.log.Info("pre-pulling image", "service", sv.Name, "image", image, "nodes", len(targets))
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
	if m.Admit != nil && desired > sv.DesiredCount {
		if err := m.Admit(ctx, AdmitRequest{EnvironmentID: sv.EnvironmentID, ServiceID: sv.ID, Desired: desired}); err != nil {
			return ServiceView{}, err
		}
	}
	if _, err := m.st.UpdateService(ctx, sv.ID, "", desired, actor, m.now()); err != nil {
		return ServiceView{}, err
	}
	m.Enqueue(sv.ID)
	return m.ServiceView(ctx, sv.ID)
}

// Rollback makes an earlier revision current again (as a new revision).
// Pre-deploy hooks run only with runHooks: migrations are usually forward-only.
func (m *Manager) Rollback(ctx context.Context, serviceID string, revision int, actor string, runHooks bool) (ServiceView, error) {
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return ServiceView{}, err
	}
	if sv.Deleting {
		return ServiceView{}, ErrInvalid{errors.New("the service is being deleted")}
	}
	if revision == sv.Revision {
		return ServiceView{}, ErrInvalid{fmt.Errorf("revision %d is already current", revision)}
	}
	return m.rollback(ctx, sv, revision, actor, fmt.Sprintf("rollback to revision %d", revision), runHooks)
}

func (m *Manager) rollback(ctx context.Context, sv store.Service, revision int, actor, msg string, runHooks bool) (ServiceView, error) {
	td, err := m.st.TaskDefinition(ctx, sv.ID, revision)
	if errors.Is(err, store.ErrNotFound) {
		return ServiceView{}, ErrInvalid{fmt.Errorf("revision %d no longer exists", revision)}
	} else if err != nil {
		return ServiceView{}, err
	}
	if spec, err := ParseSpec(td.Spec); err == nil && m.ImageAvailable != nil {
		if ok, err := m.ImageAvailable(ctx, spec.Image); err == nil && !ok {
			return ServiceView{}, ErrInvalid{fmt.Errorf("the image of revision %d (%s) was removed by registry cleanup; raise the project's rollback window to keep more", revision, spec.Image)}
		}
	}
	if _, ok := ctx.Value(causeKey{}).(Cause); !ok && actor != actorCircuitBreaker {
		ctx = WithCause(ctx, Cause{Trigger: store.TriggerRollback})
	}
	if err := m.rollout(ctx, sv, td.Spec, sv.DesiredCount, actor, runHooks, msg); err != nil {
		return ServiceView{}, err
	}
	m.Enqueue(sv.ID)
	return m.ServiceView(ctx, sv.ID)
}

// SetSharedEnv replaces an environment's shared variables and rolls them out:
// every service whose variables change gets a new revision (a normal rolling
// deployment). It returns the services that were redeployed.
func (m *Manager) SetSharedEnv(ctx context.Context, env store.Environment, vars map[string]string, actor string) ([]string, error) {
	if err := ValidateEnv(vars); err != nil {
		return nil, ErrInvalid{err}
	}
	if err := m.checkLock(ctx, env.ID); err != nil {
		return nil, err // the variables would redeploy every service
	}
	if err := m.st.SetSharedEnv(ctx, env.ID, vars); err != nil {
		return nil, err
	}
	services, err := m.st.ListServicesIn(ctx, env.ID)
	if err != nil {
		return nil, err
	}
	redeployed := []string{}
	for _, sv := range services {
		if sv.Deleting {
			continue
		}
		spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			return redeployed, err
		}
		before := sv.Revision
		v, _, err := m.Apply(WithCause(ctx, Cause{Trigger: store.TriggerVariables}), env, sv.Name, spec, -1, actor)
		if err != nil {
			return redeployed, fmt.Errorf("%s: %w", sv.Name, err)
		}
		if v.Revision != before {
			redeployed = append(redeployed, sv.Name)
		}
	}
	return redeployed, nil
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

// DomainInput is a custom domain to add (Phase 15c adds Path, StripPrefix
// and RedirectTo).
type DomainInput struct {
	Host        string // validated and normalized by the caller
	Port        string // the HTTP port ("" = the first)
	Path        string // a path prefix ("" = the whole host)
	StripPrefix bool
	RedirectTo  string // validated and normalized by the caller; "" = route
}

var pathRE = regexp.MustCompile(`^(/[A-Za-z0-9._~!$&'()*+,;=:@%-]+)+$`)

// AddDomain routes a host (or a path of it) to one of the service's HTTP
// ports, or makes it redirect to another host.
func (m *Manager) AddDomain(ctx context.Context, serviceID string, in DomainInput) (store.Domain, error) {
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
	port := in.Port
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
	path := strings.TrimRight(strings.TrimSpace(in.Path), "/")
	switch {
	case path != "" && (!pathRE.MatchString(path) || len(path) > 200 || strings.Contains(path, "//")):
		return store.Domain{}, ErrInvalid{fmt.Errorf("path %q: start with / and use URL path characters", in.Path)}
	case in.StripPrefix && path == "":
		return store.Domain{}, ErrInvalid{errors.New("stripping the prefix needs a path")}
	case in.RedirectTo != "" && path != "":
		return store.Domain{}, ErrInvalid{errors.New("a redirect applies to the whole host: leave the path empty")}
	case in.RedirectTo != "" && in.RedirectTo == in.Host:
		return store.Domain{}, ErrInvalid{errors.New("a domain cannot redirect to itself")}
	}
	if m.AdmitCount != nil {
		if err := m.AdmitCount(ctx, sv.EnvironmentID, "domain"); err != nil {
			return store.Domain{}, err
		}
	}
	d := store.Domain{ID: auth.NewID("dom_"), ServiceID: sv.ID, Host: in.Host, Path: path, PortName: port, StripPrefix: in.StripPrefix,
		RedirectTo: in.RedirectTo, CreatedAt: m.now().UTC().Truncate(time.Second)}
	if err := m.st.AddDomain(ctx, d); errors.Is(err, store.ErrNameTaken) {
		where := in.Host
		if path != "" {
			where += path
		}
		return store.Domain{}, ErrInvalid{fmt.Errorf("%s is already routed to a service", where)}
	} else if err != nil {
		return store.Domain{}, err
	}
	m.domainsChanged()
	return d, nil
}

// RemoveDomain stops routing a domain (by ID, or every path of a host) to
// the service.
func (m *Manager) RemoveDomain(ctx context.Context, serviceID, ref string) error {
	if err := m.st.DeleteDomain(ctx, serviceID, ref); err != nil {
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

// PreDeployDone finishes the pre-deploy phase of a deployment: on success the
// new revision becomes current and rolls out; on failure the deployment
// fails and the old revision keeps running.
func (m *Manager) PreDeployDone(ctx context.Context, depID string, ok bool, msg string) {
	dep, err := m.st.DeploymentByID(ctx, depID)
	if err != nil || dep.Status != store.DeployWaitingHook {
		return // superseded meanwhile
	}
	now := m.now().UTC()
	if !ok {
		_ = m.st.SetDeploymentStatus(ctx, depID, store.DeployFailed, "pre-deploy job failed: "+msg, &now)
		m.DeploymentEvent(ctx, depID, store.DeployFailed, "pre-deploy job failed: "+msg)
		m.log.Warn("deployment aborted by pre-deploy job", "service", dep.ServiceID, "revision", dep.ToRev, "reason", msg)
	} else {
		if err := m.st.SetServiceRevision(ctx, dep.ServiceID, dep.ToRev, now); err != nil {
			m.log.Error("switch revision", "service", dep.ServiceID, "err", err)
			return
		}
		if err := m.st.ReleaseService(ctx, dep.ServiceID); err != nil {
			m.log.Error("release service", "service", dep.ServiceID, "err", err)
		}
		_ = m.st.SetDeploymentStatus(ctx, depID, store.DeployInProgress, "", nil) // the timeline says the jobs passed
		m.DeploymentEvent(ctx, depID, "hooks-done", fmt.Sprintf("pre-deploy jobs succeeded; revision %d is now current", dep.ToRev))
		m.routesDirty()
		if dep.FromRev > 0 {
			go m.prePull(context.WithoutCancel(ctx), dep.ServiceID, dep.FromRev, dep.ToRev)
		}
	}
	m.Enqueue(dep.ServiceID)
	if v, err := m.ServiceView(ctx, dep.ServiceID); err == nil {
		m.bus.Publish(TopicService, v)
	}
}
