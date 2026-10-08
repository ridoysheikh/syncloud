package agent

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// collect runs a host session and returns its output and exit code.
func collect(t *testing.T, e *execSessions, st *agentv1.ExecStart, input ...*agentv1.ExecInput) (string, int32, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.handle(ctx, &agentv1.ExecInput{SessionId: "s1", Msg: &agentv1.ExecInput_Start{Start: st}})
	for _, in := range input {
		in.SessionId = "s1"
		e.handle(ctx, in)
	}
	var out strings.Builder
	for {
		select {
		case o := <-e.out:
			out.Write(o.GetData())
			if o.GetExited() {
				return out.String(), o.GetExitCode(), o.GetError()
			}
		case <-ctx.Done():
			t.Fatalf("no exit; output so far %q", out.String())
		}
	}
}

func TestHostShell(t *testing.T) {
	e := newExecSessions(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.hostShell = true

	out, code, _ := collect(t, e, &agentv1.ExecStart{Host: true, Command: []string{"sh", "-c", "echo hello; exit 3"}})
	if !strings.Contains(out, "hello") || code != 3 {
		t.Errorf("pipe: output %q, code %d", out, code)
	}

	out, code, _ = collect(t, e, &agentv1.ExecStart{Host: true, Tty: true, Cols: 100, Rows: 20, Command: []string{"sh"}},
		&agentv1.ExecInput{Msg: &agentv1.ExecInput_Stdin{Stdin: []byte("echo term=$TERM size=$(stty size)\nexit 0\n")}})
	if !strings.Contains(out, "term=xterm-256color size=20 100") || code != 0 {
		t.Errorf("tty: output %q, code %d", out, code)
	}

	e.hostShell = false
	_, code, msg := collect(t, e, &agentv1.ExecStart{Host: true})
	if code != -1 || !strings.Contains(msg, "turned off") {
		t.Errorf("--no-host-shell: code %d, error %q", code, msg)
	}
}
