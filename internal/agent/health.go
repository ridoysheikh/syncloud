package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/agent/docker"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// Health states reported in TaskStatus.health.
const (
	HealthStarting  = "starting"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
)

// prober runs one task's health check (§5.6).
type prober struct {
	cancel context.CancelFunc
	hash   string // spec the probe belongs to

	mu      sync.Mutex
	health  string
	lastErr string // why the last check failed ("" after a pass)
}

func (p *prober) get() (health, lastErr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.health, p.lastErr
}

// startProbe begins probing a running task, replacing an older probe.
func (r *Runner) startProbe(spec *agentv1.TaskSpec, containerID, hash string) {
	hc := spec.GetHealth()
	r.mu.Lock()
	ctx := r.ctx
	old := r.probes[spec.TaskId]
	if old != nil && old.hash == hash {
		r.mu.Unlock()
		return // already probing this spec
	}
	if old != nil {
		old.cancel()
		delete(r.probes, spec.TaskId)
	}
	if hc == nil {
		r.mu.Unlock()
		return
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &prober{cancel: cancel, hash: hash, health: HealthStarting}
	r.probes[spec.TaskId] = p
	r.mu.Unlock()
	go r.probeLoop(pctx, spec.TaskId, containerID, hc, p)
}

func (r *Runner) stopProbe(taskID string) {
	r.mu.Lock()
	if p := r.probes[taskID]; p != nil {
		p.cancel()
		delete(r.probes, taskID)
	}
	r.mu.Unlock()
}

// probeHealth returns the probe's state for a task and why its last check
// failed ("" when not probed).
func (r *Runner) probeHealth(taskID string) (health, lastErr string) {
	r.mu.Lock()
	p := r.probes[taskID]
	r.mu.Unlock()
	if p == nil {
		return "", ""
	}
	return p.get()
}

// describeCheck says what a health check probes.
func describeCheck(hc *agentv1.HealthCheck) string {
	every := fmt.Sprintf("every %ds, %d failures in a row make it unhealthy", max(hc.GetIntervalSeconds(), 1), max(hc.GetRetries(), 1))
	if sp := hc.GetStartPeriodSeconds(); sp > 0 {
		every += fmt.Sprintf(", failures in the first %ds do not count", sp)
	}
	switch hc.GetType() {
	case "http":
		path := hc.GetPath()
		if path == "" {
			path = "/"
		}
		return fmt.Sprintf("HTTP GET :%d%s %s", hc.GetPort(), path, every)
	case "tcp":
		return fmt.Sprintf("TCP connect to :%d %s", hc.GetPort(), every)
	case "cmd":
		return fmt.Sprintf("command %q %s", strings.Join(hc.GetCommand(), " "), every)
	}
	return hc.GetType()
}

func (r *Runner) probeLoop(ctx context.Context, taskID, containerID string, hc *agentv1.HealthCheck, p *prober) {
	defer func() {
		r.mu.Lock()
		if r.probes[taskID] == p {
			delete(r.probes, taskID)
		}
		r.mu.Unlock()
	}()
	interval := time.Duration(max(hc.GetIntervalSeconds(), 1)) * time.Second
	timeout := time.Duration(max(hc.GetTimeoutSeconds(), 1)) * time.Second
	retries := int(max(hc.GetRetries(), 1))
	started := time.Now()
	startPeriod := time.Duration(hc.GetStartPeriodSeconds()) * time.Second
	failures := 0
	lastLogged := ""
	t := time.NewTimer(min(interval, 2*time.Second)) // first probe soon after start
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(interval)
		c, err := r.docker.Inspect(ctx, containerID)
		if err != nil || !c.State.Running {
			return // the container is gone or stopped; Watch reports that
		}
		ip := ""
		for _, n := range c.NetworkSettings.Networks {
			if n.IPAddress != "" {
				ip = n.IPAddress
			}
		}
		if n, ok := c.NetworkSettings.Networks[TaskNetwork]; ok && n.IPAddress != "" {
			ip = n.IPAddress
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		t0 := time.Now()
		detail, err := check(pctx, r.docker, hc, ip, containerID)
		took := fmt.Sprintf("%.1f ms", float64(time.Since(t0).Microseconds())/1000)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil && errors.Is(pctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("no answer within %s: %w", timeout, err)
		}

		p.mu.Lock()
		before := p.health
		grace := false
		switch {
		case err == nil:
			failures = 0
			p.health, p.lastErr = HealthHealthy, ""
		case before == HealthStarting && time.Since(started) < startPeriod:
			grace = true // still starting: failures do not count yet
			p.lastErr = err.Error()
		default:
			failures++
			p.lastErr = err.Error()
			if failures >= retries {
				p.health = HealthUnhealthy
			}
		}
		after := p.health
		p.mu.Unlock()

		// The deployment log gets each distinct failure, and every one up to
		// the point the task turns unhealthy; repeats of the same error while
		// unhealthy are only counted.
		if err != nil {
			msg := err.Error()
			switch {
			case grace && msg != lastLogged:
				r.event(taskID, "health", levelInfo, "not ready yet (start period): %s", msg)
			case !grace && after != HealthUnhealthy:
				r.event(taskID, "health", levelWarn, "health check failed (%d of %d allowed): %s", failures, retries, msg)
			case !grace && after == HealthUnhealthy && before != HealthUnhealthy:
				r.event(taskID, "unhealthy", levelError, "unhealthy: %d health checks in a row failed; last: %s", failures, msg)
			case !grace && msg != lastLogged:
				r.event(taskID, "health", levelWarn, "still unhealthy: %s", msg)
			}
			lastLogged = msg
		} else if after != before {
			r.event(taskID, "healthy", levelInfo, "healthy: %s (%s)", detail, took)
			lastLogged = ""
		}
		if after != before {
			r.log.Info("task health changed", "task", taskID, "health", after, "err", err)
			r.emit(r.inspect(ctx, containerID, taskID))
		}
	}
}

// check runs one health check and describes a pass (e.g. "HTTP 200").
func check(ctx context.Context, d *docker.Client, hc *agentv1.HealthCheck, ip, containerID string) (string, error) {
	switch hc.GetType() {
	case "http":
		if ip == "" {
			return "", fmt.Errorf("the container has no address on the %s network", TaskNetwork)
		}
		path := hc.GetPath()
		if path == "" {
			path = "/"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(ip, strconv.Itoa(int(hc.GetPort())))+path, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "syncloud-health/1")
		resp, err := healthClient.Do(req)
		if err != nil {
			return "", err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			snippet := strings.TrimSpace(string(body))
			if len(snippet) > 200 {
				snippet = snippet[:200] + "…"
			}
			if snippet != "" {
				return "", fmt.Errorf("GET %s answered HTTP %d: %s", req.URL, resp.StatusCode, snippet)
			}
			return "", fmt.Errorf("GET %s answered HTTP %d", req.URL, resp.StatusCode)
		}
		return fmt.Sprintf("GET %s answered HTTP %d", req.URL, resp.StatusCode), nil
	case "tcp":
		if ip == "" {
			return "", fmt.Errorf("the container has no address on the %s network", TaskNetwork)
		}
		addr := net.JoinHostPort(ip, strconv.Itoa(int(hc.GetPort())))
		c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return "", err
		}
		return "TCP connect to " + addr + " succeeded", c.Close()
	case "cmd":
		id, err := d.ExecCreate(ctx, containerID, hc.GetCommand(), false)
		if err != nil {
			return "", err
		}
		conn, br, err := d.ExecStart(ctx, id, false)
		if err != nil {
			return "", err
		}
		go func() { <-ctx.Done(); conn.Close() }()
		out, _ := io.ReadAll(io.LimitReader(br, 4<<10))
		conn.Close()
		code, done, err := d.ExecExitCode(context.WithoutCancel(ctx), id)
		if err != nil {
			return "", err
		}
		if !done || code != 0 {
			msg := fmt.Sprintf("command exited with code %d", code)
			if s := strings.TrimSpace(stripMux(out)); s != "" {
				if len(s) > 300 {
					s = s[len(s)-300:]
				}
				msg += ": " + s
			}
			return "", errors.New(msg)
		}
		return "command exited with code 0", nil
	}
	return "", fmt.Errorf("unknown health check type %q", hc.GetType())
}

// stripMux drops Docker's 8-byte stream headers from non-TTY exec output.
func stripMux(b []byte) string {
	var out []byte
	for len(b) >= 8 && (b[0] == 1 || b[0] == 2) && b[1] == 0 && b[2] == 0 && b[3] == 0 {
		n := int(b[4])<<24 | int(b[5])<<16 | int(b[6])<<8 | int(b[7])
		b = b[8:]
		if n > len(b) {
			n = len(b)
		}
		out = append(out, b[:n]...)
		b = b[n:]
	}
	return string(append(out, b...))
}

// healthClient never follows redirects (3xx counts as healthy) and reuses no
// connections, so every probe exercises the app.
var healthClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{DisableKeepAlives: true},
}
