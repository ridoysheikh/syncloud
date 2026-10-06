// Package rollout upgrades agents node by node (§5.0.1 step 6, §6.2): each
// node gets the agent binary from the controller's downloads over its agent
// stream, restarts into it, and must reconnect with the new version before
// the next node starts. A failure stops the rollout.
package rollout

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"syncloud/internal/agentgw"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/nodes"
	"syncloud/internal/store"
)

const chunkSize = 1 << 20

// ReconnectTimeout is how long a node has to come back with the new version
// (longer than the agent's own rollback guard).
var ReconnectTimeout = 3 * time.Minute

// confirmAfter is how long a reconnected agent has to report a rollback
// before its upgrade counts.
const confirmAfter = 5 * time.Second

type NodeStatus struct {
	NodeID     string     `json:"nodeId"`
	Node       string     `json:"node"`
	From       string     `json:"from"`
	State      string     `json:"state"` // pending | sending | restarting | done | failed | skipped
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type Rollout struct {
	Target     string       `json:"target"`
	State      string       `json:"state"` // running | done | failed
	Message    string       `json:"message,omitempty"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt *time.Time   `json:"finishedAt,omitempty"`
	Nodes      []NodeStatus `json:"nodes"`
}

type Manager struct {
	gw        *agentgw.Gateway
	reg       *nodes.Registry
	downloads string
	target    string
	log       *slog.Logger

	mu      sync.Mutex
	cur     *Rollout
	waits map[string]*wait
}

// wait is a node being upgraded: the agent's failure report, or the version
// it reconnected with.
type wait struct {
	failed    chan string
	connected chan string
}

// New returns a manager rolling agents out to target (the controller's own
// version) from the binaries in downloads.
func New(gw *agentgw.Gateway, reg *nodes.Registry, downloads, target string, log *slog.Logger) *Manager {
	return &Manager{gw: gw, reg: reg, downloads: downloads, target: target, log: log, waits: map[string]*wait{}}
}

func (m *Manager) Target() string { return m.target }

// Hooks deliver agents' upgrade failures to the waiting rollout.
func (m *Manager) Hooks() agentgw.Hooks {
	get := func(id string) *wait {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.waits[id]
	}
	return agentgw.Hooks{
		OnUpgradeResult: func(node store.Node, r *agentv1.UpgradeResult) {
			m.log.Warn("agent upgrade failed", "node", node.Name, "version", r.GetVersion(), "err", r.GetError())
			if w := get(node.ID); w != nil {
				select {
				case w.failed <- r.GetError():
				default:
				}
			}
		},
		OnConnect: func(c agentgw.Conn) {
			if w := get(c.Node.ID); w != nil {
				select {
				case w.connected <- c.Hello.GetAgentVersion():
				default:
				}
			}
		},
	}
}

// Status is the current or last rollout (nil if none ran).
func (m *Manager) Status() *Rollout {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	c := *m.cur
	c.Nodes = append([]NodeStatus(nil), m.cur.Nodes...)
	return &c
}

// ErrRunning is returned while a rollout is in progress.
var ErrRunning = errors.New("an agent rollout is already running")

// Start upgrades the given nodes (all when empty) that run another version;
// force also re-sends to nodes already at the target.
func (m *Manager) Start(ids []string, force bool) (*Rollout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && m.cur.State == "running" {
		return nil, ErrRunning
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	r := &Rollout{Target: m.target, State: "running", StartedAt: time.Now().UTC()}
	found := 0
	for _, v := range m.reg.List() {
		if len(want) > 0 && !want[v.ID] && !want[v.Name] {
			continue
		}
		found++
		ns := NodeStatus{NodeID: v.ID, Node: v.Name, From: v.Info.AgentVersion, State: "pending"}
		switch {
		case !v.Connected:
			ns.State, ns.Error = "skipped", "not connected"
		case v.Info.AgentVersion == m.target && !force:
			ns.State = "skipped"
			ns.Error = "already at " + m.target
		}
		r.Nodes = append(r.Nodes, ns)
	}
	if len(want) > 0 && found < len(want) {
		return nil, fmt.Errorf("unknown node in %v", ids)
	}
	m.cur = r
	go m.run(r)
	c := *r
	return &c, nil
}

func (m *Manager) set(r *Rollout, i int, f func(*NodeStatus)) {
	m.mu.Lock()
	f(&r.Nodes[i])
	m.mu.Unlock()
}

func (m *Manager) run(r *Rollout) {
	var failure error
	for i := range r.Nodes {
		if r.Nodes[i].State != "pending" {
			continue
		}
		if failure != nil {
			m.set(r, i, func(n *NodeStatus) { n.State, n.Error = "skipped", "rollout stopped" })
			continue
		}
		now := time.Now().UTC()
		m.set(r, i, func(n *NodeStatus) { n.State, n.StartedAt = "sending", &now })
		err := m.upgradeNode(r, i)
		end := time.Now().UTC()
		m.set(r, i, func(n *NodeStatus) {
			n.FinishedAt = &end
			if err != nil {
				n.State, n.Error = "failed", err.Error()
			} else {
				n.State = "done"
			}
		})
		if err != nil {
			failure = fmt.Errorf("%s: %w", r.Nodes[i].Node, err)
		}
	}
	end := time.Now().UTC()
	m.mu.Lock()
	r.FinishedAt = &end
	if failure != nil {
		r.State, r.Message = "failed", failure.Error()
	} else {
		r.State = "done"
	}
	m.mu.Unlock()
	m.log.Info("agent rollout finished", "state", r.State, "message", r.Message)
}

func (m *Manager) upgradeNode(r *Rollout, i int) error {
	n := r.Nodes[i]
	v, ok := m.reg.Get(n.NodeID)
	if !ok || !v.Connected {
		return errors.New("not connected")
	}
	arch := v.Info.Arch
	if arch == "" {
		arch = "amd64"
	}
	path := filepath.Join(m.downloads, "syncloud-agent-linux-"+arch)
	bin, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("agent binary for %s: %w", arch, err)
	}
	sum := sha256.Sum256(bin)
	w := &wait{failed: make(chan string, 1), connected: make(chan string, 4)}
	m.mu.Lock()
	m.waits[n.NodeID] = w
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.waits, n.NodeID)
		m.mu.Unlock()
	}()
	for off := 0; off < len(bin) || off == 0; off += chunkSize {
		end := min(off+chunkSize, len(bin))
		msg := &agentv1.UpgradeAgent{Version: r.Target, Sha256: hex.EncodeToString(sum[:]), Size: int64(len(bin)),
			Offset: int64(off), Data: bin[off:end], Last: end == len(bin)}
		if err := m.gw.Send(n.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_UpgradeAgent{UpgradeAgent: msg}}); err != nil {
			return fmt.Errorf("send binary: %w", err)
		}
		if end == len(bin) {
			break
		}
	}
	m.set(r, i, func(s *NodeStatus) { s.State = "restarting" })
	deadline := time.After(ReconnectTimeout)
	for {
		select {
		case msg := <-w.failed:
			return errors.New(msg)
		case got := <-w.connected:
			if got == r.Target {
				// A restored agent reports a rollback right after connecting
				// (with force, it may even run the target version).
				select {
				case msg := <-w.failed:
					return errors.New(msg)
				case <-time.After(confirmAfter):
					return nil
				}
			}
			// The old agent is back: wait for its report of what failed.
			select {
			case msg := <-w.failed:
				return errors.New(msg)
			case <-time.After(10 * time.Second):
				return fmt.Errorf("reconnected with %s instead of %s", got, r.Target)
			}
		case <-deadline:
			return fmt.Errorf("did not reconnect with %s within %s", r.Target, ReconnectTimeout)
		}
	}
}
