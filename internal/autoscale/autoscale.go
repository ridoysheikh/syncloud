// Package autoscale is target tracking for services (§5.5): every 15s it
// reads each policy's metric from VictoriaMetrics and moves the desired task
// count towards the number that brings the metric back to its target,
// quickly out and conservatively in.
package autoscale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"syncloud/internal/events"
	"syncloud/internal/metrics"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

const (
	// Interval between evaluations.
	Interval = 15 * time.Second
	// TopicEvent carries a store.ScalingEvent when the autoscaler scales.
	TopicEvent = "scaling.event"
	// Actor is who the autoscaler's changes are attributed to.
	Actor = "autoscaler"
	// tolerance: within ±10% of the target nothing changes, so the count
	// does not flap around it.
	tolerance = 0.1
	maxTasks  = 100
)

// Metrics the policies can track and their units.
var Metrics = map[string]string{
	"cpu":     "% of reserved CPU",
	"memory":  "% of reserved memory",
	"rps":     "requests/s per task",
	"latency": "ms p95",
}

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

// Validate checks a policy and fills in defaults.
func Validate(p *store.ScalingPolicy) error {
	if _, ok := Metrics[p.Metric]; !ok {
		return ErrInvalid{errors.New("metric must be cpu, memory, rps or latency")}
	}
	if p.Min < 0 || p.Max < 1 || p.Min > p.Max || p.Max > maxTasks {
		return ErrInvalid{fmt.Errorf("min and max must satisfy 0 ≤ min ≤ max ≤ %d, max ≥ 1", maxTasks)}
	}
	if p.Target <= 0 || math.IsNaN(p.Target) || math.IsInf(p.Target, 0) {
		return ErrInvalid{errors.New("target must be a positive number")}
	}
	if (p.Metric == "cpu" || p.Metric == "memory") && p.Target > 100 {
		return ErrInvalid{errors.New("a cpu or memory target is a percentage of the reservation, at most 100")}
	}
	if p.ScaleOutCooldown == 0 {
		p.ScaleOutCooldown = 60
	}
	if p.ScaleInCooldown == 0 {
		p.ScaleInCooldown = 300
	}
	if p.ScaleInChecks == 0 {
		p.ScaleInChecks = 4 // a minute of evaluations
	}
	if p.ScaleOutCooldown < 15 || p.ScaleInCooldown < 15 || p.ScaleOutCooldown > 3600 || p.ScaleInCooldown > 3600 {
		return ErrInvalid{errors.New("cooldowns must be 15–3600 seconds")}
	}
	if p.ScaleInChecks < 1 || p.ScaleInChecks > 40 {
		return ErrInvalid{errors.New("scaleInChecks must be 1–40")}
	}
	return nil
}

// Querier reads metrics (metrics.Store).
type Querier interface {
	Instant(ctx context.Context, q string) ([]metrics.Sample, error)
	Import(ctx context.Context, text string) error
}

type state struct {
	below       int // consecutive evaluations that wanted fewer tasks
	belowWanted int // the most tasks any of them wanted
	lastChange  time.Time
	value       *float64
	evaluatedAt time.Time
}

type Manager struct {
	st  *store.Store
	wl  *workload.Manager
	m   Querier
	bus *events.Bus
	log *slog.Logger
	now func() time.Time

	mu     sync.Mutex
	states map[string]*state
}

func New(st *store.Store, wl *workload.Manager, m Querier, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{st: st, wl: wl, m: m, bus: bus, log: log, now: time.Now, states: map[string]*state{}}
}

// Current is the latest measurement of a service's policy metric.
type Current struct {
	Value       *float64   `json:"value"` // nil: no data
	EvaluatedAt *time.Time `json:"evaluatedAt"`
}

// CurrentValue returns what the last evaluation measured.
func (m *Manager) CurrentValue(serviceID string) Current {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.states[serviceID]
	if !ok || s.evaluatedAt.IsZero() {
		return Current{}
	}
	at := s.evaluatedAt
	return Current{Value: s.value, EvaluatedAt: &at}
}

// Run evaluates every policy each Interval until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.evaluateAll(ctx)
	}
}

