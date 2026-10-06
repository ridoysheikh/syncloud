package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"syncloud/internal/metrics"
	"syncloud/internal/store"
)

// summaryWindow is how far back traffic tables and the map look.
const summaryWindow = 5 * time.Minute

func (s *Server) metricsErr(w http.ResponseWriter, err error) {
	if errors.Is(err, metrics.ErrDisabled) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	writeError(w, http.StatusServiceUnavailable, CodeInternal, err.Error())
}

func (s *Server) writeTraffic(w http.ResponseWriter, r *http.Request, sc metrics.TrafficScope) {
	if s.metrics == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "metrics are not enabled")
		return
	}
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	res, err := s.metrics.Traffic(r.Context(), sc, metrics.Ranges[rng], s.now())
	if err != nil {
		s.metricsErr(w, err)
		return
	}
	routes, err := s.metrics.TrafficSummary(r.Context(), sc, summaryWindow)
	if err != nil {
		s.metricsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"start": res.Start, "end": res.End, "stepSeconds": res.Step, "charts": res.Charts,
		"routes": routes, "windowSeconds": int(summaryWindow.Seconds())})
}

// handleTraffic charts every route: requests by status, latency, bandwidth
// and requests per service, plus a table of the last 5 minutes (§5.7).
func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	s.writeTraffic(w, r, metrics.TrafficScope{})
}

func (s *Server) handleEnvironmentTraffic(w http.ResponseWriter, r *http.Request) {
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	s.writeTraffic(w, r, metrics.TrafficScope{Project: r.PathValue("project"), Environment: e.Name})
}

func (s *Server) handleServiceTraffic(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	s.writeTraffic(w, r, metrics.TrafficScope{ServiceID: sv.ID})
}

type mapTask struct {
	ID       string  `json:"id"`
	Node     string  `json:"node"`
	IP       string  `json:"ip"`
	State    string  `json:"state"`
	Health   string  `json:"health"`
	Revision int     `json:"revision"`
	RPS      float64 `json:"rps"`
}

type mapRoute struct {
	metrics.RouteTraffic
	Hosts []string  `json:"hosts"`
	Tasks []mapTask `json:"tasks"`
}

// handleTrafficMap is the live traffic map: each routed service with its
// hostnames, its traffic over the last minutes and the tasks that answered.
func (s *Server) handleTrafficMap(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil || s.workloads == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "metrics are not enabled")
		return
	}
	ctx := r.Context()
	base := ""
	if s.domains != nil {
		base = s.domains.Base()
	}
	summary, err := s.metrics.TrafficSummary(ctx, metrics.TrafficScope{}, summaryWindow)
	if err != nil {
		s.metricsErr(w, err)
		return
	}
	byID := map[string]metrics.RouteTraffic{}
	for _, rt := range summary {
		if rt.ServiceID != "" {
			byID[rt.ServiceID] = rt
		}
	}
	// Which task answered comes from the access log: each task gets its
	// share of the service's request rate, so the map's flows add up.
	perTask, perService := map[string]float64{}, map[string]float64{}
	if s.logs != nil {
		if counts, err := s.logs.UpstreamCounts(ctx, summaryWindow); err == nil {
			for _, c := range counts {
				perTask[c.ServiceID+"|"+hostOf(c.Upstream)] += float64(c.Requests)
				perService[c.ServiceID] += float64(c.Requests)
			}
		}
	}
	taskRPS := func(serviceID, ip string, rps float64) float64 {
		if perService[serviceID] == 0 {
			return 0
		}
		return rps * perTask[serviceID+"|"+ip] / perService[serviceID]
	}
	tasks, err := s.store.ActiveTasks(ctx)
	if err != nil {
		s.internalError(w, "list tasks", err)
		return
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.internalError(w, "list nodes", err)
		return
	}
	nodeName := map[string]string{}
	for _, n := range nodes {
		nodeName[n.ID] = n.Name
	}
	byService := map[string][]store.Task{}
	for _, t := range tasks {
		byService[t.ServiceID] = append(byService[t.ServiceID], t)
	}
	routes := map[string]*mapRoute{}
	var order []string
	for _, rt := range s.workloads.Routes(ctx, base) {
		m, ok := routes[rt.ServiceID]
		if !ok {
			sv, err := s.store.ServiceByID(ctx, rt.ServiceID)
			if err != nil {
				continue
			}
			tr := byID[rt.ServiceID]
			tr.ServiceID, tr.Project, tr.Environment, tr.Service = sv.ID, sv.Project, sv.Environment, sv.Name
			m = &mapRoute{RouteTraffic: tr, Hosts: []string{}, Tasks: []mapTask{}}
			for _, t := range byService[sv.ID] {
				m.Tasks = append(m.Tasks, mapTask{ID: t.ID, Node: nodeName[t.NodeID], IP: t.IP, State: t.State, Health: t.Health,
					Revision: t.Revision, RPS: taskRPS(sv.ID, t.IP, tr.RPS)})
			}
			routes[rt.ServiceID] = m
			order = append(order, rt.ServiceID)
		}
		m.Hosts = append(m.Hosts, rt.Host)
	}
	out := make([]mapRoute, 0, len(order))
	for _, id := range order {
		out = append(out, *routes[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RPS > out[j].RPS })
	writeJSON(w, http.StatusOK, map[string]any{"routes": out, "windowSeconds": int(summaryWindow.Seconds())})
}

// hostOf strips the port from "10.92.0.5:8080".
func hostOf(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

// requestStatus validates ?status= for request lines: 2, 3, 4 or 5 (or 5xx).
func requestStatus(v string) (string, bool) {
	v = strings.TrimSuffix(strings.ToLower(v), "xx")
	switch v {
	case "", "2", "3", "4", "5":
		return v, true
	}
	return "", false
}
