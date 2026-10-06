package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"syncloud/internal/agent/docker"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

// Container labels set on every SynCloud-managed container.
const (
	LabelManaged  = "syncloud.managed"
	LabelTaskID   = "syncloud.task_id"
	LabelSpecHash = "syncloud.spec_hash"
	LabelSystem   = "syncloud.system"
)

// Runner applies task specs to the local Docker Engine (§6.2 Executor) and
// reports state changes. Operations on the same task are serialized.
type Runner struct {
	docker *docker.Client
	log    *slog.Logger
	// Status updates for the controller; dropped when no session is listening.
	out chan *agentv1.TaskStatus
	// NetworkReady reports whether tasks may join TaskNetwork yet (nil: always).
	NetworkReady func() error
	// prePulls holds images being pulled ahead of deployments.
	prePulls sync.Map

	mu     sync.Mutex
	locks  map[string]*sync.Mutex
	probes map[string]*prober // task ID -> health probe
	ctx    context.Context    // for probes started from Run
}

// TaskNetwork is the Docker network service tasks join (§8).
const TaskNetwork = "syncloud"

func NewRunner(d *docker.Client, log *slog.Logger) *Runner {
	return &Runner{docker: d, log: log, out: make(chan *agentv1.TaskStatus, 256), locks: map[string]*sync.Mutex{},
		probes: map[string]*prober{}, ctx: context.Background()}
}

