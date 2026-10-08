package workload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/store"
)

// Cause says what started a deployment (Phase 15a).
type Cause struct {
	Trigger string // store.Trigger*
	BuildID string
}

type causeKey struct{}

// WithCause marks the deployments started under ctx.
func WithCause(ctx context.Context, c Cause) context.Context {
	return context.WithValue(ctx, causeKey{}, c)
}

type holdKey struct{}

// WithHeldFirstDeploy makes a service created under ctx wait for its
// pre-deploy jobs before its first tasks start: the caller adds the jobs,
// then starts them with DeployHooks.RunPreDeploy for the service's
// deployment.
func WithHeldFirstDeploy(ctx context.Context) context.Context {
	return context.WithValue(ctx, holdKey{}, true)
}

// actorCircuitBreaker is the actor of automatic rollbacks.
const actorCircuitBreaker = "circuit-breaker"

// causeOf is ctx's cause, else what the actor implies: builds deploy as
// "build:<id>", automatic rollbacks as the circuit breaker.
func causeOf(ctx context.Context, actor string) Cause {
	if c, ok := ctx.Value(causeKey{}).(Cause); ok {
		return c
	}
	switch {
	case strings.HasPrefix(actor, "build:"):
		return Cause{Trigger: store.TriggerGit, BuildID: strings.TrimPrefix(actor, "build:")}
	case actor == actorCircuitBreaker:
		return Cause{Trigger: store.TriggerAutoRollback}
	}
	return Cause{Trigger: store.TriggerManual}
}

// recordDeployment stores a deployment from fromRev to toRev with its cause,
// image and change summary, and opens its timeline.
func (m *Manager) recordDeployment(ctx context.Context, serviceID string, fromRev, toRev int, actor, status, msg string) (string, error) {
	c := causeOf(ctx, actor)
	if strings.HasPrefix(actor, "build:") || actor == actorCircuitBreaker {
		actor = "system"
	}
	d := store.Deployment{ID: auth.NewID("dep_"), ServiceID: serviceID, FromRev: fromRev, ToRev: toRev, Status: status,
		Message: msg, StartedAt: m.now().UTC().Truncate(time.Second), Trigger: c.Trigger, Actor: actor, BuildID: c.BuildID}
	to, err := m.SpecFor(ctx, serviceID, toRev)
	if err != nil {
		return "", err
	}
	d.Image = to.Image
	changes := []Change{}
	if fromRev > 0 {
		if from, err := m.SpecFor(ctx, serviceID, fromRev); err == nil {
			changes = Diff(from, to)
		}
	}
	b, _ := json.Marshal(changes)
	d.Changes = string(b)
	if err := m.st.StartDeployment(ctx, d); err != nil {
		return "", err
	}
	what := fmt.Sprintf("revision %d → %d", fromRev, toRev)
	if fromRev == 0 {
		what = fmt.Sprintf("first deployment, revision %d", toRev)
	}
	m.DeploymentEvent(ctx, d.ID, "started", what+" ("+c.Trigger+")")
	return d.ID, nil
}

// DeploymentEvent appends a step to a deployment's timeline.
func (m *Manager) DeploymentEvent(ctx context.Context, depID, kind, msg string) {
	if err := m.st.AddDeploymentEvent(ctx, depID, kind, msg, m.now().UTC()); err != nil {
		m.log.Warn("record deployment event", "deployment", depID, "err", err)
	}
}

