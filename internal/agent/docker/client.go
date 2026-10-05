// Package docker is a minimal Docker Engine API client over the local unix
// socket: only what the agent needs. Using the API directly keeps the agent a
// small static binary without the Docker SDK's dependency tree.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIVersion is the Engine API version the agent speaks (Docker 25+).
const APIVersion = "v1.44"

const DefaultSocket = "/var/run/docker.sock"

type Client struct {
	http *http.Client
}

func New(socket string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConns: 4,
	}}}
}

// Error is an Engine API error response.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.Status) }

func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	u := "http://docker/" + APIVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var e struct{ Message string }
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		return nil, &Error{Status: resp.StatusCode, Message: e.Message}
	}
	return resp, nil
}

func (c *Client) json(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, err := c.do(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct{ Version string }
	// /version works on any API version, so this also detects too-old daemons.
	return v.Version, c.json(ctx, "GET", "/version", nil, nil, &v)
}

func (c *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	err := c.json(ctx, "GET", "/images/"+ref+"/json", nil, nil, nil)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// Pull pulls ref ("name:tag" or "name@digest"). registryAuth is the
// base64url-encoded JSON auth config, or "" for anonymous pulls.
func (c *Client) Pull(ctx context.Context, ref, registryAuth string) error {
	req, err := http.NewRequestWithContext(ctx, "POST",
		"http://docker/"+APIVersion+"/images/create?"+url.Values{"fromImage": {ref}}.Encode(), nil)
	if err != nil {
		return err
	}
	if registryAuth != "" {
		req.Header.Set("X-Registry-Auth", registryAuth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e struct{ Message string }
		_ = json.Unmarshal(b, &e)
		return &Error{Status: resp.StatusCode, Message: e.Message}
	}
	// The pull streams JSON progress; failures appear as an "error" field mid-stream.
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var msg struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, msg.Error)
		}
	}
	return sc.Err()
}

// EnsureNetwork creates a bridge network if it doesn't exist.
func (c *Client) EnsureNetwork(ctx context.Context, name string, labels map[string]string) error {
	err := c.json(ctx, "GET", "/networks/"+name, nil, nil, nil)
	if err == nil {
		return nil
	}
	if !IsNotFound(err) {
		return err
	}
	err = c.json(ctx, "POST", "/networks/create", nil, map[string]any{
		"Name": name, "Driver": "bridge", "Labels": labels, "CheckDuplicate": true,
	}, nil)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusConflict {
		return nil // created concurrently
	}
	return err
}

// CreateRequest is the subset of the container create body the agent uses.
type CreateRequest struct {
	Image        string              `json:"Image"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	HostConfig   HostConfig          `json:"HostConfig"`
}

type HostConfig struct {
	NetworkMode   string                   `json:"NetworkMode,omitempty"`
	PortBindings  map[string][]PortBinding `json:"PortBindings,omitempty"`
	Mounts        []Mount                  `json:"Mounts,omitempty"`
	RestartPolicy RestartPolicy            `json:"RestartPolicy"`
	Memory        int64                    `json:"Memory,omitempty"`
	NanoCPUs      int64                    `json:"NanoCpus,omitempty"`
	ExtraHosts    []string                 `json:"ExtraHosts,omitempty"`
	DNS           []string                 `json:"Dns,omitempty"`
	DNSSearch     []string                 `json:"DnsSearch,omitempty"`
	LogConfig     LogConfig                `json:"LogConfig"`
}

type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type Mount struct {
	Type     string `json:"Type"` // volume | bind
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly"`
}

type RestartPolicy struct {
	Name string `json:"Name"`
}

type LogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config,omitempty"`
}

func (c *Client) Create(ctx context.Context, name string, req CreateRequest) (string, error) {
	var out struct{ Id string }
	err := c.json(ctx, "POST", "/containers/create", url.Values{"name": {name}}, req, &out)
	return out.Id, err
}

func (c *Client) Start(ctx context.Context, id string) error {
	return c.json(ctx, "POST", "/containers/"+id+"/start", nil, nil, nil)
}