func (m *Manager) evaluateAll(ctx context.Context) {
	policies, err := m.st.ListScalingPolicies(ctx)
	if err != nil {
		m.log.Warn("autoscaler: list policies", "err", err)
		return
	}
	var gauges strings.Builder
	active := map[string]bool{}
	for _, p := range policies {
		active[p.ServiceID] = true
		if !p.Enabled {
			continue
		}
		v, err := m.wl.ServiceView(ctx, p.ServiceID)
		if err != nil || v.Deleting {
			continue
		}
		value, ok, err := m.measure(ctx, p, v)
		if err != nil {
			m.log.Debug("autoscaler: measure", "service", v.Project+"/"+v.Name, "err", err)
		}
		if n := m.evaluate(ctx, p, v, value, ok); n != v.DesiredCount {
			v.DesiredCount = n // chart the new count right away
		}
		m.record(&gauges, p, v, value, ok)
	}
	m.mu.Lock()
	for id := range m.states {
		if !active[id] {
			delete(m.states, id)
		}
	}
	m.mu.Unlock()
	if gauges.Len() > 0 {
		if err := m.m.Import(ctx, gauges.String()); err != nil {
			m.log.Debug("autoscaler: write gauges", "err", err)
		}
	}
}

// record writes the desired and running counts and the measured value, so
// the dashboard can chart them against the target.
func (m *Manager) record(b *strings.Builder, p store.ScalingPolicy, v workload.ServiceView, value float64, ok bool) {
	labels := fmt.Sprintf(`service_id=%q,project=%q,environment=%q,app=%q`, v.ID, v.Project, v.Environment, v.Name)
	fmt.Fprintf(b, "syncloud_service_tasks{%s,kind=\"desired\"} %d\n", labels, v.DesiredCount)
	fmt.Fprintf(b, "syncloud_service_tasks{%s,kind=\"running\"} %d\n", labels, v.Running)
	fmt.Fprintf(b, "syncloud_autoscale_target{%s,metric=%q} %g\n", labels, p.Metric, p.Target)
	if ok {
		fmt.Fprintf(b, "syncloud_autoscale_value{%s,metric=%q} %g\n", labels, p.Metric, value)
	}
}

// measure reads the policy's metric over the last minute.
func (m *Manager) measure(ctx context.Context, p store.ScalingPolicy, v workload.ServiceView) (float64, bool, error) {
	sel := fmt.Sprintf(`service_id=%q`, v.ID)
	var q string
	switch p.Metric {
	case "cpu":
		q = fmt.Sprintf(`avg(avg_over_time(syncloud_task_cpu_percent{%s}[1m]))`, sel)
	case "memory":
		q = fmt.Sprintf(`avg(avg_over_time(syncloud_task_memory_bytes{%s}[1m]))`, sel)
	case "rps":
		q = fmt.Sprintf(`sum(rate(traefik_service_requests_total{%s}[1m]))`, sel)
	case "latency":
		q = fmt.Sprintf(`histogram_quantile(0.95, sum by (le) (rate(traefik_service_request_duration_seconds_bucket{%s}[1m]))) * 1000`, sel)
	}
	samples, err := m.m.Instant(ctx, q)
	if err != nil {
		return 0, false, err
	}
	if len(samples) == 0 {
		if p.Metric == "rps" && v.Running > 0 {
			return 0, true, nil // routed but idle
		}
		return 0, false, nil
	}
	val := samples[0].Value
	switch p.Metric {
	case "cpu":
		reserved := v.Spec.Resources.CPU
		if reserved <= 0 {
			reserved = 1
		}
		val /= reserved // % of one core → % of the reservation
	case "memory":
		reserved := float64(v.Spec.Resources.Memory) * 1024 * 1024
		if reserved <= 0 {
			return 0, false, nil
		}
		val = 100 * val / reserved
	case "rps":
		if v.Running == 0 {
			return 0, false, nil
		}
		val /= float64(v.Running)
	}
	return val, true, nil
}

// Decision is what one evaluation concluded.
type Decision struct {
	Desired int
	Reason  string
}

