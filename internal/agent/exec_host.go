package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"syscall"

	"github.com/creack/pty"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// runHost runs a command on the node itself (a node shell), as the user the
// agent runs as: a login shell by default, under a PTY when tty is set.
func (e *execSessions) runHost(ctx context.Context, id string, st *agentv1.ExecStart, s *execSession, fail func(error)) {
	cmd := st.GetCommand()
	if len(cmd) == 0 {
		cmd = []string{loginShell(), "-l"}
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	home := "/"
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		home = u.HomeDir
	}
	c.Dir = home
	c.Env = append(os.Environ(), "TERM=xterm-256color", "HOME="+home, "SYNCLOUD_NODE_SHELL=1")

	var (
		out io.Reader
		in  io.WriteCloser
		tty *os.File
	)
	if st.GetTty() {
		size := &pty.Winsize{Cols: uint16(st.GetCols()), Rows: uint16(st.GetRows())}
		if size.Cols == 0 {
			size.Cols, size.Rows = 120, 30
		}
		f, err := pty.StartWithSize(c, size) // also starts a new session
		if err != nil {
			fail(err)
			return
		}
		tty, out, in = f, f, f
		defer f.Close()
	} else {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		pr, pw := io.Pipe()
		c.Stdout, c.Stderr = pw, pw
		stdin, err := c.StdinPipe()
		if err != nil {
			fail(err)
			return
		}
		if err := c.Start(); err != nil {
			fail(err)
			return
		}
		go func() { _ = c.Wait(); pw.Close() }()
		out, in = pr, stdin
	}
	e.log.Info("node shell started", "command", cmd)

	// Ending the session (client gone, controller closed it) kills the
	// command and everything it started.
	go func() {
		<-ctx.Done()
		if c.Process != nil {
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
	}()
	go func() { // input -> command
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-s.in:
				switch m := msg.Msg.(type) {
				case *agentv1.ExecInput_Stdin:
					_, _ = in.Write(m.Stdin)
				case *agentv1.ExecInput_StdinEof:
					if tty == nil {
						_ = in.Close()
					} else {
						_, _ = in.Write([]byte{4}) // ^D
					}
				case *agentv1.ExecInput_Resize:
					if tty != nil {
						_ = pty.Setsize(tty, &pty.Winsize{Cols: uint16(m.Resize.GetCols()), Rows: uint16(m.Resize.GetRows())})
					}
				}
			}
		}
	}()

	buf := make([]byte, 32<<10)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			e.emit(&agentv1.ExecOutput{SessionId: id, Data: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			break // EIO on a PTY once the shell exits
		}
	}
	code := 0
	if tty != nil {
		err := c.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	} else if c.ProcessState != nil {
		code = c.ProcessState.ExitCode()
	}
	e.emit(&agentv1.ExecOutput{SessionId: id, Exited: true, ExitCode: int32(code)})
}

// loginShell is the shell for node sessions: bash when installed.
func loginShell() string {
	for _, sh := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	return "sh"
}
