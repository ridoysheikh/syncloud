package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// Central per-task probes (§5.6): the controller checks every running task
// over the private network, the way Traefik on the controller reaches it. A
// task it cannot reach three times in a row leaves Traefik's routes until it
// answers again; it keeps its VIP backends (other nodes may still reach it)
// and is not replaced, since the fault may be the path rather than the task.

const (
	taskEvery     = 10 * time.Second
	taskFailLimit = 3
)

// TaskCheck is the controller's view of one task.
type TaskCheck struct {
	State   string    `json:"state"` // ok | failing | unreachable
	Target  string    `json:"target"`
	Error   string    `json:"error,omitempty"`
	Fails   int       `json:"fails"`
	Checked time.Time `json:"checked"`
}

type taskProbes struct {
	mu     sync.Mutex
	checks map[string]*TaskCheck
	client *http.Client
}

// RunTaskProbes probes every running task until ctx ends.
func (m *Monitor) RunTaskProbes(ctx context.Context) {
	m.tasks.client = &http.Client{
		Timeout:       3 * time.Second,
		Transport:     &http.Transport{DisableKeepAlives: true, Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	t := time.NewTicker(taskEvery)
	defer t.Stop()
	for {
		m.probeTasks(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// probeTarget returns how to reach a task: an HTTP URL for an http health
// check, or a host:port to connect to.
func probeTarget(spec workload.Spec, ip string) (url, addr string) {
	port := 0
	if h := spec.Health; h != nil && (h.Type == "http" || h.Type == "tcp") {
		port = portNamed(spec, h.Port)
		if port != 0 && h.Type == "http" {
			path := h.Path
			if path == "" {
				path = "/"
			}
			return "http://" + net.JoinHostPort(ip, strconv.Itoa(port)) + path, ""
		}
	}
	if port == 0 {
		for _, p := range spec.Ports {
			if p.Protocol != "udp" {
				port = p.Container
				break
			}
		}
	}
	if port == 0 {
		return "", ""
	}
	return "", net.JoinHostPort(ip, strconv.Itoa(port))
}

func portNamed(spec workload.Spec, name string) int {
	for _, p := range spec.Ports {
		if name == "" || p.Name == name {
			return p.Container
		}
	}
	return 0
}

func (m *Monitor) probeTasks(ctx context.Context) {
	tasks, err := m.st.ActiveTasks(ctx)
	if err != nil {
		return
	}
	type job struct {
		task      store.Task
		url, addr string
	}
	var jobs []job
	for _, t := range tasks {
		if t.State != store.TaskRunning || t.Desired != "running" || t.IP == "" {
			continue
		}
		spec, err := m.wl.SpecFor(ctx, t.ServiceID, t.Revision)
		if err != nil {
			continue
		}
		url, addr := probeTarget(spec, t.IP)
		if url == "" && addr == "" {
			continue
		}
		jobs = append(jobs, job{t, url, addr})
	}
	results := make([]error, len(jobs))
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = m.probeTask(ctx, j.url, j.addr)
		}()
	}
	wg.Wait()
	now := m.now().UTC()
	changed := false
	type note struct {
		task              store.Task
		level, msg        string
		unreachable, path bool
	}
	var notes []note
	m.tasks.mu.Lock()
	live := map[string]bool{}
	for i, j := range jobs {
		live[j.task.ID] = true
		c := m.tasks.checks[j.task.ID]
		if c == nil {
			c = &TaskCheck{}
			m.tasks.checks[j.task.ID] = c
		}
		prev := c.State
		was := prev == "unreachable"
		c.Checked, c.Target = now, j.url+j.addr
		if err := results[i]; err != nil {
			c.Fails++
			c.Error = err.Error()
			c.State = "failing"
			if c.Fails >= taskFailLimit {
				c.State = "unreachable"
			}
		} else {
			c.Fails, c.Error, c.State = 0, "", "ok"
		}
		// The deployment log gets each change of what the controller sees.
		switch {
		case c.State == "ok" && prev == "":
			notes = append(notes, note{task: j.task, level: "info", msg: "reachable from the controller over the private network at " + c.Target})
		case c.State == "ok" && prev != "ok":
			notes = append(notes, note{task: j.task, level: "info", msg: "reachable from the controller again at " + c.Target + "; back in routing"})
		case c.State == "failing" && (prev == "" || prev == "ok"):
			notes = append(notes, note{task: j.task, level: "warn", msg: fmt.Sprintf("the controller cannot reach the task at %s: %s (check %d of %d before routing leaves it out)", c.Target, c.Error, c.Fails, taskFailLimit)})
		case c.State == "unreachable" && !was:
			notes = append(notes, note{task: j.task, level: "error", unreachable: true, path: true,
				msg: fmt.Sprintf("unreachable from the controller after %d checks: %s; Traefik no longer routes to it", c.Fails, c.Error)})
		}
		if was != (c.State == "unreachable") {
			changed = true
			if c.State == "unreachable" {
				m.log.Warn("task unreachable from the controller; removed from routing", "task", j.task.ID, "target", c.Target, "err", c.Error)
			} else {
				m.log.Info("task reachable again from the controller", "task", j.task.ID)
			}
		}
	}
	for id, c := range m.tasks.checks {
		if !live[id] {
			if c.State == "unreachable" {
				changed = true
			}
			delete(m.tasks.checks, id)
		}
	}
	m.tasks.mu.Unlock()
	if changed {
		m.wl.InvalidateRoutes()
	}
	for _, n := range notes {
		msg := n.msg
		if n.path && m.cfg.MeshPath != nil {
			if p := m.cfg.MeshPath(ctx, n.task.NodeID); p != "" {
				msg += ". Private network: " + p
			}
		}
		m.wl.TaskEvent(ctx, n.task, "probe", n.level, msg, now)
		if n.unreachable {
			m.log.Warn("task unreachable from the controller", "task", n.task.ID, "detail", msg)
		}
	}
}

func (m *Monitor) probeTask(ctx context.Context, url, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if url != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "SynCloud-Health")
		resp, err := m.tasks.client.Do(req)
		if err != nil {
			return explainProbe(ctx, err)
		}
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return explainProbe(ctx, err)
	}
	return c.Close()
}

// explainProbe turns a probe error into what it says about the path.
func explainProbe(ctx context.Context, err error) error {
	msg := err.Error()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || strings.Contains(msg, "i/o timeout"):
		return fmt.Errorf("no answer within 3s (packets are dropped on the way: the WireGuard tunnel, a firewall, or the node's forwarding): %w", err)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("connection refused (the node answered but nothing listens on the port; check the port the app binds and that it listens on 0.0.0.0): %w", err)
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		return fmt.Errorf("no route to the task's subnet (the private network is not set up on the controller): %w", err)
	}
	return err
}

// TaskReachable reports whether routing may use a task: true unless the
// controller failed to reach it three times in a row.
func (m *Monitor) TaskReachable(taskID string) bool {
	m.tasks.mu.Lock()
	defer m.tasks.mu.Unlock()
	c := m.tasks.checks[taskID]
	return c == nil || c.State != "unreachable"
}

// TaskCheckOf returns the controller's latest probe of a task.
func (m *Monitor) TaskCheckOf(taskID string) *TaskCheck {
	m.tasks.mu.Lock()
	defer m.tasks.mu.Unlock()
	if c := m.tasks.checks[taskID]; c != nil {
		cp := *c
		return &cp
	}
	return nil
}
