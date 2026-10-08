package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
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

	mu     sync.Mutex
	health string
}

func (p *prober) get() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.health
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

// probeHealth returns the probe's state for a task ("" when not probed).
func (r *Runner) probeHealth(taskID string) string {
	r.mu.Lock()
	p := r.probes[taskID]
	r.mu.Unlock()
	if p == nil {
		return ""
	}
	return p.get()
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
		err = check(pctx, r.docker, hc, ip, containerID)
		cancel()
		if ctx.Err() != nil {
			return
		}

		p.mu.Lock()
		before := p.health
		switch {
		case err == nil:
			failures = 0
			p.health = HealthHealthy
		case before == HealthStarting && time.Since(started) < startPeriod:
			// Still starting: failures do not count yet.
		default:
			failures++
			if failures >= retries {
				p.health = HealthUnhealthy
			}
		}
		after := p.health
		p.mu.Unlock()
		if after != before {
			r.log.Info("task health changed", "task", taskID, "health", after, "err", err)
			r.emit(r.inspect(ctx, containerID, taskID))
		}
	}
}

func check(ctx context.Context, d *docker.Client, hc *agentv1.HealthCheck, ip, containerID string) error {
	switch hc.GetType() {
	case "http":
		if ip == "" {
			return fmt.Errorf("no task IP")
		}
		path := hc.GetPath()
		if path == "" {
			path = "/"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(ip, strconv.Itoa(int(hc.GetPort())))+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "syncloud-health/1")
		resp, err := healthClient.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	case "tcp":
		if ip == "" {
			return fmt.Errorf("no task IP")
		}
		c, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(int(hc.GetPort()))))
		if err != nil {
			return err
		}
		return c.Close()
	case "cmd":
		id, err := d.ExecCreate(ctx, containerID, hc.GetCommand(), false)
		if err != nil {
			return err
		}
		conn, br, err := d.ExecStart(ctx, id, false)
		if err != nil {
			return err
		}
		go func() { <-ctx.Done(); conn.Close() }()
		_, _ = io.Copy(io.Discard, br)
		conn.Close()
		code, done, err := d.ExecExitCode(context.WithoutCancel(ctx), id)
		if err != nil {
			return err
		}
		if !done || code != 0 {
			return fmt.Errorf("exit code %d", code)
		}
		return nil
	}
	return fmt.Errorf("unknown health check type %q", hc.GetType())
}

// healthClient never follows redirects (3xx counts as healthy) and reuses no
// connections, so every probe exercises the app.
var healthClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{DisableKeepAlives: true},
}
