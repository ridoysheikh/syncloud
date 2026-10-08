package system

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/agentgw"
	"github.com/ridoysheikh/syncloud/internal/events"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// LocalNode is the controller's own node, which runs system tasks (§6.4).
const LocalNode = "ctl-0"

// TopicTask carries a TaskView whenever a system task changes.
const TopicTask = "system.task"

// resyncEvery re-sends every spec so drift (a container removed by hand,
// a spec change after upgrade) is corrected. Agents ignore unchanged specs.
const resyncEvery = time.Minute

type TaskView struct {
	TaskID      string     `json:"taskId"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Image       string     `json:"image"`
	Node        string     `json:"node"`
	State       string     `json:"state"` // pending, pulling, starting, running, exited, failed, removed
	Health      string     `json:"health"`
	Error       string     `json:"error"`
	ContainerID string     `json:"containerId"`
	StartedAt   *time.Time `json:"startedAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type Manager struct {
	gw    *agentgw.Gateway
	bus   *events.Bus
	log   *slog.Logger
	specs []*agentv1.TaskSpec
	cfg   Config

	mu     sync.Mutex
	nodeID string // ctl-0 when connected
	tasks  map[string]*TaskView
}

func NewManager(gw *agentgw.Gateway, bus *events.Bus, log *slog.Logger, cfg Config) *Manager {
	m := &Manager{gw: gw, bus: bus, log: log, specs: Specs(cfg), cfg: cfg, tasks: map[string]*TaskView{}}
	for _, c := range Components {
		m.tasks[c.TaskID] = &TaskView{TaskID: c.TaskID, Name: c.Name, Description: c.Description, Node: LocalNode, State: "pending"}
	}
	for _, s := range m.specs {
		m.tasks[s.TaskId].Image = s.Image
	}
	return m
}

// Hooks returns the gateway hooks the manager needs.
func (m *Manager) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnConnect: m.onConnect, OnTaskStatus: m.onTaskStatus}
}

func (m *Manager) onConnect(c agentgw.Conn) {
	node, snapshot := c.Node, c.Hello.GetTasks()
	if node.Name != LocalNode {
		return
	}
	m.mu.Lock()
	m.nodeID = node.ID
	m.mu.Unlock()

	wanted := map[string]bool{}
	for _, s := range m.currentSpecs() {
		wanted[s.TaskId] = true
	}
	for _, s := range snapshot {
		if wanted[s.TaskId] {
			m.onTaskStatus(node, s)
		} else if strings.HasPrefix(s.TaskId, TaskIDPrefix) {
			// A component dropped from this release: remove it.
			m.log.Info("removing obsolete system task", "task", s.TaskId)
			m.send(node.ID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
				TaskId: s.TaskId, TimeoutSeconds: 15, Remove: true,
			}}})
		}
	}
	m.applyAll(node.ID)
}

func (m *Manager) onTaskStatus(node store.Node, s *agentv1.TaskStatus) {
	if node.Name != LocalNode {
		return
	}
	m.mu.Lock()
	t, ok := m.tasks[s.TaskId]
	if !ok {
		m.mu.Unlock()
		return
	}
	t.State = stateName(s.State)
	t.Health = s.Health
	t.Error = s.Error
	t.ContainerID = s.ContainerId
	if s.Image != "" {
		t.Image = s.Image
	}
	t.StartedAt = nil
	if s.StartedAtUnix > 0 {
		at := time.Unix(s.StartedAtUnix, 0).UTC()
		t.StartedAt = &at
	}
	t.UpdatedAt = time.Now().UTC()
	v := *t
	m.mu.Unlock()
	m.bus.Publish(TopicTask, v)
}

// SetConfig re-renders the specs (e.g. after a base domain change) and applies
// them; agents recreate only the containers whose spec changed.
func (m *Manager) SetConfig(cfg Config) {
	m.mu.Lock()
	cfg.RegistryReadOnly = m.cfg.RegistryReadOnly // owned by SetRegistryReadOnly
	specs := Specs(cfg)
	was := map[string]bool{}
	for _, s := range m.specs {
		was[s.TaskId] = true
	}
	on := map[string]bool{}
	for _, s := range specs {
		on[s.TaskId] = true
		if t := m.tasks[s.TaskId]; t != nil {
			t.Image = s.Image
			if !was[s.TaskId] {
				t.State, t.Error = "pending", "" // just turned on
			}
		}
	}
	var off []string // components turned off: stop and remove them
	for _, s := range m.specs {
		if !on[s.TaskId] {
			off = append(off, s.TaskId)
			if t := m.tasks[s.TaskId]; t != nil {
				t.State = "removed"
			}
		}
	}
	m.cfg = cfg
	m.specs = specs
	id := m.nodeID
	m.mu.Unlock()
	if id != "" {
		for _, t := range off {
			m.send(id, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{TaskId: t, TimeoutSeconds: 15, Remove: true}}})
		}
		m.applyAll(id)
	}
}

func (m *Manager) currentSpecs() []*agentv1.TaskSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.specs
}

func (m *Manager) applyAll(nodeID string) {
	for _, s := range m.currentSpecs() {
		m.send(nodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: s}}})
	}
}

func (m *Manager) send(nodeID string, msg *agentv1.ConnectResponse) {
	if err := m.gw.Send(nodeID, msg); err != nil {
		m.log.Warn("send to node", "node", nodeID, "err", err)
	}
}

// Run periodically re-applies all system tasks until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(resyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.mu.Lock()
			id := m.nodeID
			m.mu.Unlock()
			if id != "" {
				m.applyAll(id)
			}
		}
	}
}

// List returns system tasks in manifest order.
func (m *Manager) List() []TaskView {
	m.mu.Lock()
	defer m.mu.Unlock()
	on := map[string]bool{}
	for _, s := range m.specs {
		on[s.TaskId] = true
	}
	out := make([]TaskView, 0, len(Components))
	for _, c := range Components {
		if on[c.TaskID] { // optional components that are off are not listed
			out = append(out, *m.tasks[c.TaskID])
		}
	}
	return out
}

func stateName(s agentv1.TaskState) string {
	switch s {
	case agentv1.TaskState_TASK_STATE_PULLING:
		return "pulling"
	case agentv1.TaskState_TASK_STATE_STARTING:
		return "starting"
	case agentv1.TaskState_TASK_STATE_RUNNING:
		return "running"
	case agentv1.TaskState_TASK_STATE_EXITED:
		return "exited"
	case agentv1.TaskState_TASK_STATE_FAILED:
		return "failed"
	case agentv1.TaskState_TASK_STATE_REMOVED:
		return "removed"
	}
	return "pending"
}

// SetRegistryReadOnly switches the registry in or out of read-only
// maintenance mode (the container is recreated).
func (m *Manager) SetRegistryReadOnly(ro bool) {
	m.mu.Lock()
	m.cfg.RegistryReadOnly = ro
	m.specs = Specs(m.cfg)
	id := m.nodeID
	m.mu.Unlock()
	if id != "" {
		m.applyAll(id)
	}
}

// NodeID is the controller node's ID ("" until it connects).
func (m *Manager) NodeID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nodeID
}

// Task returns one system task's current view.
func (m *Manager) Task(id string) (TaskView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return TaskView{}, false
	}
	return *t, true
}
