package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) execCmd() *cobra.Command {
	var s scope
	var tty bool
	cmd := &cobra.Command{
		Use:   "exec TASK|service/NAME [-- COMMAND...]",
		Short: "Run a command in a task, interactively (like kubectl exec)",
		Example: `  synctl exec task_ab12cd34 -- sh
  synctl exec service/web -p shop -- cat /etc/hostname`,
		Args:        cobra.MinimumNArgs(1),
		Annotations: op("execTask"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			target, command := args[0], args[1:]
			taskID := target
			if name, ok := strings.CutPrefix(target, "service/"); ok {
				if err := s.need(); err != nil {
					return err
				}
				ts, err := c.ServiceTasks(ctx(cmd), s.project, s.env, name)
				if err != nil {
					return err
				}
				taskID = ""
				for _, t := range ts {
					if t.Desired == "running" && t.State == "running" {
						taskID = t.ID
						break
					}
				}
				if taskID == "" {
					return fmt.Errorf("service %s has no running task", name)
				}
			}
			stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
			if !cmd.Flags().Changed("tty") {
				tty = stdinTTY
			}
			cols, rows := 80, 24
			if tty && stdinTTY {
				if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
					cols, rows = w, h
				}
			}
			conn, err := c.ExecDial(ctx(cmd), taskID, command, tty, cols, rows)
			if err != nil {
				return err
			}
			defer conn.CloseNow()
			if tty && stdinTTY {
				old, err := term.MakeRaw(int(os.Stdin.Fd()))
				if err == nil {
					defer term.Restore(int(os.Stdin.Fd()), old)
				}
			}
			ctx, cancel := context.WithCancel(ctx(cmd))
			defer cancel()
			go func() {
				buf := make([]byte, 32<<10)
				for {
					n, err := os.Stdin.Read(buf)
					if n > 0 {
						if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
							return
						}
					}
					if err != nil {
						// End of input (e.g. a pipe): let the command see EOF.
						_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"eof"}`))
						return
					}
				}
			}()
			for {
				typ, data, err := conn.Read(ctx)
				if err != nil {
					if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
						return nil
					}
					return err
				}
				if typ == websocket.MessageBinary {
					_, _ = os.Stdout.Write(data)
					continue
				}
				var m struct {
					Type    string `json:"type"`
					Code    int    `json:"code"`
					Message string `json:"message"`
				}
				if json.Unmarshal(data, &m) != nil {
					continue
				}
				switch m.Type {
				case "exit":
					if m.Code != 0 {
						return exitError(m.Code)
					}
					return nil
				case "error":
					return errors.New(m.Message)
				}
			}
		},
	}
	a.scopeFlags(cmd, &s)
	cmd.Flags().BoolVarP(&tty, "tty", "t", false, "allocate a terminal (default: when stdin is a terminal)")
	return cmd
}

// exitError carries the remote command's exit code.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("command exited with code %d", int(e)) }

// ExitCode returns the exit code synctl should end with for err.
func ExitCode(err error) int {
	var e exitError
	if errors.As(err, &e) {
		return int(e)
	}
	return 1
}
