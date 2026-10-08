// Package alerts evaluates alert rules (§9) every 30 seconds and notifies
// channels when an alert fires and when it resolves. Rules watch metrics
// (resource use, request rates, errors, latency, any PromQL), log lines,
// service health and nodes; failed deployments, builds and jobs notify
// once each.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/health"
	"github.com/ridoysheikh/syncloud/internal/logs"
	"github.com/ridoysheikh/syncloud/internal/metrics"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

const (
	// Interval between evaluations.
	Interval = 30 * time.Second
	// TopicAlert carries a store.AlertEvent for every notification.
	TopicAlert = "alert.event"
)

// Rule is an alert rule as the API shows and accepts it.
type Rule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Type     string `json:"type"`     // metric | promql | log | health | node | deployment | build | job
	Severity string `json:"severity"` // info | warning | critical
	// Scope: empty matches every project, environment or service.
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	Service     string `json:"service,omitempty"`
	// metric: cpu | memory | rps | error_rate | latency.
	Metric string `json:"metric,omitempty"`
	// promql: any query; each series is an instance.
	Query string `json:"query,omitempty"`
	// log: lines containing Text (case-insensitive) and/or with Level.
	Text  string `json:"text,omitempty"`
	Level string `json:"level,omitempty"`
	// metric, promql, log: value Op Threshold over WindowSeconds.
	Op            string  `json:"op,omitempty"`
	Threshold     float64 `json:"threshold"`
	WindowSeconds int     `json:"windowSeconds,omitempty"`
	// ForSeconds is how long the condition must hold before firing.
	ForSeconds int       `json:"forSeconds"`
	Channels   []string  `json:"channels"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Types and metrics, with what they measure.
var (
	RuleTypes = map[string]string{
		"metric": "a service metric crosses a threshold", "promql": "a PromQL expression crosses a threshold",
		"log": "log lines matching a pattern", "health": "a service is degraded or down", "node": "a node stops reporting",
		"deployment": "a deployment fails", "build": "a build fails", "job": "a job run fails",
	}
	RuleMetrics = map[string]string{
		"cpu": "CPU % of one core (average per task)", "memory": "memory % of the limit (busiest task)",
		"rps": "requests per second", "error_rate": "5xx responses, % of requests", "latency": "p95 latency, ms",
	}
)

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

func invalid(format string, a ...any) error { return ErrInvalid{fmt.Errorf(format, a...)} }

// Validate checks a rule and fills in defaults.
func (r *Rule) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 100 {
		return invalid("name is required (at most 100 characters)")
	}
	if _, ok := RuleTypes[r.Type]; !ok {
		return invalid("type must be metric, promql, log, health, node, deployment, build or job")
	}
	if r.Severity == "" {
		r.Severity = "warning"
	}
	if r.Severity != "info" && r.Severity != "warning" && r.Severity != "critical" {
		return invalid("severity must be info, warning or critical")
	}
	for _, s := range []string{r.Project, r.Environment, r.Service} {
		if strings.ContainsAny(s, "\"\\\n") {
			return invalid("invalid scope %q", s)
		}
	}
	switch r.Type {
	case "metric", "promql", "log":
		if r.Op == "" {
			r.Op = ">"
		}
		if r.Op != ">" && r.Op != "<" {
			return invalid("op must be > or <")
		}
		if r.WindowSeconds == 0 {
			r.WindowSeconds = 300
		}
		if r.WindowSeconds < 60 || r.WindowSeconds > 86400 {
			return invalid("windowSeconds must be 60–86400")
		}
		if math.IsNaN(r.Threshold) || math.IsInf(r.Threshold, 0) {
			return invalid("threshold must be a number")
		}
	default:
		r.Op, r.Threshold, r.WindowSeconds = "", 0, 0
	}
	switch r.Type {
	case "metric":
		if _, ok := RuleMetrics[r.Metric]; !ok {
			return invalid("metric must be cpu, memory, rps, error_rate or latency")
		}
	case "promql":
		if strings.TrimSpace(r.Query) == "" {
			return invalid("query is required")
		}
	case "log":
		if r.Text == "" && r.Level == "" {
			return invalid("give text, level or both")
		}
		if r.Level != "" && r.Level != "error" && r.Level != "warn" && r.Level != "fatal" {
			return invalid("level must be error, warn or fatal")
		}
	}
	if r.ForSeconds < 0 || r.ForSeconds > 86400 {
		return invalid("forSeconds must be 0–86400")
	}
	if r.Channels == nil {
		r.Channels = []string{}
	}
	return nil
}

// MetricsSource reads metrics (metrics.Store).
type MetricsSource interface {
	Instant(ctx context.Context, q string) ([]metrics.Sample, error)
}

// LogsSource counts log lines (logs.Store).
type LogsSource interface {
	Count(ctx context.Context, f logs.Filter, level string, window time.Duration) ([]logs.Count, error)
}

// HealthSource lists service health (health.Monitor).
type HealthSource interface {
	List(ctx context.Context) ([]health.ServiceHealth, error)
}

type Manager struct {
	st      *store.Store
	box     *secrets.Box
	metrics MetricsSource
	logs    LogsSource
	health  HealthSource
	bus     *events.Bus
	log     *slog.Logger
	now     func() time.Time
	// DashboardURL links notifications to the dashboard.
	DashboardURL func() string

	mu     sync.Mutex
	states map[string]map[string]*store.AlertState // rule ID → key
	cursor time.Time                               // failures after this were notified
}

func New(st *store.Store, box *secrets.Box, m MetricsSource, l LogsSource, h HealthSource, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{st: st, box: box, metrics: m, logs: l, health: h, bus: bus, log: log, now: time.Now,
		states: map[string]map[string]*store.AlertState{}}
}

// ── rules ──────────────────────────────────────────────────────────────────

func ruleFrom(r store.AlertRule) (Rule, error) {
	var x Rule
	if err := json.Unmarshal([]byte(r.Spec), &x); err != nil {
		return x, err
	}
	x.ID, x.Name, x.Enabled, x.CreatedAt, x.UpdatedAt = r.ID, r.Name, r.Enabled, r.CreatedAt, r.UpdatedAt
	if x.Channels == nil {
		x.Channels = []string{}
	}
	return x, nil
}

func (m *Manager) Rules(ctx context.Context) ([]Rule, error) {
	rs, err := m.st.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rs))
	for _, r := range rs {
		x, err := ruleFrom(r)
		if err != nil {
			m.log.Warn("alerts: bad rule", "rule", r.Name, "err", err)
			continue
		}
		out = append(out, x)
	}
	return out, nil
}

// PutRule creates (empty ID) or replaces a rule.
func (m *Manager) PutRule(ctx context.Context, r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	chans, err := m.st.ListAlertChannels(ctx)
	if err != nil {
		return Rule{}, err
	}
	known := map[string]bool{}
	for _, c := range chans {
		known[c.ID] = true
	}
	for _, id := range r.Channels {
		if !known[id] {
			return Rule{}, invalid("unknown channel %s", id)
		}
	}
	now := m.now().UTC().Truncate(time.Second)
	if r.ID == "" {
		r.ID, r.CreatedAt = auth.NewID("alr_"), now
	}
	r.UpdatedAt = now
	spec, _ := json.Marshal(r)
	if err := m.st.PutAlertRule(ctx, store.AlertRule{ID: r.ID, Name: r.Name, Spec: string(spec), Enabled: r.Enabled, CreatedAt: r.CreatedAt, UpdatedAt: now}); err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			return Rule{}, invalid("a rule named %q exists", r.Name)
		}
		return Rule{}, err
	}
	// A changed rule starts over: open alerts of the old definition resolve
	// silently and fire again if they still hold.
	m.mu.Lock()
	for key := range m.states[r.ID] {
		_ = m.st.DeleteAlertState(ctx, r.ID, key)
	}
	delete(m.states, r.ID)
	m.mu.Unlock()
	return r, nil
}

func (m *Manager) DeleteRule(ctx context.Context, id string) error {
	if err := m.st.DeleteAlertRule(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.states, id)
	m.mu.Unlock()
	return nil
}

// ── channels ───────────────────────────────────────────────────────────────

// Channel is a notification channel as the API shows it (no secrets).
type Channel struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"createdAt"`
}

