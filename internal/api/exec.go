package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"

	"syncloud/internal/execrelay"
	"syncloud/internal/store"
)

// execControl is a JSON text message on the exec WebSocket.
type execControl struct {
	Type    string `json:"type"` // resize, eof (client) | exit, error (server)
	Cols    uint32 `json:"cols,omitempty"`
	Rows    uint32 `json:"rows,omitempty"`
	Code    int32  `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// handleExec opens an interactive command in a task (kubectl exec). Binary
// messages carry stdin/stdout; text messages carry execControl.
//
//	GET /api/v1/tasks/{id}/exec?command=sh&command=-l&tty=1&cols=120&rows=30
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "exec is not enabled")
		return
	}
	t, err := s.store.TaskByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such task")
		return
	} else if err != nil {
		s.internalError(w, "get task", err)
		return
	}
	if t.Desired != "running" || t.State != store.TaskRunning || t.NodeID == "" {
		writeError(w, http.StatusConflict, CodeConflict, "the task is not running")
		return
	}
	q := r.URL.Query()
	command := q["command"]
	tty := q.Get("tty") != "0"
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, _ := strconv.Atoi(q.Get("rows"))

	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "task:Exec", "srn:syncloud:task/"+t.ID, map[string]any{"command": strings.Join(command, " ")})
	s.relayExec(w, r, t.NodeID, t.ID, command, tty, cols, rows)
}

// relayExec upgrades to a WebSocket and relays an exec session in a
// container (a task, or a Cloud Shell) on a node.
// An empty taskID opens the command on the node itself.
func (s *Server) relayExec(w http.ResponseWriter, r *http.Request, nodeID, taskID string, command []string, tty bool, cols, rows int) {
	c, err := websocket.Accept(w, r, nil) // same-origin only
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	var sess *execrelay.Session
	if taskID == "" {
		sess, err = s.exec.OpenHost(nodeID, command, tty, uint32(cols), uint32(rows))
	} else {
		sess, err = s.exec.Open(nodeID, taskID, command, tty, uint32(cols), uint32(rows))
	}
	if err != nil {
		writeControl(r.Context(), c, execControl{Type: "error", Message: err.Error()})
		c.Close(websocket.StatusNormalClosure, "")
		return
	}
	defer sess.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() { // client -> node
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				_ = sess.Stdin(data)
				continue
			}
			var m execControl
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "resize":
				_ = sess.Resize(m.Cols, m.Rows)
			case "eof":
				_ = sess.CloseStdin()
			}
		}
	}()
	for { // node -> client
		select {
		case <-ctx.Done():
			return
		case o, ok := <-sess.Out:
			if !ok {
				c.Close(websocket.StatusNormalClosure, "")
				return
			}
			if len(o.GetData()) > 0 {
				if err := c.Write(ctx, websocket.MessageBinary, o.GetData()); err != nil {
					return
				}
			}
			if o.GetExited() {
				m := execControl{Type: "exit", Code: o.GetExitCode()}
				if o.GetError() != "" {
					m = execControl{Type: "error", Message: o.GetError()}
				}
				writeControl(ctx, c, m)
				c.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}
}

func writeControl(ctx context.Context, c *websocket.Conn, m execControl) {
	b, _ := json.Marshal(m)
	_ = c.Write(ctx, websocket.MessageText, b)
}
