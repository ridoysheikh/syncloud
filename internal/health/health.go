// Package health is the controller's central health layer (§5.6): it checks
// every routed service end to end through Traefik, derives service health
// from task state and those checks, records uptime, and opens and closes
// incidents. State lives in memory; only incident transitions are stored.
package health

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/traefik"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// Service health states.
const (
	Healthy   = "healthy"
	Degraded  = "degraded"
	Down      = "down"
	Deploying = "deploying"
	Stopped   = "stopped"

	// TopicHealth carries a ServiceHealth when a service's state changes.
	TopicHealth = "service.health"

	every     = 15 * time.Second
	keep      = 24 * time.Hour
	flapLimit = 2 // consecutive evaluations before an incident opens or closes
)

// Check is one end-to-end probe result.
type Check struct {
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	Status    int       `json:"status"`
	LatencyMs float64   `json:"latencyMs"`
	Error     string    `json:"error,omitempty"`
	URL       string    `json:"url"`
}

// ServiceHealth is a service's current health for the API.
type ServiceHealth struct {
	ServiceID   string          `json:"serviceId"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Service     string          `json:"service"`
	State       string          `json:"state"`
	Reason      string          `json:"reason"`
	Serving     int             `json:"serving"`
	Desired     int             `json:"desired"`
	Uptime24h   *float64        `json:"uptime24h"` // percent; nil without checks
	Uptime7d    *float64        `json:"uptime7d"`
	Uptime30d   *float64        `json:"uptime30d"`
	LastCheck   *Check          `json:"lastCheck"`
	Latency     []LatencyPoint  `json:"latency"` // last hour, for sparklines
	Incident    *store.Incident `json:"incident"`
}

type LatencyPoint struct {
	At        int64   `json:"t"` // unix ms
	LatencyMs float64 `json:"ms"`
	OK        bool    `json:"ok"`
}

type svcState struct {
	samples    []Check
	state      string
	pending    string // candidate state awaiting flapLimit confirmations
	streak     int
	incident   *store.Incident
	reason     string
	serving    int
	desired    int
	failChecks int
}

// Config wires the monitor to the platform.
type Config struct {
	// BaseDomain returns the base domain ("" in development).
	BaseDomain func() string
	// HTTPAddr and HTTPSAddr are where Traefik listens on this host.
	HTTPAddr, HTTPSAddr string
	// VictoriaMetricsURL stores check samples for 7d/30d uptime ("" disables).
	VictoriaMetricsURL string
	// ProbeToken marks probes so Traefik keeps them out of the services'
	// traffic (traefik.Provider.ProbeToken).
	ProbeToken string
}

type Monitor struct {
	st   *store.Store
	wl   *workload.Manager
	bus  *events.Bus
	log  *slog.Logger
	cfg  Config
	http *http.Client
	vm   *http.Client
	now  func() time.Time

	mu       sync.Mutex
	services map[string]*svcState
	tasks    taskProbes
}

func New(st *store.Store, wl *workload.Manager, bus *events.Bus, log *slog.Logger, cfg Config) *Monitor {
	m := &Monitor{st: st, wl: wl, bus: bus, log: log, cfg: cfg, now: time.Now, services: map[string]*svcState{}, vm: &http.Client{Timeout: 10 * time.Second},
		tasks: taskProbes{checks: map[string]*TaskCheck{}}}
	// Every probe goes to the local Traefik, whatever the URL's host says,
	// so checks exercise routing, TLS termination and the app (§5.6).
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		target := loopback(cfg.HTTPAddr)
		if strings.HasSuffix(addr, ":443") {
			target = loopback(cfg.HTTPSAddr)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, target)
	}
	m.http = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:       dial,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // certificate validity is tracked by the certificate manager
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return m
}

func loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// Run checks and evaluates every service until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		m.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Monitor) tick(ctx context.Context) {
	svcs, err := m.st.ListServices(ctx)
	if err != nil {
		return
	}
	base := m.cfg.BaseDomain()
	routes := map[string]workload.Route{}
	for _, r := range m.wl.Routes(ctx, base) {
		if _, seen := routes[r.ServiceID]; !seen && strings.HasPrefix(r.Name, "svc-") {
			routes[r.ServiceID] = r // the default hostname of the first http port
		}
	}
	live := map[string]bool{}
	var wg sync.WaitGroup
	for _, sv := range svcs {
		if sv.Deleting {
			continue
		}
		live[sv.ID] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.evaluate(ctx, sv, routes[sv.ID], base)
		}()
	}
	wg.Wait()
	m.mu.Lock()
	for id := range m.services {
		if !live[id] {
			delete(m.services, id)
		}
	}
	m.mu.Unlock()
}

func (m *Monitor) probe(ctx context.Context, r workload.Route, base string) Check {
	scheme := "https"
	if base == "" {
		scheme = "http"
	}
	u := scheme + "://" + r.Host + "/"
	c := Check{At: m.now().UTC(), URL: u}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "syncloud-uptime/1")
	if m.cfg.ProbeToken != "" {
		req.Header.Set(traefik.ProbeHeader, m.cfg.ProbeToken)
	}
	start := time.Now()
	resp, err := m.http.Do(req)
	c.LatencyMs = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		c.Error = err.Error()
		return c
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
	resp.Body.Close()
	c.Status = resp.StatusCode
	c.OK = resp.StatusCode < 500 // the app answered; 4xx is the app's business
	if !c.OK {
		c.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return c
}

func (m *Monitor) evaluate(ctx context.Context, sv store.Service, r workload.Route, base string) {
	tasks, err := m.st.ServiceTasks(ctx, sv.ID, 5)
	if err != nil {
		return
	}
	serving := 0
	var lastStopped *store.Task
	for i, t := range tasks {
		if t.Desired == "running" && m.wl.Serving(ctx, t) {
			serving++
		}
		if t.Desired == "stopped" && lastStopped == nil && (t.State == store.TaskExited || t.State == store.TaskFailed || t.State == store.TaskLost || t.Health == "unhealthy") {
			lastStopped = &tasks[i]
		}
	}
	desired := sv.DesiredCount
	if spec, err := m.wl.SpecFor(ctx, sv.ID, sv.Revision); err == nil && spec.Image == workload.AwaitingBuild {
		desired = 0 // nothing to run until the first build is deployed
	}
	var check *Check
	if r.Host != "" && desired > 0 {
		c := m.probe(ctx, r, base)
		check = &c
		m.push(ctx, sv, c)
	}

	m.mu.Lock()
	st := m.services[sv.ID]
	if st == nil {
		st = &svcState{state: Healthy}
		if open, err := m.st.ListIncidents(ctx, sv.ID, true, 1); err == nil && len(open) == 1 {
			st.incident, st.state = &open[0], open[0].State
		}
		m.services[sv.ID] = st
	}
	if check != nil {
		st.samples = append(st.samples, *check)
		cut := 0
		for cut < len(st.samples) && check.At.Sub(st.samples[cut].At) > keep {
			cut++
		}
		st.samples = st.samples[cut:]
		if check.OK {
			st.failChecks = 0
		} else {
			st.failChecks++
		}
	}
	st.serving, st.desired = serving, sv.DesiredCount

	// Derive the state and its reason.
	dep, depErr := m.st.ActiveDeployment(ctx, sv.ID)
	deploying := depErr == nil && dep.ToRev == sv.Revision
	state, reason := Healthy, ""
	switch {
	case desired == 0:
		state = Stopped
	case deploying && serving == 0 && dep.FromRev == 0:
		state = Deploying // a new service is still starting
	case serving == 0:
		state, reason = Down, "no task is serving"
	case check != nil && st.failChecks >= 2:
		state, reason = Down, "public route check failed: "+check.Error
	case deploying:
		state = Deploying
	case serving < sv.DesiredCount:
		state, reason = Degraded, fmt.Sprintf("%d of %d tasks serving", serving, sv.DesiredCount)
	}
	if state == Down || state == Degraded {
		switch {
		case sv.Status != "":
			reason += "; " + sv.Status
		case lastStopped != nil:
			reason += "; " + taskCause(*lastStopped)
		}
	}
	st.reason = reason

	// Flap protection: a new state must hold for flapLimit evaluations.
	if state == st.state {
		st.pending, st.streak = "", 0
	} else if state == st.pending {
		st.streak++
	} else {
		st.pending, st.streak = state, 1
	}
	changed := false
	if st.pending != "" && st.streak >= flapLimit {
		prev := st.state
		st.state, st.pending, st.streak = state, "", 0
		changed = true
		m.transition(ctx, sv, st, prev, state, reason)
	}
	view := m.view(sv, st)
	m.mu.Unlock()
	if changed {
		m.bus.Publish(TopicHealth, view)
	}
}

func taskCause(t store.Task) string {
	switch {
	case t.State == store.TaskLost:
		return "a task's node was lost"
	case t.Health == "unhealthy":
		return "a task failed its health check"
	case t.ExitCode == 137:
		return "a task was killed (exit 137, often out of memory)"
	case t.Error != "":
		return "last task error: " + t.Error
	}
	return fmt.Sprintf("a task exited with code %d", t.ExitCode)
}

// transition opens, escalates or closes incidents (called with m.mu held).
func (m *Monitor) transition(ctx context.Context, sv store.Service, st *svcState, from, to, reason string) {
	now := m.now().UTC().Truncate(time.Second)
	bad := to == Down || to == Degraded
	switch {
	case bad && st.incident == nil:
		inc := store.Incident{ID: auth.NewID("inc_"), ServiceID: sv.ID, State: to, Cause: reason, OpenedAt: now}
		if err := m.st.OpenIncident(ctx, inc); err == nil {
			st.incident = &inc
		}
		m.log.Warn("incident opened", "service", sv.Project+"/"+sv.Environment+"/"+sv.Name, "state", to, "cause", reason)
	case bad && st.incident != nil:
		if to == Down || st.incident.State != Down { // keep the worst state seen
			st.incident.State = to
		}
		st.incident.Cause = reason
		_ = m.st.UpdateIncident(ctx, st.incident.ID, st.incident.State, reason)
	case !bad && st.incident != nil:
		_ = m.st.CloseIncident(ctx, st.incident.ID, now)
		m.log.Info("incident closed", "service", sv.Project+"/"+sv.Environment+"/"+sv.Name, "after", now.Sub(st.incident.OpenedAt))
		st.incident = nil
	}
	_ = from
}

func (m *Monitor) view(sv store.Service, st *svcState) ServiceHealth {
	v := ServiceHealth{ServiceID: sv.ID, Project: sv.Project, Environment: sv.Environment, Service: sv.Name, State: st.state,
		Reason: st.reason, Serving: st.serving, Desired: st.desired, Incident: st.incident, Latency: []LatencyPoint{}}
	if st.state == Healthy {
		v.Reason = ""
	}
	if n := len(st.samples); n > 0 {
		up := 0
		for _, s := range st.samples {
			if s.OK {
				up++
			}
		}
		pct := 100 * float64(up) / float64(n)
		v.Uptime24h = &pct
		last := st.samples[n-1]
		v.LastCheck = &last
		for _, s := range st.samples {
			if last.At.Sub(s.At) <= time.Hour {
				v.Latency = append(v.Latency, LatencyPoint{At: s.At.UnixMilli(), LatencyMs: s.LatencyMs, OK: s.OK})
			}
		}
	}
	return v
}

// List returns every service's health, with 7d/30d uptime from VictoriaMetrics.
func (m *Monitor) List(ctx context.Context) ([]ServiceHealth, error) {
	svcs, err := m.st.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	long7, long30 := m.longUptime(ctx, "7d"), m.longUptime(ctx, "30d")
	out := make([]ServiceHealth, 0, len(svcs))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sv := range svcs {
		st := m.services[sv.ID]
		if st == nil {
			st = &svcState{state: Healthy, desired: sv.DesiredCount}
		}
		v := m.view(sv, st)
		if p, ok := long7[sv.ID]; ok {
			v.Uptime7d = &p
		}
		if p, ok := long30[sv.ID]; ok {
			v.Uptime30d = &p
		}
		out = append(out, v)
	}
	return out, nil
}

// ── VictoriaMetrics ─────────────────────────────────────────────────────────

func (m *Monitor) push(ctx context.Context, sv store.Service, c Check) {
	if m.cfg.VictoriaMetricsURL == "" {
		return
	}
	up := 0
	if c.OK {
		up = 1
	}
	lbl := fmt.Sprintf(`{service_id=%q,project=%q,environment=%q,service=%q}`, sv.ID, sv.Project, sv.Environment, sv.Name)
	body := fmt.Sprintf("syncloud_uptime_up%s %d %d\nsyncloud_uptime_latency_seconds%s %f %d\n",
		lbl, up, c.At.UnixMilli(), lbl, c.LatencyMs/1000, c.At.UnixMilli())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.VictoriaMetricsURL+"/api/v1/import/prometheus", bytes.NewBufferString(body))
	if err != nil {
		return
	}
	if resp, err := m.vm.Do(req); err == nil {
		resp.Body.Close()
	}
}

func (m *Monitor) longUptime(ctx context.Context, window string) map[string]float64 {
	out := map[string]float64{}
	if m.cfg.VictoriaMetricsURL == "" {
		return out
	}
	q := url.Values{"query": {"avg_over_time(syncloud_uptime_up[" + window + "]) * 100"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.VictoriaMetricsURL+"/api/v1/query?"+q.Encode(), nil)
	if err != nil {
		return out
	}
	resp, err := m.vm.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var r struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil {
		return out
	}
	for _, x := range r.Data.Result {
		if s, ok := x.Value[1].(string); ok {
			var f float64
			if _, err := fmt.Sscan(s, &f); err == nil {
				out[x.Metric["service_id"]] = f
			}
		}
	}
	return out
}