func taskFailure(t store.Task) string {
	id := strings.TrimPrefix(t.ID, TaskIDPrefix)
	if len(id) > 8 {
		id = id[:8]
	}
	switch {
	case t.State == store.TaskRunning:
		return fmt.Sprintf("task %s turned unhealthy", id)
	case t.Error != "":
		return fmt.Sprintf("task %s %s: %s", id, t.State, t.Error)
	case t.State == store.TaskExited:
		return fmt.Sprintf("task %s exited with code %d", id, t.ExitCode)
	}
	return fmt.Sprintf("task %s %s", id, t.State)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// CancelDeployment stops a deployment that is running or waiting on its
// pre-deploy jobs. A running one rolls back to the revision before it.
func (m *Manager) CancelDeployment(ctx context.Context, serviceID, depID, actor string) (ServiceView, error) {
	dep, err := m.st.DeploymentByID(ctx, depID)
	if err != nil || dep.ServiceID != serviceID {
		return ServiceView{}, store.ErrNotFound
	}
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return ServiceView{}, err
	}
	now := m.now().UTC()
	switch dep.Status {
	case store.DeployWaitingHook:
		if err := m.st.SetDeploymentStatus(ctx, dep.ID, store.DeployCancelled, "cancelled before the pre-deploy jobs finished", &now); err != nil {
			return ServiceView{}, err
		}
		m.DeploymentEvent(ctx, dep.ID, store.DeployCancelled, fmt.Sprintf("cancelled; revision %d keeps running", dep.FromRev))
		if m.DeployHooks != nil {
			m.DeployHooks.CancelHooks(ctx, dep.ID)
		}
	case store.DeployInProgress:
		if dep.FromRev == 0 {
			return ServiceView{}, ErrInvalid{errors.New("the first deployment of a service has nothing to return to; scale the service to 0 or delete it")}
		}
		if err := m.st.FinishDeployment(ctx, dep.ID, store.DeployCancelled, fmt.Sprintf("cancelled; rolled back to revision %d", dep.FromRev), now); err != nil {
			return ServiceView{}, err
		}
		m.DeploymentEvent(ctx, dep.ID, store.DeployCancelled, fmt.Sprintf("cancelled; rolling back to revision %d", dep.FromRev))
		m.progress.Delete(dep.ID)
		return m.rollback(WithCause(ctx, Cause{Trigger: store.TriggerRollback}), sv, dep.FromRev, actor,
			fmt.Sprintf("cancelled deployment of revision %d", dep.ToRev), false)
	default:
		return ServiceView{}, ErrInvalid{fmt.Errorf("the deployment is %s; only a running one can be cancelled", strings.ReplaceAll(dep.Status, "_", " "))}
	}
	m.Enqueue(sv.ID)
	return m.ServiceView(ctx, sv.ID)
}

// Redeploy restarts every task with a rolling deployment of the current
// spec, as a new revision marked with the time. Pre-deploy jobs run with
// runHooks.
func (m *Manager) Redeploy(ctx context.Context, serviceID, actor string, runHooks bool) (ServiceView, error) {
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return ServiceView{}, err
	}
	if sv.Deleting {
		return ServiceView{}, ErrInvalid{errors.New("the service is being deleted")}
	}
	spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		return ServiceView{}, err
	}
	if spec.Image == AwaitingBuild {
		return ServiceView{}, ErrInvalid{errors.New("the service has no image yet: it is waiting for its first build")}
	}
	spec.RedeployedAt = m.now().UTC().Format(time.RFC3339Nano)
	ctx = WithCause(ctx, Cause{Trigger: store.TriggerRedeploy})
	if err := m.rollout(ctx, sv, spec.Canonical(), sv.DesiredCount, actor, runHooks, "redeploy"); err != nil {
		return ServiceView{}, err
	}
	m.Enqueue(sv.ID)
	return m.ServiceView(ctx, sv.ID)
}

// DeploymentDetail is a deployment with its timeline and what changed.
type DeploymentDetail struct {
	store.Deployment
	Changes []Change                `json:"changes"`
	Events  []store.DeploymentEvent `json:"events"`
}

// Detail decodes a deployment's change summary and loads its timeline.
func (m *Manager) Detail(ctx context.Context, d store.Deployment) (DeploymentDetail, error) {
	out := DeploymentDetail{Deployment: d, Changes: DecodeChanges(d.Changes)}
	ev, err := m.st.DeploymentEvents(ctx, d.ID)
	out.Events = ev
	return out, err
}

// DecodeChanges reads a stored change summary.
func DecodeChanges(raw string) []Change {
	out := []Change{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