func (m *Manager) Channels(ctx context.Context) ([]Channel, error) {
	cs, err := m.st.ListAlertChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(cs))
	for _, c := range cs {
		out = append(out, Channel{ID: c.ID, Name: c.Name, Type: c.Type, Summary: c.Summary, CreatedAt: c.CreatedAt})
	}
	return out, nil
}

// PutChannel creates (empty id) or replaces a channel.
func (m *Manager) PutChannel(ctx context.Context, id, name, typ string, cfg ChannelConfig) (Channel, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 {
		return Channel{}, invalid("name is required (at most 60 characters)")
	}
	summary, err := cfg.Validate(typ)
	if err != nil {
		return Channel{}, ErrInvalid{err}
	}
	c := store.AlertChannel{ID: id, Name: name, Type: typ, Summary: summary, CreatedAt: m.now().UTC()}
	if id == "" {
		c.ID = auth.NewID("ach_")
	} else if old, err := m.st.AlertChannel(ctx, id); err != nil {
		return Channel{}, err
	} else {
		c.CreatedAt = old.CreatedAt
	}
	b, _ := json.Marshal(cfg)
	c.ConfigEnc = m.box.Seal(b, []byte("alert-channel:"+c.ID))
	if err := m.st.PutAlertChannel(ctx, c); errors.Is(err, store.ErrNameTaken) {
		return Channel{}, invalid("a channel named %q exists", name)
	} else if err != nil {
		return Channel{}, err
	}
	return Channel{ID: c.ID, Name: c.Name, Type: c.Type, Summary: c.Summary, CreatedAt: c.CreatedAt}, nil
}

