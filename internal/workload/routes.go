package workload

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// Route is one public HTTP route to a service port (§5.7).
type Route struct {
	Name      string   // stable Traefik router/service name
	Host      string   // public hostname
	Servers   []string // http://<task-ip>:<port> of running tasks
	ServiceID string
	// HealthPath is the HTTP health check of this port ("" = none): Traefik
	// probes it too, so a task on a crashed node leaves the route within
	// seconds (§5.6).
	HealthPath string
	// Path limits a custom domain's route to a prefix; StripPrefix removes
	// it before forwarding; RedirectTo answers with a 301 to that host.
	Path        string
	StripPrefix bool
	RedirectTo  string
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

// InvalidateRoutes rebuilds routes on the next read (e.g. a task became
// unreachable from the controller).
func (m *Manager) InvalidateRoutes() {
	m.routesDirty()
	if m.OnTaskChange != nil {
		m.OnTaskChange()
	}
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
	routing := map[string]map[string]store.PortRouting{}
	if rows, err := m.st.ListRouting(ctx, ""); err == nil {
		for _, r := range rows {
			if routing[r.ServiceID] == nil {
				routing[r.ServiceID] = map[string]store.PortRouting{}
			}
			routing[r.ServiceID][r.PortName] = r
		}
	}
	running := map[string][]store.Task{}
	for _, t := range tasks {
		if t.IP != "" && m.serving(ctx, t) && (m.Reachable == nil || m.Reachable(t.ID)) {
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
			r := Route{Name: fmt.Sprintf("svc-%s-%s", sv.ID, p.Name), Host: generatedHost(sv, p, i == 0, base, portRouting(routing[sv.ID], p.Name)), ServiceID: sv.ID}
			if h := spec.Health; h != nil && h.Type == "http" && (h.Port == "" && i == 0 || h.Port == p.Name) {
				r.HealthPath = h.Path
				if r.HealthPath == "" {
					r.HealthPath = "/"
				}
			}
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
			if r.Host != "" { // the generated address is on
				out = append(out, r)
			}
			for _, d := range byService[sv.ID] {
				if d.PortName == p.Name {
					out = append(out, Route{Name: "dom-" + strings.TrimPrefix(d.ID, "dom_"), Host: d.Host, Servers: r.Servers, ServiceID: sv.ID,
						HealthPath: r.HealthPath, Path: d.Path, StripPrefix: d.StripPrefix, RedirectTo: d.RedirectTo})
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

// ServiceEndpoints returns a service's generated public URLs.
func ServiceEndpoints(sv store.Service, spec Spec, base, scheme, port string, routing []store.PortRouting) []string {
	var out []string
	rs := routingByPort(routing)
	for i, p := range spec.HTTPPorts() {
		h := generatedHost(sv, p, i == 0, base, portRouting(rs, p.Name))
		if h == "" {
			continue
		}
		u := scheme + "://" + h
		if port != "" {
			u += ":" + port
		}
		out = append(out, u)
	}
	return out
}