// Decide computes the desired count for a measurement. current is the
// desired count, running the running tasks; it changes no state.
func Decide(p store.ScalingPolicy, current, running int, value float64, ok bool) Decision {
	unit := Metrics[p.Metric]
	switch {
	case current < p.Min:
		return Decision{p.Min, fmt.Sprintf("below the minimum of %d tasks", p.Min)}
	case current > p.Max:
		return Decision{p.Max, fmt.Sprintf("above the maximum of %d tasks", p.Max)}
	case !ok:
		return Decision{current, "no data"}
	}
	ratio := value / p.Target
	if ratio >= 1-tolerance && ratio <= 1+tolerance {
		return Decision{current, "within 10% of the target"}
	}
	base := current
	if running > 0 && running < current {
		if ratio > 1 {
			return Decision{current, "waiting for starting tasks"}
		}
		base = running
	}
	if base == 0 {
		if ratio > 1 {
			base = 1
		} else {
			return Decision{current, "no tasks"}
		}
	}
	want := int(math.Ceil(float64(base) * ratio))
	want = min(max(want, p.Min), p.Max)
	cmp := ">"
	if ratio < 1 {
		cmp = "<"
	}
	return Decision{want, fmt.Sprintf("%s %s %s target %s %s", p.Metric, fmtValue(value), cmp, fmtValue(p.Target), unit)}
}

func fmtValue(v float64) string {
	if v >= 100 || v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// evaluate applies cooldowns and scale-in patience to a decision and
// scales the service. It returns the desired count afterwards.
func (m *Manager) evaluate(ctx context.Context, p store.ScalingPolicy, v workload.ServiceView, value float64, ok bool) int {
	now := m.now()
	m.mu.Lock()
	s, found := m.states[p.ServiceID]
	if !found {
		s = &state{}
		m.states[p.ServiceID] = s
		if evs, err := m.st.ScalingEvents(ctx, p.ServiceID, 1); err == nil && len(evs) > 0 {
			s.lastChange = evs[0].At // cooldowns survive restarts
		}
	}
	s.evaluatedAt = now
	s.value = nil
	if ok {
		val := value
		s.value = &val
	}
	d := Decide(p, v.DesiredCount, v.Running, value, ok)
	target := v.DesiredCount
	clamp := v.DesiredCount < p.Min || v.DesiredCount > p.Max
	switch {
	case d.Desired > v.DesiredCount:
		s.below, s.belowWanted = 0, 0
		if clamp || now.Sub(s.lastChange) >= time.Duration(p.ScaleOutCooldown)*time.Second {
			target = d.Desired
		}
	case d.Desired < v.DesiredCount:
		if clamp {
			target = d.Desired
			break
		}
		s.below++
		s.belowWanted = max(s.belowWanted, d.Desired)
		if s.below >= p.ScaleInChecks && now.Sub(s.lastChange) >= time.Duration(p.ScaleInCooldown)*time.Second {
			target = s.belowWanted // the most any recent evaluation wanted
			d.Reason += fmt.Sprintf(" for %d checks", s.below)
		}
	default:
		s.below, s.belowWanted = 0, 0
	}
	if target == v.DesiredCount {
		m.mu.Unlock()
		return target
	}
	s.below, s.belowWanted, s.lastChange = 0, 0, now
	m.mu.Unlock()

	if _, err := m.wl.Scale(ctx, v.ID, target, Actor); err != nil {
		m.log.Warn("autoscaler: scale", "service", v.Project+"/"+v.Name, "err", err)
		return v.DesiredCount
	}
	e := store.ScalingEvent{ServiceID: v.ID, At: now.UTC().Truncate(time.Second), From: v.DesiredCount, To: target, Metric: p.Metric,
		Target: p.Target, Reason: fmt.Sprintf("%s → %d→%d tasks", d.Reason, v.DesiredCount, target)}
	if ok {
		e.Value = &value
	}
	if err := m.st.AddScalingEvent(ctx, e); err != nil {
		m.log.Warn("autoscaler: record event", "err", err)
	}
	m.log.Info("autoscaled", "service", v.Project+"/"+v.Environment+"/"+v.Name, "reason", e.Reason)
	m.bus.Publish(TopicEvent, e)
	return target
}