func (m *Manager) DeleteChannel(ctx context.Context, id string) error {
	rules, err := m.Rules(ctx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		for _, c := range r.Channels {
			if c == id {
				return invalid("rule %q uses this channel", r.Name)
			}
		}
	}
	return m.st.DeleteAlertChannel(ctx, id)
}

// TestChannel sends a test notification and returns the delivery error.
func (m *Manager) TestChannel(ctx context.Context, id string) error {
	c, err := m.st.AlertChannel(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := m.openConfig(c)
	if err != nil {
		return err
	}
	n := Notification{Kind: "test", Rule: "Test notification", Severity: "info", Instance: c.Name,
		Message: "Notifications from SynCloud reach this channel.", At: m.now().UTC(), URL: m.dashboardURL()}
	return Send(ctx, c.Type, cfg, n)
}

func (m *Manager) openConfig(c store.AlertChannel) (ChannelConfig, error) {
	var cfg ChannelConfig
	b, err := m.box.Open(c.ConfigEnc, []byte("alert-channel:"+c.ID))
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(b, &cfg)
}

func (m *Manager) dashboardURL() string {
	if m.DashboardURL == nil {
		return ""
	}
	return m.DashboardURL()
}

// ── evaluation ─────────────────────────────────────────────────────────────

// Active returns the alerts whose condition holds, firing first.
func (m *Manager) Active(ctx context.Context) ([]store.AlertState, error) {
	states, err := m.st.ListAlertStates(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(states, func(i, j int) bool { return states[i].State == "firing" && states[j].State != "firing" })
	return states, nil
}

// Run evaluates every rule each Interval until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	if states, err := m.st.ListAlertStates(ctx); err == nil {
		m.mu.Lock()
		for i := range states {
			s := states[i]
			if m.states[s.RuleID] == nil {
				m.states[s.RuleID] = map[string]*store.AlertState{}
			}
			m.states[s.RuleID][s.Key] = &s
		}
		m.mu.Unlock()
	}
	m.cursor = m.now()
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.Evaluate(ctx)
	}
}

// observation is one instance of a rule with its condition.
type observation struct {
	key, label, message string
	value               *float64
}

