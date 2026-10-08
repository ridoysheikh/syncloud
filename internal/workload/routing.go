package workload

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// Ports and domains (Phase 15c): how each port of a service is reached.
// Routing lives outside the revisions, so changing it does not redeploy.

// PortRoute is one port of a service with how it is reached.
type PortRoute struct {
	Port      string `json:"port"`
	Container int    `json:"container"`
	Protocol  string `json:"protocol"`
	// HTTP ports: the generated address is served unless Generated is off;
	// Label replaces its name (<label>.<base domain>).
	Generated bool   `json:"generated"`
	Label     string `json:"label,omitempty"`
	Host      string `json:"host,omitempty"` // the generated address, when served
	// TCP and UDP ports: a public port on the controller and edge nodes.
	Public     bool     `json:"public"`
	PublicPort int      `json:"publicPort,omitempty"`
	Address    string   `json:"address,omitempty"` // what clients connect to
	Allow      []string `json:"allow"`
}

// RoutingInput changes one port's routing; nil fields keep their value.
type RoutingInput struct {
	Generated *bool    `json:"generated,omitempty"`
	Label     *string  `json:"label,omitempty"`
	Public    *bool    `json:"public,omitempty"`
	Allow     []string `json:"allow,omitempty"`
}

// DefaultPublicPorts is the range public TCP/UDP ports are assigned from.
var DefaultPublicPorts = [2]int{20000, 20999}

// reservedLabels name platform hosts under the base domain.
var reservedLabels = map[string]bool{"registry": true, "git": true, "db": true, "www": true}

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func routingByPort(rs []store.PortRouting) map[string]store.PortRouting {
	out := map[string]store.PortRouting{}
	for _, r := range rs {
		out[r.PortName] = r
	}
	return out
}

// portRouting is a port's routing row, or the defaults.
func portRouting(rs map[string]store.PortRouting, port string) store.PortRouting {
	if r, ok := rs[port]; ok {
		return r
	}
	return store.PortRouting{PortName: port, Generated: true}
}

// generatedHost is an HTTP port's generated address ("" when it is off).
func generatedHost(sv store.Service, p Port, first bool, base string, r store.PortRouting) string {
	switch {
	case !r.Generated:
		return ""
	case r.Label != "":
		if base == "" {
			base = "localhost"
		}
		return r.Label + "." + base
	}
	return HostName(sv, p, first, base)
}

// PublicAddress is where clients reach a public TCP/UDP port.
func PublicAddress(base string, port int) string {
	if base == "" {
		base = "localhost"
	}
	return net.JoinHostPort(base, fmt.Sprint(port))
}

// Routing returns how each port of a service is reached.
func (m *Manager) Routing(ctx context.Context, serviceID, base string) ([]PortRoute, error) {
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		return nil, err
	}
	rows, err := m.st.ListRouting(ctx, sv.ID)
	if err != nil {
		return nil, err
	}
	rs := routingByPort(rows)
	out := []PortRoute{}
	first := true
	for _, p := range spec.Ports {
		r := portRouting(rs, p.Name)
		pr := PortRoute{Port: p.Name, Container: p.Container, Protocol: p.Protocol, Allow: r.Allow}
		if pr.Allow == nil {
			pr.Allow = []string{}
		}
		if p.Protocol == "http" {
			pr.Generated, pr.Label = r.Generated, r.Label
			pr.Host = generatedHost(sv, p, first, base, r)
			first = false
		} else if r.PublicPort > 0 {
			pr.Public, pr.PublicPort, pr.Address = true, r.PublicPort, PublicAddress(base, r.PublicPort)
		}
		out = append(out, pr)
	}
	return out, nil
}

