// Package quota enforces per-project quotas at admission and meters usage
// (§7.2).
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/metrics"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// Limits of a project or environment. Zero means unlimited.
type Limits struct {
	CPU              float64 `json:"cpu,omitempty"`       // cores reserved by desired tasks
	MemoryMiB        int     `json:"memoryMiB,omitempty"` // MiB reserved by desired tasks
	Tasks            int     `json:"tasks,omitempty"`     // desired tasks
	Services         int     `json:"services,omitempty"`
	Jobs             int     `json:"jobs,omitempty"`
	Domains          int     `json:"domains,omitempty"` // custom domains
	ConcurrentBuilds int     `json:"concurrentBuilds,omitempty"`
	LogsMiBPerDay    int     `json:"logsMiBPerDay,omitempty"` // metered and warned, not enforced
}

// Validate rejects negative limits.
func (l Limits) Validate() error {
	if l.CPU < 0 || l.MemoryMiB < 0 || l.Tasks < 0 || l.Services < 0 || l.Jobs < 0 || l.Domains < 0 || l.ConcurrentBuilds < 0 || l.LogsMiBPerDay < 0 {
		return errors.New("limits must be zero (unlimited) or positive")
	}
	return nil
}

// Usage is what a project or environment holds now.
type Usage struct {
	CPU          float64 `json:"cpu"`
	MemoryMiB    int     `json:"memoryMiB"`
	Tasks        int     `json:"tasks"`
	Services     int     `json:"services"`
	Jobs         int     `json:"jobs"`
	Domains      int     `json:"domains"`
	Builds       int     `json:"builds"` // building now
	LogsMiBToday float64 `json:"logsMiBToday"`
}

type Manager struct {
	st      *store.Store
	wl      *workload.Manager
	metrics *metrics.Store
	log     *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	logBytes map[[2]string]int64 // project, env -> bytes since the last meter tick
}

func New(st *store.Store, wl *workload.Manager, m *metrics.Store, log *slog.Logger) *Manager {
	return &Manager{st: st, wl: wl, metrics: m, log: log, now: time.Now, logBytes: map[[2]string]int64{}}
}

// LimitsFor returns a project's limits and an environment's override.
func (m *Manager) LimitsFor(ctx context.Context, projectID, envID string) (project, env Limits, err error) {
	qs, err := m.st.ListQuotas(ctx)
	if err != nil {
		return
	}
	for _, q := range qs {
		if q.ProjectID != projectID {
			continue
		}
		switch q.EnvironmentID {
		case "":
			_ = json.Unmarshal([]byte(q.Limits), &project)
		case envID:
			_ = json.Unmarshal([]byte(q.Limits), &env)
		}
	}
	return
}

// footprint is a service's reservation.
type footprint struct {
	env          string
	cpu          float64
	mem, desired int
}

func (m *Manager) footprints(ctx context.Context, projectID string) (map[string]footprint, error) {
	envs, err := m.st.ListEnvironments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := map[string]footprint{}
	for _, e := range envs {
		svcs, err := m.st.ListServicesIn(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		for _, sv := range svcs {
			if sv.Deleting {
				continue
			}
			spec, err := m.wl.SpecFor(ctx, sv.ID, sv.Revision)
			if err != nil {
				continue
			}
			out[sv.ID] = footprint{env: e.ID, cpu: spec.Resources.CPU, mem: spec.Resources.Memory, desired: sv.DesiredCount}
		}
	}
	return out, nil
}

// UsageOf sums a project's (or, with envID, an environment's) usage.
func (m *Manager) UsageOf(ctx context.Context, projectID, envID string) (Usage, error) {
	var u Usage
	fps, err := m.footprints(ctx, projectID)
	if err != nil {
		return u, err
	}
	envOK := func(e string) bool { return envID == "" || e == envID }
	for _, f := range fps {
		if envOK(f.env) {
			u.Services++
			u.Tasks += f.desired
			u.CPU += f.cpu * float64(f.desired)
			u.MemoryMiB += f.mem * f.desired
		}
	}
	u.CPU = math.Round(u.CPU*1000) / 1000
	envs, _ := m.st.ListEnvironments(ctx, projectID)
	for _, e := range envs {
		if !envOK(e.ID) {
			continue
		}
		js, _ := m.st.ListJobsIn(ctx, e.ID)
		u.Jobs += len(js)
		svcs, _ := m.st.ListServicesIn(ctx, e.ID)
		for _, sv := range svcs {
			ds, _ := m.st.ListDomains(ctx, sv.ID)
			u.Domains += len(ds)
		}
	}
	bs, _ := m.st.BuildsByStatus(ctx, "building")
	for _, b := range bs {
		if f, ok := fps[b.ServiceID]; ok && envOK(f.env) {
			u.Builds++
		}
	}
	day := m.now().UTC().Format("2006-01-02")
	p, _ := m.projectName(ctx, projectID)
	rows, _ := m.st.ListUsage(ctx, p, day, day)
	envName := ""
	for _, e := range envs {
		if e.ID == envID {
			envName = e.Name
		}
	}
	for _, r := range rows {
		if envName == "" || r.Environment == envName {
			u.LogsMiBToday += float64(r.LogBytes) / (1 << 20)
		}
	}
	return u, nil
}

func (m *Manager) projectName(ctx context.Context, id string) (string, error) {
	var name string
	err := m.st.R.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, id).Scan(&name)
	return name, err
}