// Evaluate checks every enabled rule once.
func (m *Manager) Evaluate(ctx context.Context) {
	rules, err := m.Rules(ctx)
	if err != nil {
		m.log.Warn("alerts: list rules", "err", err)
		return
	}
	services := map[string]store.Service{}
	if svcs, err := m.st.ListServices(ctx); err == nil {
		for _, s := range svcs {
			services[s.ID] = s
		}
	}
	now := m.now()
	since := m.cursor
	failures, ferr := m.st.FailedSince(ctx, since)
	if ferr == nil {
		m.cursor = now
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		switch r.Type {
		case "deployment", "build", "job":
			if ferr == nil {
				m.events(ctx, r, failures, services)
			}
			continue
		}
		obs, err := m.observe(ctx, r, services)
		if err != nil {
			m.log.Debug("alerts: evaluate", "rule", r.Name, "err", err)
			continue // unknown is not resolved
		}
		m.transition(ctx, r, obs, now)
	}
}

// transition moves each instance through pending → firing → resolved.
func (m *Manager) transition(ctx context.Context, r Rule, obs []observation, now time.Time) {
	m.mu.Lock()
	cur := m.states[r.ID]
	if cur == nil {
		cur = map[string]*store.AlertState{}
		m.states[r.ID] = cur
	}
	m.mu.Unlock()
	seen := map[string]bool{}
	for _, o := range obs {
		seen[o.key] = true
		s, ok := cur[o.key]
		if !ok {
			s = &store.AlertState{RuleID: r.ID, Key: o.key, State: "pending", Since: now.UTC().Truncate(time.Second)}
			cur[o.key] = s
		}
		s.Label, s.Value, s.Message = o.label, o.value, o.message
		if s.State == "pending" && now.Sub(s.Since) >= time.Duration(r.ForSeconds)*time.Second {
			s.State = "firing"
			m.notify(ctx, r, "firing", *s)
		}
		_ = m.st.PutAlertState(ctx, *s)
	}
	for key, s := range cur {
		if seen[key] {
			continue
		}
		if s.State == "firing" {
			res := *s
			res.Message = "resolved: " + s.Message
			m.notify(ctx, r, "resolved", res)
		}
		delete(cur, key)
		_ = m.st.DeleteAlertState(ctx, r.ID, key)
	}
}

// notify records a notification and delivers it to the rule's channels in
// the background.
func (m *Manager) notify(ctx context.Context, r Rule, kind string, s store.AlertState) {
	e := store.AlertEvent{RuleID: r.ID, RuleName: r.Name, Severity: r.Severity, Kind: kind, Key: s.Key, Label: s.Label,
		Message: s.Message, Value: s.Value, At: m.now().UTC().Truncate(time.Second)}
	id, err := m.st.AddAlertEvent(ctx, e)
	if err != nil {
		m.log.Warn("alerts: record", "err", err)
	}
	e.ID = id
	m.log.Info("alert", "rule", r.Name, "kind", kind, "instance", s.Label, "message", s.Message)
	m.bus.Publish(TopicAlert, e)
	n := Notification{Kind: kind, Rule: r.Name, Severity: r.Severity, Instance: s.Label, Message: s.Message, Value: s.Value,
		At: e.At, URL: m.dashboardURL()}
	if len(r.Channels) == 0 {
		return
	}
	go func() {
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var results []string
		for _, id := range r.Channels {
			c, err := m.st.AlertChannel(dctx, id)
			if err != nil {
				continue
			}
			cfg, err := m.openConfig(c)
			if err == nil {
				err = Send(dctx, c.Type, cfg, n)
			}
			if err != nil {
				results = append(results, c.Name+": "+err.Error())
				m.log.Warn("alerts: deliver", "channel", c.Name, "err", err)
			} else {
				results = append(results, c.Name+": ok")
			}
		}
		if e.ID != 0 {
			_ = m.st.SetAlertDelivery(dctx, e.ID, strings.Join(results, "; "))
		}
	}()
}

func inScope(r Rule, project, env, service string) bool {
	return (r.Project == "" || r.Project == project) && (r.Environment == "" || r.Environment == env) && (r.Service == "" || r.Service == service)
}

func svcLabel(s store.Service) string { return s.Project + "/" + s.Environment + "/" + s.Name }

