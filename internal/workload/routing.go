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

	"github.com/ridoysheikh/syncloud/internal/ports"
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
	// TCP and UDP ports: a public port on the project's load balancer.
	Public     bool     `json:"public"`
	PublicPort int      `json:"publicPort,omitempty"`
	Address    string   `json:"address,omitempty"` // what clients connect to
	Allow      []string `json:"allow"`
	// PortMode is how the public port was picked: auto (from the range) or
	// chosen (Phase 26).
	PortMode string `json:"portMode,omitempty"`
	// TCP only: PROXY protocol to the app, TLS at the load balancer.
	ProxyProtocol bool   `json:"proxyProtocol,omitempty"`
	TLS           string `json:"tls,omitempty"` // "" (raw) | terminate
}

// RoutingInput changes one port's routing; nil fields keep their value.
type RoutingInput struct {
	Generated *bool    `json:"generated,omitempty"`
	Label     *string  `json:"label,omitempty"`
	Public    *bool    `json:"public,omitempty"`
	Allow     []string `json:"allow,omitempty"`
	// PublicPort chooses the public port (tcp/udp; it makes the port
	// public); 0 goes back to an automatic one.
	PublicPort *int `json:"publicPort,omitempty"`
	// ProxyProtocol (tcp) sends the client's address to the app first.
	ProxyProtocol *bool `json:"proxyProtocol,omitempty"`
	// TLS (tcp): "none" or "" for raw bytes, "terminate" for TLS at the
	// load balancer.
	TLS *string `json:"tls,omitempty"`
}

// TLS modes of a public TCP port.
const TLSTerminate = "terminate"

// DefaultPublicPorts is the range public TCP/UDP ports are assigned from.
var DefaultPublicPorts = [2]int{20000, 20999}

// reservedLabels name platform hosts under the base domain.
var reservedLabels = map[string]bool{"registry": true, "git": true, "db": true, "www": true, "agent": true}

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

// ProjectBase returns the base domain of a project's generated addresses
// and public ports (nil: the cluster's). A project with its own ingress
// nodes gets the first one's sslip.io name (Phase 23).
var ProjectBase func(project, base string) string

func projectBase(project, base string) string {
	if ProjectBase == nil {
		return base
	}
	return ProjectBase(project, base)
}

// generatedHost is an HTTP port's generated address ("" when it is off).
func generatedHost(sv store.Service, p Port, first bool, base string, r store.PortRouting) string {
	base = projectBase(sv.Project, base)
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
			pr.Public, pr.PublicPort, pr.Address = true, r.PublicPort, PublicAddress(projectBase(sv.Project, base), r.PublicPort)
			pr.PortMode, pr.ProxyProtocol, pr.TLS = r.PortMode, r.ProxyProtocol, r.TLS
			if pr.PortMode == "" {
				pr.PortMode = store.PortAuto
			}
		}
		out = append(out, pr)
	}
	return out, nil
}