// over lists the limits u exceeds.
func over(u Usage, l Limits, scope string) []string {
	var out []string
	if l.CPU > 0 && u.CPU > l.CPU+1e-9 {
		out = append(out, fmt.Sprintf("%s CPU quota is %g cores (this needs %g)", scope, l.CPU, u.CPU))
	}
	if l.MemoryMiB > 0 && u.MemoryMiB > l.MemoryMiB {
		out = append(out, fmt.Sprintf("%s memory quota is %d MiB (this needs %d)", scope, l.MemoryMiB, u.MemoryMiB))
	}
	if l.Tasks > 0 && u.Tasks > l.Tasks {
		out = append(out, fmt.Sprintf("%s task quota is %d (this needs %d)", scope, l.Tasks, u.Tasks))
	}
	if l.Services > 0 && u.Services > l.Services {
		out = append(out, fmt.Sprintf("%s service quota is %d", scope, l.Services))
	}
	return out
}

// Admit checks a service change (workload.Manager.Admit).
func (m *Manager) Admit(ctx context.Context, req workload.AdmitRequest) error {
	env, err := m.st.EnvironmentByID(ctx, req.EnvironmentID)
	if err != nil {
		return err
	}
	pl, el, err := m.LimitsFor(ctx, env.ProjectID, env.ID)
	if err != nil {
		return err
	}
	if pl == (Limits{}) && el == (Limits{}) {
		return nil
	}
	fps, err := m.footprints(ctx, env.ProjectID)
	if err != nil {
		return err
	}
	f := footprint{env: env.ID, desired: req.Desired}
	if req.Spec != nil {
		f.cpu, f.mem = req.Spec.Resources.CPU, req.Spec.Resources.Memory
	} else if old, ok := fps[req.ServiceID]; ok {
		f.cpu, f.mem = old.cpu, old.mem
	}
	key := req.ServiceID
	if key == "" {
		key = "new"
	}
	fps[key] = f
	var pu, eu Usage
	for _, x := range fps {
		pu.Services++
		pu.Tasks += x.desired
		pu.CPU += x.cpu * float64(x.desired)
		pu.MemoryMiB += x.mem * x.desired
		if x.env == env.ID {
			eu.Services++
			eu.Tasks += x.desired
			eu.CPU += x.cpu * float64(x.desired)
			eu.MemoryMiB += x.mem * x.desired
		}
	}
	p, _ := m.projectName(ctx, env.ProjectID)
	problems := append(over(pu, pl, "project "+p), over(eu, el, "environment "+p+"/"+env.Name)...)
	if len(problems) > 0 {
		return workload.ErrQuota{Msg: "quota exceeded: " + strings.Join(problems, "; ")}
	}
	return nil
}

// AdmitCount checks adding one job or domain to an environment.
func (m *Manager) AdmitCount(ctx context.Context, envID, what string) error {
	env, err := m.st.EnvironmentByID(ctx, envID)
	if err != nil {
		return err
	}
	pl, el, err := m.LimitsFor(ctx, env.ProjectID, env.ID)
	if err != nil || (pl == (Limits{}) && el == (Limits{})) {
		return err
	}
	pu, err := m.UsageOf(ctx, env.ProjectID, "")
	if err != nil {
		return err
	}
	eu, err := m.UsageOf(ctx, env.ProjectID, env.ID)
	if err != nil {
		return err
	}
	check := func(n, limit int, scope string) error {
		if limit > 0 && n+1 > limit {
			return workload.ErrQuota{Msg: fmt.Sprintf("quota exceeded: %s %s quota is %d", scope, what, limit)}
		}
		return nil
	}
	p, _ := m.projectName(ctx, env.ProjectID)
	switch what {
	case "job":
		if err := check(pu.Jobs, pl.Jobs, "project "+p); err != nil {
			return err
		}
		return check(eu.Jobs, el.Jobs, "environment "+p+"/"+env.Name)
	case "domain":
		if err := check(pu.Domains, pl.Domains, "project "+p); err != nil {
			return err
		}
		return check(eu.Domains, el.Domains, "environment "+p+"/"+env.Name)
	}
	return nil
}

