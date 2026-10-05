package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// ExecCreate prepares a command in a running container.
func (c *Client) ExecCreate(ctx context.Context, containerID string, cmd []string, tty bool) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	err := c.json(ctx, "POST", "/containers/"+containerID+"/exec", nil, map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": tty, "Cmd": cmd,
		"Env": []string{"TERM=xterm-256color"},
	}, &out)
	return out.ID, err
}

// ExecStart starts an exec and returns the hijacked connection: writes go to
// the command's stdin; reads return its output (raw with a TTY, otherwise
// multiplexed like container logs).
func (c *Client) ExecStart(ctx context.Context, execID string, tty bool) (net.Conn, *bufio.Reader, error) {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, nil, err
	}
	body, _ := json.Marshal(map[string]bool{"Detach": false, "Tty": tty})
	req, _ := http.NewRequest("POST", "http://docker/"+APIVersion+"/exec/"+execID+"/start", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	req.ContentLength = int64(len(body))
	req.Body = io.NopCloser(bytesReader(body))
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		conn.Close()
		return nil, nil, &Error{Status: resp.StatusCode, Message: string(b)}
	}
	return conn, br, nil
}

// ExecResize sets the TTY size.
func (c *Client) ExecResize(ctx context.Context, execID string, cols, rows uint32) error {
	resp, err := c.do(ctx, "POST", "/exec/"+execID+"/resize", url.Values{"w": {fmt.Sprint(cols)}, "h": {fmt.Sprint(rows)}}, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ExecExitCode returns the exit code once the command finished.
func (c *Client) ExecExitCode(ctx context.Context, execID string) (int, bool, error) {
	var out struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	err := c.json(ctx, "GET", "/exec/"+execID+"/json", nil, nil, &out)
	return out.ExitCode, !out.Running, err
}

type byteReader struct {
	b []byte
	i int
}

func bytesReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