// events notifies each failure in the rule's scope once.
func (m *Manager) events(ctx context.Context, r Rule, failures []store.Failure, services map[string]store.Service) {
	for _, f := range failures {
		if f.Kind != r.Type {
			continue
		}
		sv, ok := services[f.ServiceID]
		label := ""
		if ok {
			if !inScope(r, sv.Project, sv.Environment, sv.Name) {
				continue
			}
			label = svcLabel(sv)
		} else if r.Project != "" || r.Environment != "" || r.Service != "" {
			continue
		}
		if f.JobName != "" {
			label = strings.TrimSuffix(label, "/"+sv.Name)
			if label == "" {
				label = "job"
			}
			label += " job " + f.JobName
		}
		msg := fmt.Sprintf("%s %s failed", f.Kind, f.ID)
		if f.Message != "" {
			msg += ": " + f.Message
		}
		m.notify(ctx, r, "event", store.AlertState{RuleID: r.ID, Key: f.ID, Label: label, Message: msg})
	}
}

func compare(op string, v, threshold float64) bool {
	if op == "<" {
		return v < threshold
	}
	return v > threshold
}

func fmtNum(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e9 {
		return fmt.Sprintf("%.0f", v)
	}
	if math.Abs(v) >= 100 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// observe returns the instances of r whose condition holds now.
func (m *Manager) observe(ctx context.Context, r Rule, services map[string]store.Service) ([]observation, error) {
	switch r.Type {
	case "metric":
		return m.observeMetric(ctx, r, services)
	case "promql":
		return m.observePromQL(ctx, r)
	case "log":
		return m.observeLogs(ctx, r)
	case "health":
		return m.observeHealth(ctx, r)
	case "node":
		return m.observeNodes(ctx)
	}
	return nil, fmt.Errorf("unknown rule type %s", r.Type)
}

func (m *Manager) observeMetric(ctx context.Context, r Rule, services map[string]store.Service) ([]observation, error) {
	if m.metrics == nil {
		return nil, errors.New("metrics are not enabled")
	}
	sel := func(name string) string {
		var parts []string
		for _, kv := range [][2]string{{"project", r.Project}, {"environment", r.Environment}, {name, r.Service}} {
			if kv[1] != "" {
				parts = append(parts, fmt.Sprintf("%s=%q", kv[0], kv[1]))
			}
		}
		parts = append(parts, `service_id!=""`)
		return strings.Join(parts, ",")
	}
	task, edge := sel("service"), sel("app")
	w := fmt.Sprintf("%ds", r.WindowSeconds)
	var q, unit string
	switch r.Metric {
	case "cpu":
		q, unit = fmt.Sprintf(`avg by (service_id) (avg_over_time(syncloud_task_cpu_percent{%s}[%s]))`, task, w), "%"
	case "memory":
		q, unit = fmt.Sprintf(`max by (service_id) (100 * max_over_time(syncloud_task_memory_bytes{%[1]s}[%[2]s]) / max_over_time(syncloud_task_memory_limit_bytes{%[1]s}[%[2]s]))`, task, w), "%"
	case "rps":
		q, unit = fmt.Sprintf(`sum by (service_id) (rate(traefik_service_requests_total{%s}[%s]))`, edge, w), " req/s"
	case "error_rate":
		q, unit = fmt.Sprintf(`100 * sum by (service_id) (rate(traefik_service_requests_total{%[1]s,code=~"5.."}[%[2]s])) / sum by (service_id) (rate(traefik_service_requests_total{%[1]s}[%[2]s]))`, edge, w), "%"
	case "latency":
		q, unit = fmt.Sprintf(`histogram_quantile(0.95, sum by (le, service_id) (rate(traefik_service_request_duration_seconds_bucket{%s}[%s]))) * 1000`, edge, w), "ms"
	}
	samples, err := m.metrics.Instant(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []observation
	for _, s := range samples {
		id := s.Labels["service_id"]
		sv, ok := services[id]
		if !ok || !compare(r.Op, s.Value, r.Threshold) {
			continue
		}
		v := s.Value
		out = append(out, observation{key: id, label: svcLabel(sv), value: &v,
			message: fmt.Sprintf("%s %s%s %s %s%s (over %s)", r.Metric, fmtNum(v), unit, r.Op, fmtNum(r.Threshold), unit, time.Duration(r.WindowSeconds)*time.Second)})
	}
	return out, nil
}

func (m *Manager) observePromQL(ctx context.Context, r Rule) ([]observation, error) {
	if m.metrics == nil {
		return nil, errors.New("metrics are not enabled")
	}
	samples, err := m.metrics.Instant(ctx, r.Query)
	if err != nil {
		return nil, err
	}
	var out []observation
	for _, s := range samples {
		if !compare(r.Op, s.Value, r.Threshold) {
			continue
		}
		keys := make([]string, 0, len(s.Labels))
		for k := range s.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+"="+s.Labels[k])
		}
		label := strings.Join(parts, ",")
		if label == "" {
			label = "value"
		}
		v := s.Value
		out = append(out, observation{key: label, label: label, value: &v, message: fmt.Sprintf("%s %s %s", fmtNum(v), r.Op, fmtNum(r.Threshold))})
	}
	return out, nil
}