// BuildSlots tells the build queue how many builds a service's project may
// run at once (0: no limit).
func (m *Manager) BuildSlots(ctx context.Context, serviceID string) (projectID string, limit int) {
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return "", 0
	}
	env, err := m.st.EnvironmentByID(ctx, sv.EnvironmentID)
	if err != nil {
		return "", 0
	}
	pl, _, _ := m.LimitsFor(ctx, env.ProjectID, env.ID)
	return env.ProjectID, pl.ConcurrentBuilds
}

// ── metering ────────────────────────────────────────────────────────────────

// AddLogBytes counts ingested log volume (called by the log store).
func (m *Manager) AddLogBytes(project, env string, n int) {
	m.mu.Lock()
	m.logBytes[[2]string{project, env}] += int64(n)
	m.mu.Unlock()
}

// Run meters usage every minute until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	last := m.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := m.now()
			m.meter(ctx, last, now)
			last = now
		}
	}
}

func (m *Manager) meter(ctx context.Context, from, to time.Time) {
	hours := to.Sub(from).Hours()
	day := to.UTC().Format("2006-01-02")
	rows := map[[2]string]*store.UsageRow{}
	row := func(p, e string) *store.UsageRow {
		k := [2]string{p, e}
		if rows[k] == nil {
			rows[k] = &store.UsageRow{Project: p, Environment: e, Day: day}
		}
		return rows[k]
	}
	// Reserved: running tasks' reservations.
	tasks, err := m.st.ActiveTasks(ctx)
	if err == nil {
		names := map[string]store.Service{}
		for _, t := range tasks {
			if t.State != store.TaskRunning {
				continue
			}
			sv, ok := names[t.ServiceID]
			if !ok {
				sv, _ = m.st.ServiceByID(ctx, t.ServiceID)
				names[t.ServiceID] = sv
			}
			spec, err := m.wl.SpecFor(ctx, t.ServiceID, t.Revision)
			if err != nil || sv.Project == "" {
				continue
			}
			r := row(sv.Project, sv.Environment)
			r.CPUReservedHours += spec.Resources.CPU * hours
			r.MemReservedHours += float64(spec.Resources.Memory) / 1024 * hours
		}
	}
	// Used and network out, from the agents' samples.
	if m.metrics != nil {
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		for q, add := range map[string]func(*store.UsageRow, float64){
			`sum by (project, environment) (avg_over_time(syncloud_task_cpu_percent[1m])) / 100`:         func(r *store.UsageRow, v float64) { r.CPUUsedHours += v * hours },
			`sum by (project, environment) (avg_over_time(syncloud_task_memory_bytes[1m])) / 1073741824`: func(r *store.UsageRow, v float64) { r.MemUsedHours += v * hours },
			`sum by (project, environment) (rate(syncloud_task_net_tx_bytes_total[2m]))`:                 func(r *store.UsageRow, v float64) { r.NetOutBytes += int64(v * to.Sub(from).Seconds()) },
		} {
			samples, err := m.metrics.Instant(qctx, q)
			if err != nil {
				continue
			}
			for _, s := range samples {
				if p := s.Labels["project"]; p != "" && p != "syncloud" {
					add(row(p, s.Labels["environment"]), s.Value)
				}
			}
		}
		cancel()
	}
	// Build minutes: builds that finished in this window.
	if bs, err := m.st.RecentBuilds(ctx, 200, ""); err == nil {
		for _, b := range bs {
			if b.FinishedAt == nil || b.StartedAt == nil || !b.FinishedAt.After(from) || b.FinishedAt.After(to) {
				continue
			}
			if sv, err := m.st.ServiceByID(ctx, b.ServiceID); err == nil {
				row(sv.Project, sv.Environment).BuildSeconds += int64(b.FinishedAt.Sub(*b.StartedAt).Seconds())
			}
		}
	}
	m.mu.Lock()
	logs := m.logBytes
	m.logBytes = map[[2]string]int64{}
	m.mu.Unlock()
	for k, n := range logs {
		if k[0] != "" && k[0] != "syncloud" {
			row(k[0], k[1]).LogBytes += n
		}
	}
	for _, r := range rows {
		if err := m.st.AddUsage(ctx, *r); err != nil {
			m.log.Warn("record usage", "project", r.Project, "err", err)
		}
	}
}
