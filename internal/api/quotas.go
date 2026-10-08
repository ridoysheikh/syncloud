package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ridoysheikh/syncloud/internal/quota"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Quotas and usage (§7.2).

type quotaView struct {
	Project     string       `json:"project"`
	Environment string       `json:"environment,omitempty"` // "" = the whole project
	Limits      quota.Limits `json:"limits"`
	Usage       quota.Usage  `json:"usage"`
	// Warnings name limits at 80% or more.
	Warnings []string `json:"warnings"`
}

func warnings(u quota.Usage, l quota.Limits) []string {
	out := []string{}
	check := func(name string, used, limit float64) {
		if limit <= 0 {
			return
		}
		pct := used / limit * 100
		if pct >= 100 {
			out = append(out, fmt.Sprintf("%s at %.0f%% of its quota", name, pct))
		} else if pct >= 80 {
			out = append(out, fmt.Sprintf("%s at %.0f%% of its quota", name, pct))
		}
	}
	check("CPU", u.CPU, l.CPU)
	check("memory", float64(u.MemoryMiB), float64(l.MemoryMiB))
	check("tasks", float64(u.Tasks), float64(l.Tasks))
	check("services", float64(u.Services), float64(l.Services))
	check("jobs", float64(u.Jobs), float64(l.Jobs))
	check("domains", float64(u.Domains), float64(l.Domains))
	check("builds", float64(u.Builds), float64(l.ConcurrentBuilds))
	check("logs today", u.LogsMiBToday, float64(l.LogsMiBPerDay))
	return out
}

func (s *Server) requireQuotas(w http.ResponseWriter) bool {
	if s.quotas == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "quotas are not enabled")
		return false
	}
	return true
}

func (s *Server) quotaViews(r *http.Request, p store.Project) ([]quotaView, error) {
	ctx := r.Context()
	envs, err := s.store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	pl, _, err := s.quotas.LimitsFor(ctx, p.ID, "")
	if err != nil {
		return nil, err
	}
	pu, err := s.quotas.UsageOf(ctx, p.ID, "")
	if err != nil {
		return nil, err
	}
	out := []quotaView{{Project: p.Name, Limits: pl, Usage: pu, Warnings: warnings(pu, pl)}}
	for _, e := range envs {
		_, el, err := s.quotas.LimitsFor(ctx, p.ID, e.ID)
		if err != nil {
			return nil, err
		}
		eu, err := s.quotas.UsageOf(ctx, p.ID, e.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, quotaView{Project: p.Name, Environment: e.Name, Limits: el, Usage: eu, Warnings: warnings(eu, el)})
	}
	return out, nil
}

// handleListQuotas lists every project's quota and usage.
func (s *Server) handleListQuotas(w http.ResponseWriter, r *http.Request) {
	if !s.requireQuotas(w) {
		return
	}
	ps, err := s.store.ListProjects(r.Context())
	if err != nil {
		s.internalError(w, "list projects", err)
		return
	}
	out := []quotaView{}
	for _, p := range ps {
		vs, err := s.quotaViews(r, p)
		if err != nil {
			s.internalError(w, "quota", err)
			return
		}
		out = append(out, vs...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, out, itemProjectField)})
}

func (s *Server) handleGetQuota(w http.ResponseWriter, r *http.Request) {
	if !s.requireQuotas(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	vs, err := s.quotaViews(r, p)
	if err != nil {
		s.internalError(w, "quota", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": vs})
}

func (s *Server) putQuota(w http.ResponseWriter, r *http.Request, envID string) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var l quota.Limits
	if !decodeJSON(w, r, &l) {
		return
	}
	if err := l.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	b, _ := json.Marshal(l)
	if err := s.store.PutQuota(r.Context(), store.Quota{ProjectID: p.ID, EnvironmentID: envID, Limits: string(b), UpdatedAt: s.now()}); err != nil {
		s.internalError(w, "save quota", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "quota:SetQuota", s.resourceOf(r, ""), map[string]any{"limits": l})
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) handlePutQuota(w http.ResponseWriter, r *http.Request) {
	if s.requireQuotas(w) {
		s.putQuota(w, r, "")
	}
}

func (s *Server) handlePutEnvironmentQuota(w http.ResponseWriter, r *http.Request) {
	if !s.requireQuotas(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	s.putQuota(w, r, e.ID)
}

func (s *Server) handleDeleteQuota(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	envID := ""
	if r.PathValue("env") != "" {
		e, ok := s.environment(w, r)
		if !ok {
			return
		}
		envID = e.ID
	}
	if err := s.store.DeleteQuota(r.Context(), p.ID, envID); err != nil {
		s.internalError(w, "delete quota", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "quota:DeleteQuota", s.resourceOf(r, ""), nil)
	w.WriteHeader(http.StatusNoContent)
}

// usageRange reads from/to (YYYY-MM-DD) or month (YYYY-MM); default: the
// last 30 days.
func (s *Server) usageRange(r *http.Request) (string, string, error) {
	q := r.URL.Query()
	if m := q.Get("month"); m != "" {
		t, err := time.Parse("2006-01", m)
		if err != nil {
			return "", "", errors.New("month must be YYYY-MM")
		}
		return t.Format("2006-01-02"), t.AddDate(0, 1, -1).Format("2006-01-02"), nil
	}
	to := s.now().UTC()
	from := to.AddDate(0, 0, -29)
	for k, dst := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := q.Get(k); v != "" {
			t, err := time.Parse("2006-01-02", v)
			if err != nil {
				return "", "", errors.New(k + " must be YYYY-MM-DD")
			}
			*dst = t
		}
	}
	return from.Format("2006-01-02"), to.Format("2006-01-02"), nil
}

// handleGetUsage returns metered usage per project, environment and day.
func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.usageRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	rows, err := s.store.ListUsage(r.Context(), r.URL.Query().Get("project"), from, to)
	if err != nil {
		s.internalError(w, "list usage", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "items": s.filterItems(r, rows, itemProjectField)})
}

// handleExportUsage is the usage report as CSV.
func (s *Server) handleExportUsage(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.usageRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	rows, err := s.store.ListUsage(r.Context(), r.URL.Query().Get("project"), from, to)
	if err != nil {
		s.internalError(w, "list usage", err)
		return
	}
	filtered := s.filterItems(r, rows, itemProjectField)
	b, _ := json.Marshal(filtered)
	var list []store.UsageRow
	_ = json.Unmarshal(b, &list)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="syncloud-usage-%s-%s.csv"`, from, to))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"day", "project", "environment", "cpu_reserved_core_hours", "memory_reserved_gib_hours", "cpu_used_core_hours", "memory_used_gib_hours", "network_out_bytes", "log_bytes", "build_minutes"})
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
	for _, u := range list {
		_ = cw.Write([]string{u.Day, u.Project, u.Environment, f(u.CPUReservedHours), f(u.MemReservedHours), f(u.CPUUsedHours), f(u.MemUsedHours),
			strconv.FormatInt(u.NetOutBytes, 10), strconv.FormatInt(u.LogBytes, 10), f(float64(u.BuildSeconds) / 60)})
	}
	cw.Flush()
}