func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	err := c.json(ctx, "POST", "/containers/"+id+"/stop", url.Values{"t": {fmt.Sprint(int(timeout.Seconds()))}}, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) Remove(ctx context.Context, id string) error {
	err := c.json(ctx, "DELETE", "/containers/"+id, url.Values{"force": {"true"}}, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"` // created, running, exited, …
	Labels map[string]string `json:"Labels"`
}

// List returns all containers (running or not) that have every given label.
func (c *Client) List(ctx context.Context, labels ...string) ([]ContainerSummary, error) {
	f, _ := json.Marshal(map[string][]string{"label": labels})
	var out []ContainerSummary
	return out, c.json(ctx, "GET", "/containers/json", url.Values{"all": {"1"}, "filters": {string(f)}}, nil, &out)
}

type ContainerState struct {
	Status    string `json:"Status"`
	Running   bool   `json:"Running"`
	ExitCode  int    `json:"ExitCode"`
	Error     string `json:"Error"`
	StartedAt string `json:"StartedAt"`
	Health    *struct {
		Status string `json:"Status"`
	} `json:"Health"`
}

type ContainerJSON struct {
	ID     string         `json:"Id"`
	Name   string         `json:"Name"`
	State  ContainerState `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (c *Client) Inspect(ctx context.Context, id string) (ContainerJSON, error) {
	var out ContainerJSON
	return out, c.json(ctx, "GET", "/containers/"+id+"/json", nil, nil, &out)
}

type Event struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

// Events streams container events for containers with the given label until
// ctx ends or the connection drops (the error is sent on the returned channel).
func (c *Client) Events(ctx context.Context, label string) (<-chan Event, <-chan error) {
	evc, errc := make(chan Event, 64), make(chan error, 1)
	go func() {
		defer close(evc)
		f, _ := json.Marshal(map[string][]string{"type": {"container"}, "label": {label}})
		resp, err := c.do(ctx, "GET", "/events", url.Values{"filters": {string(f)}}, nil)
		if err != nil {
			errc <- err
			return
		}
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			var e Event
			if err := dec.Decode(&e); err != nil {
				errc <- err
				return
			}
			select {
			case evc <- e:
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			}
		}
	}()
	return evc, errc
}

// EnsureBridge makes sure a bridge network exists with exactly this subnet.
// A network with a different subnet is recreated if no containers use it.
func (c *Client) EnsureBridge(ctx context.Context, name, subnet, gateway string, options, labels map[string]string) error {
	var cur struct {
		IPAM struct {
			Config []struct {
				Subnet string `json:"Subnet"`
			} `json:"Config"`
		} `json:"IPAM"`
		Options    map[string]string `json:"Options"`
		Containers map[string]any    `json:"Containers"`
	}
	err := c.json(ctx, "GET", "/networks/"+name, nil, nil, &cur)
	switch {
	case err == nil:
		same := len(cur.IPAM.Config) == 1 && cur.IPAM.Config[0].Subnet == subnet
		for k, v := range options {
			same = same && cur.Options[k] == v
		}
		if same {
			return nil
		}
		if len(cur.Containers) > 0 {
			return fmt.Errorf("network %s has different settings and %d containers attached; stop them to let it be recreated", name, len(cur.Containers))
		}
		if err := c.json(ctx, "DELETE", "/networks/"+name, nil, nil, nil); err != nil && !IsNotFound(err) {
			return err
		}
	case !IsNotFound(err):
		return err
	}
	return c.json(ctx, "POST", "/networks/create", nil, map[string]any{
		"Name": name, "Driver": "bridge", "Labels": labels, "Options": options, "CheckDuplicate": true,
		"IPAM": map[string]any{"Driver": "default", "Config": []map[string]string{{"Subnet": subnet, "Gateway": gateway}}},
	}, nil)
}

// LogEntry is one line of container output.
type LogEntry struct {
	Time   time.Time
	Stream string // stdout | stderr
	Line   string
}

// Logs follows a container's output from since (exclusive) until the
// container stops or ctx ends. Lines longer than 64 KiB are split.
func (c *Client) Logs(ctx context.Context, id string, since time.Time, fn func(LogEntry)) error {
	q := url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if !since.IsZero() {
		q.Set("since", fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond()))
	}
	resp, err := c.do(ctx, "GET", "/containers/"+id+"/logs", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Non-TTY output is multiplexed: an 8-byte header (stream, 0, 0, 0, size
	// big-endian) before each frame.
	r := bufio.NewReaderSize(resp.Body, 64<<10)
	hdr := make([]byte, 8)
	partial := map[byte]string{}
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		n := int(hdr[4])<<24 | int(hdr[5])<<16 | int(hdr[6])<<8 | int(hdr[7])
		frame := make([]byte, n)
		if _, err := io.ReadFull(r, frame); err != nil {
			return err
		}
		stream := "stdout"
		if hdr[0] == 2 {
			stream = "stderr"
		}
		text := partial[hdr[0]] + string(frame)
		lines := strings.Split(text, "\n")
		partial[hdr[0]] = lines[len(lines)-1] // incomplete last line
		if len(partial[hdr[0]]) > 64<<10 {
			lines = append(lines[:len(lines)-1], partial[hdr[0]], "")
			partial[hdr[0]] = ""
		}
		for _, l := range lines[:len(lines)-1] {
			ts, msg, ok := strings.Cut(l, " ")
			e := LogEntry{Stream: stream, Line: msg}
			if t, err := time.Parse(time.RFC3339Nano, ts); ok && err == nil {
				e.Time = t
			} else {
				e.Time, e.Line = time.Now(), l
			}
			fn(e)
		}
	}
}
