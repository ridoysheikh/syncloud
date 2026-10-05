package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"syncloud/internal/agent/docker"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

// execSessions runs interactive commands in task containers (kubectl exec).
type execSessions struct {
	docker *docker.Client
	log    *slog.Logger
	out    chan *agentv1.ExecOutput

	mu       sync.Mutex
	sessions map[string]*execSession
}

func newExecSessions(d *docker.Client, log *slog.Logger) *execSessions {
	return &execSessions{docker: d, log: log, out: make(chan *agentv1.ExecOutput, 256), sessions: map[string]*execSession{}}
}

type execSession struct {
	in     chan *agentv1.ExecInput // input queued until the exec is attached
	cancel context.CancelFunc
}

func (e *execSessions) handle(ctx context.Context, in *agentv1.ExecInput) {
	id := in.GetSessionId()
	if st := in.GetStart(); st != nil {
		sctx, cancel := context.WithCancel(ctx)
		s := &execSession{in: make(chan *agentv1.ExecInput, 1024), cancel: cancel}
		e.mu.Lock()
		e.sessions[id] = s
		e.mu.Unlock()
		go e.run(sctx, id, st, s)
		return
	}
	e.mu.Lock()
	s := e.sessions[id]
	e.mu.Unlock()
	if s == nil {
		return
	}
	if in.GetClose() {
		s.cancel()
		return
	}
	select {
	case s.in <- in:
	default: // a client flooding input faster than the command reads it
	}
}

func (e *execSessions) emit(o *agentv1.ExecOutput) {
	select {
	case e.out <- o:
	case <-time.After(5 * time.Second): // the stream is gone; the session ends anyway
	}
}

func (e *execSessions) run(ctx context.Context, id string, st *agentv1.ExecStart, s *execSession) {
	defer func() {
		s.cancel()
		e.mu.Lock()
		delete(e.sessions, id)
		e.mu.Unlock()
	}()
	fail := func(err error) {
		e.emit(&agentv1.ExecOutput{SessionId: id, Exited: true, ExitCode: -1, Error: err.Error()})
	}
	list, err := e.docker.List(ctx, LabelTaskID+"="+st.GetTaskId())
	if err != nil || len(list) == 0 || list[0].State != "running" {
		fail(errNotRunning)
		return
	}
	cmd := st.GetCommand()
	if len(cmd) == 0 {
		cmd = []string{"sh"}
	}
	execID, err := e.docker.ExecCreate(ctx, list[0].ID, cmd, st.GetTty())
	if err != nil {
		fail(err)
		return
	}
	conn, br, err := e.docker.ExecStart(ctx, execID, st.GetTty())
	if err != nil {
		fail(err)
		return
	}
	go func() { <-ctx.Done(); conn.Close() }()
	if st.GetTty() && st.GetCols() > 0 {
		_ = e.docker.ExecResize(ctx, execID, st.GetCols(), st.GetRows())
	}
	e.log.Info("exec started", "task", st.GetTaskId(), "command", cmd)

	go func() { // queued and new input -> command
		for {
			select {
			case <-ctx.Done():
				return
			case in := <-s.in:
				switch m := in.Msg.(type) {
				case *agentv1.ExecInput_Stdin:
					_, _ = conn.Write(m.Stdin)
				case *agentv1.ExecInput_StdinEof:
					if cw, ok := conn.(interface{ CloseWrite() error }); ok {
						_ = cw.CloseWrite()
					}
				case *agentv1.ExecInput_Resize:
					_ = e.docker.ExecResize(ctx, execID, m.Resize.GetCols(), m.Resize.GetRows())
				}
			}
		}
	}()

	buf := make([]byte, 32<<10)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if !st.GetTty() {
				data = demux(data)
			}
			e.emit(&agentv1.ExecOutput{SessionId: id, Data: data})
		}
		if err != nil {
			break
		}
	}
	code := -1
	for range 10 { // the exit code is set shortly after the stream closes
		c, done, err := e.docker.ExecExitCode(context.WithoutCancel(ctx), execID)
		if err != nil || done {
			code = c
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.emit(&agentv1.ExecOutput{SessionId: id, Exited: true, ExitCode: int32(code)})
}

// demux strips the 8-byte stream headers of non-TTY output. Frames split
// across reads are rare for interactive use; partial headers pass through.
func demux(b []byte) []byte {
	var out []byte
	for len(b) >= 8 && (b[0] == 1 || b[0] == 2) && b[1] == 0 && b[2] == 0 && b[3] == 0 {
		n := int(b[4])<<24 | int(b[5])<<16 | int(b[6])<<8 | int(b[7])
		if 8+n > len(b) {
			break
		}
		out = append(out, b[8:8+n]...)
		b = b[8+n:]
	}
	return append(out, b...)
}

type agentError string

func (e agentError) Error() string { return string(e) }

const errNotRunning = agentError("the task is not running on this node")