func (m *Manager) observeLogs(ctx context.Context, r Rule) ([]observation, error) {
	if m.logs == nil {
		return nil, errors.New("logs are not enabled")
	}
	window := time.Duration(r.WindowSeconds) * time.Second
	counts, err := m.logs.Count(ctx, logs.Filter{Project: r.Project, Environment: r.Environment, Service: r.Service, Text: r.Text}, r.Level, window)
	if err != nil {
		return nil, err
	}
	what := "lines"
	switch {
	case r.Text != "" && r.Level != "":
		what = fmt.Sprintf("%s lines containing %q", r.Level, r.Text)
	case r.Text != "":
		what = fmt.Sprintf("lines containing %q", r.Text)
	default:
		what = r.Level + " lines"
	}
	byKey := map[string]logs.Count{}
	for _, c := range counts {
		byKey[c.Project+"/"+c.Environment+"/"+c.Service] = c
	}
	var out []observation
	// With "<", a scoped service that logged nothing also matches.
	if r.Op == "<" && r.Service != "" && r.Project != "" {
		k := r.Project + "/" + orDefault(r.Environment, "production") + "/" + r.Service
		if _, ok := byKey[k]; !ok {
			byKey[k] = logs.Count{Project: r.Project, Environment: orDefault(r.Environment, "production"), Service: r.Service}
		}
	}
	for k, c := range byKey {
		v := float64(c.Lines)
		if !compare(r.Op, v, r.Threshold) {
			continue
		}
		out = append(out, observation{key: k, label: k, value: &v,
			message: fmt.Sprintf("%d %s in %s (%s %s)", c.Lines, what, window, r.Op, fmtNum(r.Threshold))})
	}
	return out, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (m *Manager) observeHealth(ctx context.Context, r Rule) ([]observation, error) {
	if m.health == nil {
		return nil, errors.New("health monitoring is not enabled")
	}
	list, err := m.health.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []observation
	for _, h := range list {
		if !inScope(r, h.Project, h.Environment, h.Service) || (h.State != health.Degraded && h.State != health.Down) {
			continue
		}
		msg := h.State
		if h.Reason != "" {
			msg += ": " + h.Reason
		}
		msg += fmt.Sprintf(" (%d of %d tasks serving)", h.Serving, h.Desired)
		out = append(out, observation{key: h.ServiceID, label: h.Project + "/" + h.Environment + "/" + h.Service, message: msg})
	}
	return out, nil
}

func (m *Manager) observeNodes(ctx context.Context) ([]observation, error) {
	nodes, err := m.st.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	var out []observation
	for _, n := range nodes {
		if n.Status != store.NodeNotReady {
			continue
		}
		msg := "not reporting"
		if n.LastSeenAt != nil {
			msg += " since " + n.LastSeenAt.UTC().Format(time.RFC3339)
		}
		out = append(out, observation{key: n.ID, label: "node " + n.Name, message: msg})
	}
	return out, nil
}
