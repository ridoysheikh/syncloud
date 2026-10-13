package alerts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// System rule types (Phase 20b) watch the platform itself rather than a
// service. Their data comes from Sources, set by the controller, so this
// package does not depend on the subsystems.

// Sources feed the system rule types; a nil source skips its type.
type Sources struct {
	// Databases lists managed databases with their health.
	Databases func(ctx context.Context) ([]DatabaseState, error)
	// Components lists the system tasks (Traefik, registry, …).
	Components func() []ComponentState
	// Backup is the controller backup status.
	Backup func() BackupState
	// ControllerDisk is the controller data disk's usage.
	ControllerDisk func() (used, total uint64, err error)
}

type DatabaseState struct {
	ID, Name, Project, Environment, Engine string
	Health                                 string // healthy | degraded | starting | down | deleting
	Reason                                 string
}

type ComponentState struct {
	ID, Name, State, Error string
}

type BackupState struct {
	Configured    bool
	LastRunAt     *time.Time
	LastSuccessAt *time.Time
	LastError     string
}

// validateSystem checks the system rule types; ok is false for other types.
func (r *Rule) validateSystem() (ok bool, err error) {
	limits := map[string][3]float64{ // min, max, default threshold
		"node_overload":   {1, 100, 90},
		"backup":          {1, 720, 26},
		"certificate":     {1, 90, 14},
		"controller_disk": {1, 100, 85},
	}
	switch r.Type {
	case "node_overload", "backup", "certificate", "controller_disk", "system_component":
		r.Project, r.Environment, r.Service = "", "", ""
	case "database_failover", "database_health":
		r.Service = ""
	default:
		return false, nil
	}
	r.Op, r.Query, r.Text, r.Level = ">", "", "", ""
	if r.Type != "node_overload" {
		r.Metric, r.WindowSeconds = "", 0
	}
	l, hasThreshold := limits[r.Type]
	if !hasThreshold {
		r.Op, r.Threshold = "", 0
	} else {
		if r.Threshold == 0 {
			r.Threshold = l[2]
		}
		if r.Threshold < l[0] || r.Threshold > l[1] {
			return true, invalid("threshold must be %s–%s for %s", fmtNum(l[0]), fmtNum(l[1]), r.Type)
		}
	}
	if r.Type == "node_overload" {
		if r.Metric != "cpu" && r.Metric != "memory" && r.Metric != "disk" {
			return true, invalid("metric must be cpu, memory or disk")
		}
		if r.WindowSeconds == 0 {
			r.WindowSeconds = 300
		}
		if r.WindowSeconds < 60 || r.WindowSeconds > 86400 {
			return true, invalid("windowSeconds must be 60–86400")
		}
	}
	return true, nil
}

// observeSystem returns the instances of a system rule whose condition holds.
func (m *Manager) observeSystem(ctx context.Context, r Rule) ([]observation, error) {
	switch r.Type {
	case "node_overload":
		return m.observeNodeOverload(ctx, r)
	case "database_health":
		return m.observeDatabases(ctx, r)
	case "system_component":
		return m.observeComponents()
	case "backup":
		return m.observeBackup(r)
	case "certificate":
		return m.observeCertificates(ctx, r)
	case "controller_disk":
		return m.observeControllerDisk(r)
	}
	return nil, fmt.Errorf("unknown rule type %s", r.Type)
}

