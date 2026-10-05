// Package execrelay connects interactive exec clients (dashboard terminal,
// synctl exec) to sessions on agents, multiplexed on the agent stream.
package execrelay

import (
	"errors"
	"sync"

	"syncloud/internal/agentgw"
	"syncloud/internal/auth"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/store"
)

type Relay struct {
	gw *agentgw.Gateway

	mu       sync.Mutex
	sessions map[string]*Session
}

func New(gw *agentgw.Gateway) *Relay { return &Relay{gw: gw, sessions: map[string]*Session{}} }

func (r *Relay) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnExecOutput: r.dispatch, OnDisconnect: r.nodeGone}
}

// Session is one exec on one node.
type Session struct {
	ID     string
	nodeID string
	relay  *Relay
	// Out receives output; it is closed after the exit message.
	Out  chan *agentv1.ExecOutput
	once sync.Once
}

// ErrNodeOffline means the task's node is not connected.
var ErrNodeOffline = errors.New("the task's node is not connected")

// Open starts command in the task's container.
func (r *Relay) Open(nodeID, taskID string, command []string, tty bool, cols, rows uint32) (*Session, error) {
	s := &Session{ID: auth.NewID("exec_"), nodeID: nodeID, relay: r, Out: make(chan *agentv1.ExecOutput, 256)}
	r.mu.Lock()
	r.sessions[s.ID] = s
	r.mu.Unlock()
	err := s.send(&agentv1.ExecInput{Msg: &agentv1.ExecInput_Start{Start: &agentv1.ExecStart{TaskId: taskID, Command: command, Tty: tty, Cols: cols, Rows: rows}}})
	if err != nil {
		r.remove(s.ID)
		if errors.Is(err, agentgw.ErrNotConnected) {
			return nil, ErrNodeOffline
		}
		return nil, err
	}
	return s, nil
}

func (s *Session) send(in *agentv1.ExecInput) error {
	in.SessionId = s.ID
	return s.relay.gw.Send(s.nodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecInput{ExecInput: in}})
}

func (s *Session) Stdin(b []byte) error {
	return s.send(&agentv1.ExecInput{Msg: &agentv1.ExecInput_Stdin{Stdin: b}})
}

// CloseStdin signals end of input to the command.
func (s *Session) CloseStdin() error {
	return s.send(&agentv1.ExecInput{Msg: &agentv1.ExecInput_StdinEof{StdinEof: true}})
}

func (s *Session) Resize(cols, rows uint32) error {
	return s.send(&agentv1.ExecInput{Msg: &agentv1.ExecInput_Resize{Resize: &agentv1.ExecResize{Cols: cols, Rows: rows}}})
}

// Close ends the session on the node.
func (s *Session) Close() {
	s.once.Do(func() {
		_ = s.send(&agentv1.ExecInput{Msg: &agentv1.ExecInput_Close{Close: true}})
		s.relay.remove(s.ID)
	})
}

// Sends to and closing of Out happen under r.mu, so they never race.

func (r *Relay) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(id)
}

func (r *Relay) removeLocked(id string) {
	if s := r.sessions[id]; s != nil {
		delete(r.sessions, id)
		close(s.Out)
	}
}

func (r *Relay) dispatch(node store.Node, o *agentv1.ExecOutput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[o.GetSessionId()]
	if s == nil || s.nodeID != node.ID {
		return
	}
	select {
	case s.Out <- o:
	default: // the client is not reading; drop output rather than block the agent stream
	}
	if o.GetExited() {
		r.removeLocked(s.ID)
	}
}

func (r *Relay) nodeGone(node store.Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.sessions {
		if s.nodeID != node.ID {
			continue
		}
		select {
		case s.Out <- &agentv1.ExecOutput{SessionId: s.ID, Exited: true, ExitCode: -1, Error: "the node disconnected"}:
		default:
		}
		r.removeLocked(id)
	}
}
