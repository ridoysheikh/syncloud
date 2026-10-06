// Package shell runs Cloud Shell (§7.1): a short-lived container per user on
// the controller node, with synctl authenticated as that user through
// temporary credentials. The dashboard reaches it through exec.
package shell

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"syncloud/internal/agentgw"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/store"
)

// TaskPrefix starts shell container task IDs.
const TaskPrefix = "shell-"

// Config of the shell containers.
type Config struct {
	Image string // with sh; curl, jq and git are welcome (default alpine)
	// Synctl is the synctl binary on the controller host, mounted into the
	// shell ("" leaves it out).
	Synctl string
	// Endpoint is the API address as seen from the shell container.
	Endpoint func() string
	// IdleTimeout stops shells nobody used for this long.
	IdleTimeout time.Duration
	// Node returns the node to run shells on (the controller node).
	Node func() (store.Node, bool)
}

// Credentials are what the shell signs requests with.
type Credentials struct {
	AccessKeyID, Secret, SessionToken string
}

// Session is one user's shell.
type Session struct {
	UserID     string    `json:"-"`
	TaskID     string    `json:"taskId"`
	NodeID     string    `json:"-"`
	State      string    `json:"state"` // starting | running | failed
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	LastActive time.Time `json:"lastActive"`
	ExpiresAt  time.Time `json:"expiresAt"` // its credentials
}

type Manager struct {
	gw  *agentgw.Gateway
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]*Session // by user ID
}

func New(gw *agentgw.Gateway, cfg Config, log *slog.Logger) *Manager {
	if cfg.Image == "" {
		cfg.Image = "alpine:3.22"
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	return &Manager{gw: gw, cfg: cfg, log: log, now: time.Now, sessions: map[string]*Session{}}
}

// Hooks follow the shell containers' state, and remove shells left over
// from before a controller restart.
func (m *Manager) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnConnect: func(c agentgw.Conn) {
		for _, t := range c.Hello.GetTasks() {
			if !strings.HasPrefix(t.GetTaskId(), TaskPrefix) {
				continue
			}
			m.mu.Lock()
			live := false
			for _, s := range m.sessions {
				live = live || s.TaskID == t.GetTaskId()
			}
			m.mu.Unlock()
			if !live {
				_ = m.gw.Send(c.Node.ID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{TaskId: t.GetTaskId(), TimeoutSeconds: 1, Remove: true}}})
			}
		}
	}, OnTaskStatus: func(_ store.Node, s *agentv1.TaskStatus) {
		if !strings.HasPrefix(s.GetTaskId(), TaskPrefix) {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, sess := range m.sessions {
			if sess.TaskID != s.GetTaskId() {
				continue
			}
			switch s.GetState() {
			case agentv1.TaskState_TASK_STATE_RUNNING:
				sess.State, sess.Error = "running", ""
			case agentv1.TaskState_TASK_STATE_FAILED, agentv1.TaskState_TASK_STATE_EXITED:
				sess.State, sess.Error = "failed", s.GetError()
			}
		}
	}}
}

func taskID(userID string) string {
	return TaskPrefix + strings.ReplaceAll(strings.ToLower(userID), "_", "-")
}

// ErrUnavailable means there is no node to run shells on.
var ErrUnavailable = errors.New("Cloud Shell needs the controller node to be connected")

// Start runs (or returns) a user's shell. creds are fresh temporary
// credentials, used when a new container starts.
func (m *Manager) Start(ctx context.Context, userID string, creds func() (Credentials, time.Time, error)) (Session, error) {
	m.mu.Lock()
	if s := m.sessions[userID]; s != nil && s.State != "failed" && m.now().Before(s.ExpiresAt) {
		s.LastActive = m.now()
		cp := *s
		m.mu.Unlock()
		return cp, nil
	}
	m.mu.Unlock()
	node, ok := m.cfg.Node()
	if !ok {
		return Session{}, ErrUnavailable
	}
	c, exp, err := creds()
	if err != nil {
		return Session{}, err
	}
	id := taskID(userID)
	spec := &agentv1.TaskSpec{
		TaskId: id, Name: "syncloud-" + id, Image: m.cfg.Image, Command: []string{"sleep", "2147483647"},
		Env: map[string]string{
			"SYNCLOUD_ENDPOINT": m.cfg.Endpoint(), "SYNCLOUD_ACCESS_KEY_ID": c.AccessKeyID, "SYNCLOUD_SECRET_ACCESS_KEY": c.Secret,
			"SYNCLOUD_SESSION_TOKEN": c.SessionToken, "HOME": "/root", "PS1": `\w $ `, "TERM": "xterm-256color",
		},
		Labels:           map[string]string{"syncloud.shell_user": userID},
		NetworkMode:      "syncloud",
		MemoryLimitBytes: 256 << 20,
		NanoCpus:         500_000_000,
		Mounts:           []*agentv1.Mount{{Type: agentv1.Mount_TYPE_VOLUME, Source: "syncloud-" + id + "-home", Target: "/root"}},
	}
	if m.cfg.Synctl != "" {
		spec.Mounts = append(spec.Mounts, &agentv1.Mount{Type: agentv1.Mount_TYPE_BIND, Source: m.cfg.Synctl, Target: "/usr/local/bin/synctl", ReadOnly: true})
	}
	if err := m.gw.Send(node.ID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: spec}}}); err != nil {
		return Session{}, ErrUnavailable
	}
	now := m.now()
	s := &Session{UserID: userID, TaskID: id, NodeID: node.ID, State: "starting", StartedAt: now, LastActive: now, ExpiresAt: exp}
	m.mu.Lock()
	m.sessions[userID] = s
	cp := *s
	m.mu.Unlock()
	m.log.Info("cloud shell started", "user", userID, "node", node.Name)
	return cp, nil
}

// Get returns a user's shell.
func (m *Manager) Get(userID string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[userID]
	if s == nil {
		return Session{}, false
	}
	return *s, true
}

// Touch records activity (a terminal opened).
func (m *Manager) Touch(userID string) {
	m.mu.Lock()
	if s := m.sessions[userID]; s != nil {
		s.LastActive = m.now()
	}
	m.mu.Unlock()
}

// Stop removes a user's shell container (its home volume stays).
func (m *Manager) Stop(userID string) {
	m.mu.Lock()
	s := m.sessions[userID]
	delete(m.sessions, userID)
	m.mu.Unlock()
	if s == nil {
		return
	}
	_ = m.gw.Send(s.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{TaskId: s.TaskID, TimeoutSeconds: 2, Remove: true}}})
	m.log.Info("cloud shell stopped", "user", userID)
}

// Run stops idle and expired shells until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := m.now()
		var stale []string
		m.mu.Lock()
		for u, s := range m.sessions {
			if now.Sub(s.LastActive) > m.cfg.IdleTimeout || now.After(s.ExpiresAt) {
				stale = append(stale, u)
			}
		}
		m.mu.Unlock()
		for _, u := range stale {
			m.Stop(u)
		}
	}
}