func (m *Manager) observeNodeOverload(ctx context.Context, r Rule) ([]observation, error) {
	if m.metrics == nil {
		return nil, errors.New("metrics are not enabled")
	}
	w := fmt.Sprintf("%ds", r.WindowSeconds)
	var q string
	switch r.Metric {
	case "cpu":
		q = fmt.Sprintf(`avg by (node) (avg_over_time(syncloud_node_cpu_percent[%s]))`, w)
	case "memory":
		q = fmt.Sprintf(`100 * min by (node) (min_over_time(syncloud_node_memory_used_bytes[%[1]s])) / max by (node) (max_over_time(syncloud_node_memory_total_bytes[%[1]s]))`, w)
	case "disk":
		q = `100 * max by (node) (last_over_time(syncloud_node_disk_used_bytes[5m])) / max by (node) (last_over_time(syncloud_node_disk_total_bytes[5m]))`
	}
	samples, err := m.metrics.Instant(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []observation
	for _, s := range samples {
		node := s.Labels["node"]
		if node == "" || !compare(">", s.Value, r.Threshold) {
			continue
		}
		v := s.Value
		over := ""
		if r.Metric != "disk" {
			over = fmt.Sprintf(" for %s", time.Duration(r.WindowSeconds)*time.Second)
		}
		out = append(out, observation{key: node, label: "node " + node, value: &v,
			message: fmt.Sprintf("%s at %s%% (above %s%%%s)", r.Metric, fmtNum(v), fmtNum(r.Threshold), over)})
	}
	return out, nil
}

func (m *Manager) observeDatabases(ctx context.Context, r Rule) ([]observation, error) {
	if m.Sources.Databases == nil {
		return nil, errors.New("managed databases are not enabled")
	}
	dbs, err := m.Sources.Databases(ctx)
	if err != nil {
		return nil, err
	}
	var out []observation
	for _, d := range dbs {
		if !inScope(r, d.Project, d.Environment, "") || (d.Health != "degraded" && d.Health != "down") {
			continue
		}
		msg := d.Engine + " database " + d.Health
		if d.Reason != "" {
			msg += ": " + d.Reason
		}
		out = append(out, observation{key: d.ID, label: "database " + d.Name, message: msg})
	}
	return out, nil
}

func (m *Manager) observeComponents() ([]observation, error) {
	if m.Sources.Components == nil {
		return nil, errors.New("system tasks are not enabled")
	}
	var out []observation
	for _, c := range m.Sources.Components() {
		if c.State == "running" {
			continue
		}
		msg := c.Name + " is " + c.State
		if c.Error != "" {
			msg += ": " + c.Error
		}
		out = append(out, observation{key: c.ID, label: c.Name, message: msg})
	}
	return out, nil
}

func (m *Manager) observeBackup(r Rule) ([]observation, error) {
	if m.Sources.Backup == nil {
		return nil, errors.New("backups are not available")
	}
	b := m.Sources.Backup()
	if !b.Configured {
		return nil, nil
	}
	now := m.now()
	limit := time.Duration(r.Threshold * float64(time.Hour))
	switch {
	case b.LastError != "":
		return []observation{{key: "backup", label: "controller backups", message: "the last backup failed: " + b.LastError}}, nil
	case b.LastSuccessAt == nil && b.LastRunAt != nil:
		return []observation{{key: "backup", label: "controller backups", message: "no backup has succeeded yet"}}, nil
	case b.LastSuccessAt != nil && now.Sub(*b.LastSuccessAt) > limit:
		h := now.Sub(*b.LastSuccessAt).Hours()
		return []observation{{key: "backup", label: "controller backups", value: &h,
			message: fmt.Sprintf("no backup has succeeded for %s hours (limit %s)", fmtNum(h), fmtNum(r.Threshold))}}, nil
	}
	return nil, nil
}

func (m *Manager) observeCertificates(ctx context.Context, r Rule) ([]observation, error) {
	certs, err := m.st.ListCertificates(ctx)
	if err != nil {
		return nil, err
	}
	now := m.now()
	var out []observation
	for _, c := range certs {
		if c.Issuer != "acme" { // self-signed certificates renew themselves
			continue
		}
		days := c.NotAfter.Sub(now).Hours() / 24
		switch {
		case c.Status == "failed" || c.Status == "rate_limited":
			msg := "renewal " + c.Status
			if c.LastError != "" {
				msg += ": " + c.LastError
			}
			if !c.NotAfter.IsZero() {
				msg += fmt.Sprintf(" (expires in %s days)", fmtNum(max(days, 0)))
			}
			out = append(out, observation{key: c.Host, label: c.Host, message: msg})
		case !c.NotAfter.IsZero() && days < r.Threshold:
			d := days
			out = append(out, observation{key: c.Host, label: c.Host, value: &d,
				message: fmt.Sprintf("the certificate expires in %s days (%s)", fmtNum(max(days, 0)), c.NotAfter.UTC().Format("2006-01-02"))})
		}
	}
	return out, nil
}

func (m *Manager) observeControllerDisk(r Rule) ([]observation, error) {
	if m.Sources.ControllerDisk == nil {
		return nil, errors.New("controller disk usage is not available")
	}
	used, total, err := m.Sources.ControllerDisk()
	if err != nil || total == 0 {
		return nil, err
	}
	pct := 100 * float64(used) / float64(total)
	if pct <= r.Threshold {
		return nil, nil
	}
	return []observation{{key: "controller", label: "controller disk", value: &pct,
		message: fmt.Sprintf("the controller's data disk is %s%% full (above %s%%; %s free)", fmtNum(pct), fmtNum(r.Threshold), gib(total-used))}}, nil
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

// failovers notifies each database failover in the rule's scope once.
func (m *Manager) failovers(ctx context.Context, r Rule, events []store.DatabaseEvent, dbs map[string]store.Database) {
	for _, e := range events {
		d, ok := dbs[e.DatabaseID]
		if !ok || !inScope(r, d.Project, d.Environment, "") {
			continue
		}
		msg := fmt.Sprintf("%s: the primary moved from %s to %s", d.Engine, e.From, e.To)
		if e.To == "" {
			msg = fmt.Sprintf("%s: a switchover away from %s was requested", d.Engine, e.From)
		}
		if e.Reason != "" {
			msg += " (" + e.Reason + ")"
		}
		m.notify(ctx, r, "event", store.AlertState{RuleID: r.ID, Key: fmt.Sprint(e.ID), Label: "database " + d.Name, Message: msg})
	}
}

// ── defaults (Phase 20c) ───────────────────────────────────────────────────

const (
	settingDefaults = "alerts.defaults_v1"
	// AdminsChannel is the email channel the default rules notify.
	AdminsChannel = "Admins"
)

// DefaultRules are the System rules a new install starts with.
func DefaultRules() []Rule {
	r := func(name, typ, sev string, f func(*Rule)) Rule {
		x := Rule{Name: name, Type: typ, Severity: sev, Enabled: true, System: true, RepeatHours: 12}
		if f != nil {
			f(&x)
		}
		return x
	}
	return []Rule{
		r("Node offline", "node", "critical", func(x *Rule) { x.ForSeconds = 120 }),
		r("Node CPU overload", "node_overload", "warning", func(x *Rule) { x.Metric, x.Threshold, x.WindowSeconds = "cpu", 90, 600 }),
		r("Node memory overload", "node_overload", "warning", func(x *Rule) { x.Metric, x.Threshold, x.WindowSeconds = "memory", 90, 300 }),
		r("Node disk almost full", "node_overload", "warning", func(x *Rule) { x.Metric, x.Threshold, x.WindowSeconds = "disk", 85, 300 }),
		r("Database failover", "database_failover", "warning", func(x *Rule) { x.RepeatHours = 0 }),
		r("Database down or degraded", "database_health", "critical", func(x *Rule) { x.ForSeconds = 120 }),
		r("System component down", "system_component", "critical", func(x *Rule) { x.ForSeconds = 120 }),
		r("Backups failing", "backup", "warning", func(x *Rule) { x.Threshold = 26 }),
		r("Certificate expiring", "certificate", "warning", func(x *Rule) { x.Threshold = 14; x.RepeatHours = 24 }),
		r("Controller disk almost full", "controller_disk", "warning", func(x *Rule) { x.Threshold = 85 }),
		r("Service down or degraded", "health", "critical", func(x *Rule) { x.ForSeconds = 120 }),
	}
}

// EnsureDefaults creates the Admins email channel (to email) and the System
// rules once per install. Later calls do nothing, so deleted rules stay
// deleted; RestoreDefaults brings them back.
func (m *Manager) EnsureDefaults(ctx context.Context, email string) error {
	if _, ok, err := m.st.GetSetting(ctx, settingDefaults); err != nil || ok {
		return err
	}
	if email == "" {
		return nil // before setup: try again once the root user exists
	}
	if _, err := m.RestoreDefaults(ctx, email); err != nil {
		return err
	}
	return m.st.SetSetting(ctx, settingDefaults, m.now().UTC().Format(time.RFC3339))
}

// RestoreDefaults recreates missing System rules (by name) and the Admins
// channel, and returns how many rules it added.
func (m *Manager) RestoreDefaults(ctx context.Context, email string) (int, error) {
	chans, err := m.Channels(ctx)
	if err != nil {
		return 0, err
	}
	admins := ""
	for _, c := range chans {
		if c.Name == AdminsChannel {
			admins = c.ID
		}
	}
	if admins == "" {
		if email == "" {
			return 0, invalid("no Admins channel and no address to create it with")
		}
		c, err := m.PutChannel(ctx, "", AdminsChannel, "email", ChannelConfig{To: []string{email}})
		if err != nil {
			return 0, err
		}
		admins = c.ID
	}
	rules, err := m.Rules(ctx)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, r := range rules {
		have[r.Name] = true
	}
	added := 0
	for _, r := range DefaultRules() {
		if have[r.Name] {
			continue
		}
		r.Channels = []string{admins}
		if _, err := m.PutRule(ctx, r); err != nil {
			return added, fmt.Errorf("%s: %w", r.Name, err)
		}
		added++
	}
	return added, nil
}