// SetRouting changes how a service's ports are reached. Making a TCP or UDP
// port public assigns it a free port from PublicPorts; OnPublicPorts runs
// when the set of public ports changed (Traefik needs new entrypoints).
func (m *Manager) SetRouting(ctx context.Context, serviceID string, in map[string]RoutingInput, base string) ([]PortRoute, error) {
	sv, err := m.st.ServiceByID(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
	if err != nil {
		return nil, err
	}
	rows, err := m.st.ListRouting(ctx, sv.ID)
	if err != nil {
		return nil, err
	}
	rs := routingByPort(rows)
	ports := map[string]Port{}
	for _, p := range spec.Ports {
		ports[p.Name] = p
	}
	for name := range in {
		if _, ok := ports[name]; !ok {
			return nil, ErrInvalid{fmt.Errorf("the service has no port %q", name)}
		}
	}
	all, err := m.st.ListRouting(ctx, "")
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, r := range all {
		if r.PublicPort > 0 && r.ServiceID != sv.ID {
			used[r.PublicPort] = true
		}
	}
	lo, hi := m.publicPorts()
	changedPublic := false
	var next []store.PortRouting
	for _, p := range spec.Ports {
		r := portRouting(rs, p.Name)
		r.ServiceID = sv.ID
		x, ok := in[p.Name]
		if ok {
			if p.Protocol == "http" {
				if x.Public != nil && *x.Public {
					return nil, ErrInvalid{fmt.Errorf("port %s is HTTP: it is reached through its addresses and domains; only tcp and udp ports get a public port", p.Name)}
				}
				if x.Generated != nil {
					r.Generated = *x.Generated
				}
				if x.Label != nil {
					label := strings.ToLower(strings.TrimSpace(*x.Label))
					if label != "" {
						if err := m.checkLabel(ctx, sv, label, base); err != nil {
							return nil, err
						}
					}
					r.Label = label
				}
			} else {
				if x.Generated != nil || (x.Label != nil && *x.Label != "") {
					return nil, ErrInvalid{fmt.Errorf("port %s is %s: addresses and labels are for http ports", p.Name, p.Protocol)}
				}
				if x.Public != nil {
					switch {
					case *x.Public && r.PublicPort == 0:
						port := 0
						for c := lo; c <= hi; c++ {
							if !used[c] {
								port = c
								break
							}
						}
						if port == 0 {
							return nil, ErrInvalid{fmt.Errorf("no free public port in %d–%d", lo, hi)}
						}
						r.PublicPort, changedPublic = port, true
						used[port] = true
					case !*x.Public && r.PublicPort > 0:
						r.PublicPort, r.Allow, changedPublic = 0, nil, true
					}
				}
				if x.Allow != nil {
					allow, err := cidrs(x.Allow)
					if err != nil {
						return nil, err
					}
					if !slices.Equal(allow, r.Allow) && r.PublicPort > 0 {
						changedPublic = true // the firewall's sources change
					}
					r.Allow = allow
				}
			}
		}
		if r.Generated && r.Label == "" && r.PublicPort == 0 && len(r.Allow) == 0 {
			continue // the defaults: no row
		}
		next = append(next, r)
	}
	if err := m.st.SetRouting(ctx, sv.ID, next); errors.Is(err, store.ErrNameTaken) {
		return nil, ErrInvalid{errors.New("that label or public port belongs to another service")}
	} else if err != nil {
		return nil, err
	}
	m.routesDirty()
	if m.OnChange != nil {
		m.OnChange() // certificates for a new label
	}
	if changedPublic && m.OnPublicPorts != nil {
		m.OnPublicPorts()
	}
	return m.Routing(ctx, sv.ID, base)
}

func (m *Manager) publicPorts() (int, int) {
	if m.PublicPortRange[0] > 0 && m.PublicPortRange[1] >= m.PublicPortRange[0] {
		return m.PublicPortRange[0], m.PublicPortRange[1]
	}
	return DefaultPublicPorts[0], DefaultPublicPorts[1]
}

// checkLabel refuses labels that are invalid, reserved, or already an
// address of another service or a custom domain.
func (m *Manager) checkLabel(ctx context.Context, sv store.Service, label, base string) error {
	if !labelRE.MatchString(label) {
		return ErrInvalid{fmt.Errorf("label %q: lowercase letters, digits and dashes, at most 63", label)}
	}
	if reservedLabels[label] {
		return ErrInvalid{fmt.Errorf("%s is reserved for the platform", label)}
	}
	host := label + "." + base
	svcs, err := m.st.ListServices(ctx)
	if err != nil {
		return err
	}
	for _, o := range svcs {
		if o.ID == sv.ID {
			continue
		}
		if spec, err := m.SpecFor(ctx, o.ID, o.Revision); err == nil {
			for i, p := range spec.HTTPPorts() {
				if HostName(o, p, i == 0, base) == host {
					return ErrInvalid{fmt.Errorf("%s is the address of %s/%s/%s", host, o.Project, o.Environment, o.Name)}
				}
			}
		}
	}
	domains, err := m.st.ListDomains(ctx, "")
	if err != nil {
		return err
	}
	for _, d := range domains {
		if d.Host == host {
			return ErrInvalid{fmt.Errorf("%s is a custom domain already", host)}
		}
	}
	return nil
}

func cidrs(in []string) ([]string, error) {
	out := []string{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if ip := net.ParseIP(c); ip != nil {
				if ip.To4() != nil {
					c += "/32"
				} else {
					c += "/128"
				}
			}
		}
		if _, n, err := net.ParseCIDR(c); err != nil {
			return nil, ErrInvalid{fmt.Errorf("%q is not an address or CIDR", c)}
		} else {
			c = n.String()
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) > 50 {
		return nil, ErrInvalid{errors.New("at most 50 allowed sources")}
	}
	sort.Strings(out)
	return out, nil
}

// PublicRoute is a public TCP or UDP port of a service.
type PublicRoute struct {
	Name      string // stable Traefik name
	Protocol  string // tcp | udp
	Port      int    // the public port
	Servers   []string
	Allow     []string
	ServiceID string
}

// Entrypoint is the Traefik entrypoint of a public port.
func (r PublicRoute) Entrypoint() string { return fmt.Sprintf("%s-%d", r.Protocol, r.Port) }

// PublicRoutes returns every public TCP/UDP port with the running tasks
// behind it.
func (m *Manager) PublicRoutes(ctx context.Context) []PublicRoute {
	rows, err := m.st.ListRouting(ctx, "")
	if err != nil {
		m.log.Error("public routes", "err", err)
		return nil
	}
	tasks, _ := m.st.ActiveTasks(ctx)
	var out []PublicRoute
	for _, r := range rows {
		if r.PublicPort == 0 {
			continue
		}
		sv, err := m.st.ServiceByID(ctx, r.ServiceID)
		if err != nil || sv.Deleting {
			continue
		}
		spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			continue
		}
		var port *Port
		for i := range spec.Ports {
			if spec.Ports[i].Name == r.PortName {
				port = &spec.Ports[i]
			}
		}
		if port == nil || port.Protocol == "http" {
			continue // the port is gone or became http: nothing to serve
		}
		pr := PublicRoute{Name: fmt.Sprintf("pub-%s-%d", port.Protocol, r.PublicPort), Protocol: port.Protocol, Port: r.PublicPort,
			Allow: r.Allow, ServiceID: sv.ID}
		for _, t := range tasks {
			if t.ServiceID == sv.ID && t.IP != "" && m.serving(ctx, t) {
				c := port.Container
				if t.Revision != sv.Revision {
					if old, err := m.SpecFor(ctx, sv.ID, t.Revision); err == nil {
						c = portByName(old, port.Name, c)
					}
				}
				pr.Servers = append(pr.Servers, net.JoinHostPort(t.IP, fmt.Sprint(c)))
			}
		}
		sort.Strings(pr.Servers)
		out = append(out, pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}