func (r *Runner) lock(taskID string) func() {
	r.mu.Lock()
	l, ok := r.locks[taskID]
	if !ok {
		l = &sync.Mutex{}
		r.locks[taskID] = l
	}
	r.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (r *Runner) emit(s *agentv1.TaskStatus) {
	select {
	case r.out <- s:
	default:
		r.log.Warn("task status dropped (controller not reading)", "task", s.TaskId)
	}
}

// SpecHash identifies a spec's content; a changed hash means "recreate".
func SpecHash(spec *agentv1.TaskSpec) string {
	if spec.GetRegistryAuth() != "" { // credentials rotate; the container does not change
		spec = proto.Clone(spec).(*agentv1.TaskSpec)
		spec.RegistryAuth = ""
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(spec)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Run makes the task's container match spec: unchanged and running → nothing;
// unchanged and stopped → start; changed or missing → (re)create.
func (r *Runner) Run(ctx context.Context, spec *agentv1.TaskSpec) {
	defer r.lock(spec.TaskId)()
	hash := SpecHash(spec)
	status := &agentv1.TaskStatus{TaskId: spec.TaskId, Image: spec.Image, SpecHash: hash}
	fail := func(err error) {
		r.log.Error("task failed", "task", spec.TaskId, "err", err)
		status.State, status.Error = agentv1.TaskState_TASK_STATE_FAILED, err.Error()
		r.emit(status)
	}

	existing, err := r.docker.List(ctx, LabelTaskID+"="+spec.TaskId)
	if err != nil {
		fail(err)
		return
	}
	for _, c := range existing {
		if c.Labels[LabelSpecHash] == hash {
			if c.State != "running" {
				if err := r.docker.Start(ctx, c.ID); err != nil {
					fail(err)
					return
				}
			}
			r.startProbe(spec, c.ID, hash)
			r.emit(r.inspect(ctx, c.ID, spec.TaskId))
			return
		}
	}
	// Spec changed (or no container yet): replace.
	for _, c := range existing {
		r.log.Info("replacing container", "task", spec.TaskId, "container", c.ID[:12])
		if err := r.docker.Stop(ctx, c.ID, 15*time.Second); err != nil {
			fail(err)
			return
		}
		if err := r.docker.Remove(ctx, c.ID); err != nil {
			fail(err)
			return
		}
	}

	if ok, err := r.docker.ImageExists(ctx, spec.Image); err != nil {
		fail(err)
		return
	} else if !ok {
		status.State = agentv1.TaskState_TASK_STATE_PULLING
		r.emit(proto.Clone(status).(*agentv1.TaskStatus))
		r.log.Info("pulling image", "image", spec.Image)
		if err := r.docker.Pull(ctx, spec.Image, spec.GetRegistryAuth()); err != nil {
			fail(err)
			return
		}
	}

	mode := spec.NetworkMode
	if mode == TaskNetwork && r.NetworkReady != nil {
		// On mesh nodes the agent creates this network with the node's subnet.
		if err := r.NetworkReady(); err != nil {
			fail(err)
			return
		}
	}
	if mode != "" && mode != "host" && mode != "bridge" && mode != "none" {
		if err := r.docker.EnsureNetwork(ctx, mode, map[string]string{LabelManaged: "true"}); err != nil {
			fail(err)
			return
		}
	}
	status.State = agentv1.TaskState_TASK_STATE_STARTING
	r.emit(proto.Clone(status).(*agentv1.TaskStatus))

	id, err := r.docker.Create(ctx, spec.Name, createRequest(spec, hash))
	if err != nil {
		fail(err)
		return
	}
	if err := r.docker.Start(ctx, id); err != nil {
		_ = r.docker.Remove(ctx, id)
		fail(err)
		return
	}
	r.log.Info("task started", "task", spec.TaskId, "container", id[:12])
	r.startProbe(spec, id, hash)
	r.emit(r.inspect(ctx, id, spec.TaskId))
}

// Stop stops (and optionally removes) the task's containers.
func (r *Runner) Stop(ctx context.Context, taskID string, timeout time.Duration, remove bool) {
	defer r.lock(taskID)()
	r.stopProbe(taskID)
	existing, err := r.docker.List(ctx, LabelTaskID+"="+taskID)
	if err != nil {
		r.emit(&agentv1.TaskStatus{TaskId: taskID, State: agentv1.TaskState_TASK_STATE_FAILED, Error: err.Error()})
		return
	}
	for _, c := range existing {
		if err := r.docker.Stop(ctx, c.ID, timeout); err != nil {
			r.log.Warn("stop", "task", taskID, "err", err)
		}
		if remove {
			if err := r.docker.Remove(ctx, c.ID); err != nil {
				r.log.Warn("remove", "task", taskID, "err", err)
			}
		}
	}
	if remove || len(existing) == 0 {
		r.emit(&agentv1.TaskStatus{TaskId: taskID, State: agentv1.TaskState_TASK_STATE_REMOVED})
	}
}

// Snapshot reports every managed container, sent in Hello after (re)connecting.
func (r *Runner) Snapshot(ctx context.Context) []*agentv1.TaskStatus {
	list, err := r.docker.List(ctx, LabelManaged+"=true")
	if err != nil {
		r.log.Warn("list containers", "err", err)
		return nil
	}
	out := make([]*agentv1.TaskStatus, 0, len(list))
	for _, c := range list {
		if id := c.Labels[LabelTaskID]; id != "" {
			out = append(out, r.inspect(ctx, c.ID, id))
		}
	}
	return out
}

func (r *Runner) inspect(ctx context.Context, containerID, taskID string) *agentv1.TaskStatus {
	s := &agentv1.TaskStatus{TaskId: taskID, ContainerId: containerID}
	c, err := r.docker.Inspect(ctx, containerID)
	if docker.IsNotFound(err) {
		s.State = agentv1.TaskState_TASK_STATE_REMOVED
		return s
	} else if err != nil {
		s.State, s.Error = agentv1.TaskState_TASK_STATE_FAILED, err.Error()
		return s
	}
	s.Image = c.Config.Image
	s.SpecHash = c.Config.Labels[LabelSpecHash]
	if n, ok := c.NetworkSettings.Networks[TaskNetwork]; ok {
		s.Ip = n.IPAddress
	} else {
		for _, n := range c.NetworkSettings.Networks {
			s.Ip = n.IPAddress
		}
	}
	s.ExitCode = int32(c.State.ExitCode)
	s.Error = c.State.Error
	if c.State.Health != nil {
		s.Health = c.State.Health.Status
	}
	if h := r.probeHealth(taskID); h != "" && c.State.Running {
		s.Health = h // the agent's own probe (§5.6) wins over a Docker HEALTHCHECK
	}
	if t, err := time.Parse(time.RFC3339Nano, c.State.StartedAt); err == nil && !t.IsZero() {
		s.StartedAtUnix = t.Unix()
	}
	switch c.State.Status {
	case "running":
		s.State = agentv1.TaskState_TASK_STATE_RUNNING
	case "created", "restarting":
		s.State = agentv1.TaskState_TASK_STATE_STARTING
	default: // exited, dead, paused
		s.State = agentv1.TaskState_TASK_STATE_EXITED
	}
	return s
}

// Watch reports container state changes from Docker events until ctx ends,
// reconnecting to the event stream if it drops.
func (r *Runner) Watch(ctx context.Context) {
	r.mu.Lock()
	r.ctx = ctx // probes live as long as the agent, not one controller session
	r.mu.Unlock()
	for ctx.Err() == nil {
		evc, errc := r.docker.Events(ctx, LabelManaged+"=true")
		for e := range evc {
			taskID := e.Actor.Attributes[LabelTaskID]
			if taskID == "" {
				continue
			}
			switch {
			case e.Action == "start", e.Action == "die", e.Action == "destroy",
				strings.HasPrefix(e.Action, "health_status"):
				r.emit(r.inspect(ctx, e.Actor.ID, taskID))
			}
		}
		if err := <-errc; ctx.Err() == nil {
			r.log.Warn("docker events stream ended; retrying", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func createRequest(spec *agentv1.TaskSpec, hash string) docker.CreateRequest {
	labels := map[string]string{
		LabelManaged:  "true",
		LabelTaskID:   spec.TaskId,
		LabelSpecHash: hash,
	}
	if spec.System {
		labels[LabelSystem] = "true"
	}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)

	req := docker.CreateRequest{
		Image:      spec.Image,
		Entrypoint: spec.Entrypoint,
		Cmd:        spec.Command,
		Env:        env,
		Labels:     labels,
		HostConfig: docker.HostConfig{
			NetworkMode:   spec.NetworkMode,
			RestartPolicy: docker.RestartPolicy{Name: restartName(spec.Restart)},
			Memory:        spec.MemoryLimitBytes,
			NanoCPUs:      spec.NanoCpus,
			ExtraHosts:    spec.ExtraHosts,
			DNS:           spec.DnsServers,
			Privileged:    spec.Privileged,
			DNSSearch:     spec.DnsSearch,
			// Bounded local logs until centralized logging ships (§9.2).
			LogConfig: docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
		},
	}
	for _, p := range spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		key := fmt.Sprintf("%d/%s", p.ContainerPort, proto)
		if req.ExposedPorts == nil {
			req.ExposedPorts = map[string]struct{}{}
			req.HostConfig.PortBindings = map[string][]docker.PortBinding{}
		}
		req.ExposedPorts[key] = struct{}{}
		req.HostConfig.PortBindings[key] = append(req.HostConfig.PortBindings[key],
			docker.PortBinding{HostIP: p.HostIp, HostPort: fmt.Sprint(p.HostPort)})
	}
	for _, m := range spec.Mounts {
		t := "volume"
		if m.Type == agentv1.Mount_TYPE_BIND {
			t = "bind"
		}
		req.HostConfig.Mounts = append(req.HostConfig.Mounts, docker.Mount{Type: t, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	return req
}

func restartName(p agentv1.RestartPolicy) string {
	switch p {
	case agentv1.RestartPolicy_RESTART_POLICY_NO:
		return "no"
	case agentv1.RestartPolicy_RESTART_POLICY_ALWAYS:
		return "always"
	case agentv1.RestartPolicy_RESTART_POLICY_ON_FAILURE:
		return "on-failure"
	default:
		return "unless-stopped"
	}
}

// PrePull downloads an image ahead of a deployment unless it is already
// present or being pulled; failures are only logged (the task's own pull
// reports them).
func (r *Runner) PrePull(ctx context.Context, image, registryAuth string) {
	if _, busy := r.prePulls.LoadOrStore(image, true); busy {
		return
	}
	defer r.prePulls.Delete(image)
	if ok, err := r.docker.ImageExists(ctx, image); err != nil || ok {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	start := time.Now()
	if err := r.docker.Pull(pctx, image, registryAuth); err != nil {
		r.log.Warn("pre-pull failed", "image", image, "err", err)
		return
	}
	r.log.Info("pre-pulled image for a deployment", "image", image, "took", time.Since(start).Round(time.Millisecond))
}