// SetRouting changes how a service's ports are reached. Making a TCP or UDP
// port public assigns it a free port from PublicPorts, or the one asked for;
// the ports are claimed in the port table on every node of the project's
// load balancer, in the same transaction. OnPublicPorts runs when the set
// of public ports changed (Traefik needs new entrypoints).
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
	byName := map[string]Port{}
	for _, p := range spec.Ports {
		byName[p.Name] = p
	}
	for name := range in {
		if _, ok := byName[name]; !ok {
			return nil, ErrInvalid{fmt.Errorf("the service has no port %q", name)}
		}
	}
	led := m.ledger()
	led.Lock()
	defer led.Unlock()
	owner := m.portOwner(sv)
	lo, hi := m.publicPorts()
	all, err := m.st.ListRouting(ctx, "")
	if err != nil {
		return nil, err
	}
	clusterPorts := map[int]bool{} // public numbers of the other services
	for _, r := range all {
		if r.PublicPort > 0 && r.ServiceID != sv.ID {
			clusterPorts[r.PublicPort] = true
		}
	}
	changedPublic := false
	var next []store.PortRouting
	// Public numbers of this service by protocol, so two of its ports never
	// share one.
	mine := map[string]map[int]string{"tcp": {}, "udp": {}}
	for _, p := range spec.Ports {
		if r := portRouting(rs, p.Name); p.Protocol != "http" && r.PublicPort > 0 {
			if _, asked := in[p.Name]; !asked {
				mine[p.Protocol][r.PublicPort] = p.Name
			}
		}
	}
	for _, p := range spec.Ports {
		r := portRouting(rs, p.Name)
		r.ServiceID = sv.ID
		x, ok := in[p.Name]
		if ok {
			if p.Protocol == "http" {
				if (x.Public != nil && *x.Public) || x.PublicPort != nil {
					return nil, ErrInvalid{fmt.Errorf("port %s is HTTP: it is reached through its addresses and domains; only tcp and udp ports get a public port", p.Name)}
				}
				if (x.ProxyProtocol != nil && *x.ProxyProtocol) || (x.TLS != nil && *x.TLS != "" && *x.TLS != "none") {
					return nil, ErrInvalid{fmt.Errorf("port %s is HTTP: PROXY protocol and TLS are for tcp ports", p.Name)}
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
				oldPort := r.PublicPort
				public := r.PublicPort > 0
				if x.Public != nil {
					public = *x.Public
				}
				if x.PublicPort != nil {
					if x.Public != nil && !*x.Public && *x.PublicPort > 0 {
						return nil, ErrInvalid{fmt.Errorf("port %s: a public port was given but public is false", p.Name)}
					}
					if *x.PublicPort > 0 || x.Public == nil {
						public = true
					}
				}
				switch {
				case !public:
					r.PublicPort, r.PortMode, r.Allow, r.ProxyProtocol, r.TLS = 0, "", nil, false, ""
				case x.PublicPort != nil && *x.PublicPort > 0 && *x.PublicPort != r.PublicPort:
					want := *x.PublicPort
					if want > 65535 {
						return nil, ErrInvalid{fmt.Errorf("port %s: %d is not a port (1–65535)", p.Name, want)}
					}
					if other, ok := mine[p.Protocol][want]; ok {
						return nil, ErrInvalid{fmt.Errorf("%s %d is already this service's port %s", p.Protocol, want, other)}
					}
					states, err := led.Check(ctx, owner, p.Protocol, want, false)
					if err != nil {
						return nil, err
					}
					if err := ports.Free(states, p.Protocol, want, m.describeOwner(ctx, sv)); err != nil {
						return nil, ErrInvalid{err}
					}
					r.PublicPort, r.PortMode = want, store.PortChosen
				case x.PublicPort != nil && *x.PublicPort > 0:
					// The number it already has: nothing to claim.
				case r.PublicPort == 0 || (x.PublicPort != nil && *x.PublicPort == 0 && r.PortMode == store.PortChosen):
					// Automatic ports stay unique across the cluster, as they
					// always were: only a chosen number may repeat on another
					// load balancer.
					skip := map[int]bool{}
					for _, set := range mine {
						for n := range set {
							skip[n] = true
						}
					}
					for n := range clusterPorts {
						skip[n] = true
					}
					port, err := led.Pick(ctx, owner, p.Protocol, lo, hi, skip)
					if err != nil {
						return nil, err
					}
					if port == 0 {
						return nil, ErrInvalid{fmt.Errorf("no free public port in %d–%d", lo, hi)}
					}
					r.PublicPort, r.PortMode = port, store.PortAuto
				}
				if r.PublicPort > 0 && r.PortMode == "" {
					r.PortMode = store.PortAuto
				}
				if r.PublicPort != oldPort {
					changedPublic = true
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
				if x.ProxyProtocol != nil {
					if *x.ProxyProtocol && p.Protocol != "tcp" {
						return nil, ErrInvalid{fmt.Errorf("port %s is %s: PROXY protocol is for tcp ports", p.Name, p.Protocol)}
					}
					if *x.ProxyProtocol && r.PublicPort == 0 {
						return nil, ErrInvalid{fmt.Errorf("port %s: PROXY protocol needs a public port", p.Name)}
					}
					r.ProxyProtocol = *x.ProxyProtocol
				}
				if x.TLS != nil {
					mode := strings.TrimSpace(*x.TLS)
					switch mode {
					case "", "none":
						mode = ""
					case TLSTerminate:
						if p.Protocol != "tcp" {
							return nil, ErrInvalid{fmt.Errorf("port %s is %s: TLS at the load balancer is for tcp ports", p.Name, p.Protocol)}
						}
						if base == "" && r.TLS != TLSTerminate {
							// Without one there is no certificate, and Traefik
							// would leave the port out.
							return nil, ErrInvalid{fmt.Errorf("port %s: TLS at the load balancer needs the platform's base domain (Settings → Domain) for its certificate", p.Name)}
						}
					default:
						return nil, ErrInvalid{fmt.Errorf("tls %q: want none or terminate", mode)}
					}
					if mode != "" && r.PublicPort == 0 {
						return nil, ErrInvalid{fmt.Errorf("port %s: TLS at the load balancer needs a public port", p.Name)}
					}
					if mode != r.TLS {
						changedPublic = true // certificates for the address
					}
					r.TLS = mode
				}
				if r.PublicPort > 0 {
					mine[p.Protocol][r.PublicPort] = p.Name
				}
			}
		}
		if r.Generated && r.Label == "" && r.PublicPort == 0 && len(r.Allow) == 0 {
			continue // the defaults: no row
		}
		next = append(next, r)
	}
	var wants []ports.Want
	for _, r := range next {
		if p, ok := byName[r.PortName]; ok && r.PublicPort > 0 && p.Protocol != "http" {
			wants = append(wants, ports.Want{PortName: r.PortName, Protocol: p.Protocol, Port: r.PublicPort, Mode: r.PortMode})
		}
	}
	switch err := m.st.SetRouting(ctx, sv.ID, next, led.Claims(owner, wants)); {
	case errors.Is(err, store.ErrNameTaken):
		return nil, ErrInvalid{errors.New("that label belongs to another service")}
	case errors.Is(err, store.ErrPortTaken):
		return nil, ErrInvalid{errors.New("that public port was just taken on one of the project's load balancers; choose another")}
	case err != nil:
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

// ledger is the port table (one of its own when none was set).
func (m *Manager) ledger() *ports.Ledger {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Ports == nil {
		m.Ports = ports.New(m.st, m.log)
	}
	return m.Ports
}

func (m *Manager) portOwner(sv store.Service) ports.Owner {
	return ports.Owner{Kind: store.PortOwnerService, ID: sv.ID, Project: sv.Project}
}

// describeOwner names the holder of a taken port for a service of sv's
// project: services of the same project by name, anything else vaguely.
func (m *Manager) describeOwner(ctx context.Context, sv store.Service) func(kind, id string) string {
	return func(kind, id string) string {
		if kind == store.PortOwnerDatabase {
			return "a database"
		}
		o, err := m.st.ServiceByID(ctx, id)
		if err != nil {
			return "another service"
		}
		if o.Project == sv.Project {
			return "service " + o.Environment + "/" + o.Name
		}
		return "a service of another project"
	}
}

// PortWants are every service's public ports, for the port table's resync.
func (m *Manager) PortWants(ctx context.Context) map[ports.Owner][]ports.Want {
	out := map[ports.Owner][]ports.Want{}
	rows, err := m.st.ListRouting(ctx, "")
	if err != nil {
		m.log.Error("public ports", "err", err)
		return out
	}
	for _, r := range rows {
		if r.PublicPort == 0 {
			continue
		}
		sv, err := m.st.ServiceByID(ctx, r.ServiceID)
		if err != nil {
			continue
		}
		spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			continue
		}
		for _, p := range spec.Ports {
			if p.Name == r.PortName && p.Protocol != "http" {
				o := m.portOwner(sv)
				mode := r.PortMode
				if mode == "" {
					mode = store.PortAuto
				}
				out[o] = append(out[o], ports.Want{PortName: p.Name, Protocol: p.Protocol, Port: r.PublicPort, Mode: mode})
			}
		}
	}
	return out
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
	Project   string
	// TCP only (Phase 26): PROXY protocol to the app, TLS at the load
	// balancer.
	ProxyProtocol bool
	TLS           string
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
	first := m.firstHolders(ctx)
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
			Allow: r.Allow, ServiceID: sv.ID, Project: sv.Project}
		if port.Protocol == "tcp" {
			pr.ProxyProtocol, pr.TLS = r.ProxyProtocol, r.TLS
		}
		// The same number on another load balancer: the first holder keeps
		// the plain name, so existing routers never change.
		if h, ok := first[fmt.Sprintf("%s/%d", port.Protocol, r.PublicPort)]; ok && h != sv.ID {
			pr.Name += "-" + strings.TrimPrefix(sv.ID, "svc_")
		}
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// firstHolders maps "protocol/port" to the service that claimed it first
// (by the port table), for stable Traefik names.
func (m *Manager) firstHolders(ctx context.Context) map[string]string {
	claims, err := m.st.ListPortClaims(ctx)
	if err != nil {
		return nil
	}
	type holder struct {
		id string
		at int64
	}
	best := map[string]holder{}
	for _, c := range claims {
		if c.OwnerKind != store.PortOwnerService {
			continue
		}
		k := fmt.Sprintf("%s/%d", c.Protocol, c.Port)
		h := holder{c.OwnerID, c.CreatedAt.UnixNano()}
		if b, ok := best[k]; !ok || h.at < b.at || (h.at == b.at && h.id < b.id) {
			best[k] = h
		}
	}
	out := map[string]string{}
	for k, h := range best {
		out[k] = h.id
	}
	return out
}
