package workload

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"syncloud/internal/store"
)

// Route is one public HTTP route to a service port (§5.7).
type Route struct {
	Name      string   // stable Traefik router/service name
	Host      string   // public hostname
	Servers   []string // http://<task-ip>:<port> of running tasks
	ServiceID string
}

// HostName is the default hostname of a service's HTTP port:
// <service>-<env>-<project> for the first one, <service>-<port>-<env>-<project>
// for the others, under base (or "localhost" before a base domain exists).
func HostName(sv store.Service, p Port, first bool, base string) string {
	label := sv.Name
	if !first {
		label += "-" + p.Name
	}
	label += "-" + sv.Environment + "-" + sv.Project
	if base == "" {
		base = "localhost"
	}
	return label + "." + base
}

type routeCache struct {
	mu     sync.Mutex
	dirty  bool
	base   string
	routes []Route
}

func (m *Manager) routesDirty() {
	m.routes.mu.Lock()
	m.routes.dirty = true
	m.routes.mu.Unlock()
}

// Routes returns every service route for base domain base. It is cheap to
// call often (Traefik polls every 2s): results are cached until tasks or
// services change.
func (m *Manager) Routes(ctx context.Context, base string) []Route {
	c := &m.routes
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty && c.base == base && c.routes != nil {
		return c.routes
	}
	svcs, err := m.st.ListServices(ctx)
	if err != nil {
		m.log.Error("routes: list services", "err", err)
		return c.routes
	}
	tasks, err := m.st.ActiveTasks(ctx)
	if err != nil {
		m.log.Error("routes: list tasks", "err", err)
		return c.routes
	}
	domains, err := m.st.ListDomains(ctx, "")
	if err != nil {
		m.log.Error("routes: list domains", "err", err)
		return c.routes
	}
	byService := map[string][]store.Domain{}
	for _, d := range domains {
		byService[d.ServiceID] = append(byService[d.ServiceID], d)
	}
	running := map[string][]store.Task{}
	for _, t := range tasks {
		if t.Desired == "running" && t.State == store.TaskRunning && t.IP != "" {
			running[t.ServiceID] = append(running[t.ServiceID], t)
		}
	}
	out := []Route{}
	for _, sv := range svcs {
		if sv.Deleting {
			continue
		}
		spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			continue
		}
		for i, p := range spec.HTTPPorts() {
			r := Route{Name: fmt.Sprintf("svc-%s-%s", sv.ID, p.Name), Host: HostName(sv, p, i == 0, base), ServiceID: sv.ID}
			for _, t := range running[sv.ID] {
				// Tasks of older revisions may expose the port differently.
				port := p.Container
				if t.Revision != sv.Revision {
					if old, err := m.SpecFor(ctx, sv.ID, t.Revision); err == nil {
						port = portByName(old, p.Name, port)
					}
				}
				r.Servers = append(r.Servers, "http://"+net.JoinHostPort(t.IP, fmt.Sprint(port)))
			}
			sort.Strings(r.Servers)
			out = append(out, r)
			for _, d := range byService[sv.ID] {
				if d.PortName == p.Name {
					out = append(out, Route{Name: "dom-" + strings.TrimPrefix(d.ID, "dom_"), Host: d.Host, Servers: r.Servers, ServiceID: sv.ID})
				}
			}
		}
	}
	c.routes, c.base, c.dirty = out, base, false
	return out
}

func portByName(s Spec, name string, def int) int {
	for _, p := range s.Ports {
		if p.Name == name {
			return p.Container
		}
	}
	return def
}

// ServiceEndpoints returns a service's public URLs.
func ServiceEndpoints(sv store.Service, spec Spec, base, scheme, port string) []string {
	var out []string
	for i, p := range spec.HTTPPorts() {
		u := scheme + "://" + HostName(sv, p, i == 0, base)
		if port != "" {
			u += ":" + port
		}
		out = append(out, u)
	}
	return out
}
