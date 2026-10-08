// Package traefik generates Traefik's dynamic configuration (§5.7). Traefik
// polls it through its HTTP provider; users never write Traefik config.
package traefik

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Dynamic is the subset of Traefik's dynamic configuration we generate.
type Dynamic struct {
	HTTP HTTPConfig `json:"http"`
	TCP  *TCPConfig `json:"tcp,omitempty"`
	UDP  *UDPConfig `json:"udp,omitempty"`
	TLS  *TLSConfig `json:"tls,omitempty"`
}

// UDPConfig carries public UDP ports of services (Phase 15c).
type UDPConfig struct {
	Routers  map[string]UDPRouter  `json:"routers"`
	Services map[string]UDPService `json:"services"`
}

type UDPRouter struct {
	EntryPoints []string `json:"entryPoints"`
	Service     string   `json:"service"`
}

type UDPService struct {
	LoadBalancer UDPLoadBalancer `json:"loadBalancer"`
}

type UDPLoadBalancer struct {
	Servers []TCPServer `json:"servers"` // {address}
}

// TCPConfig carries the public database endpoints (Phase 12e): TLS is
// terminated by Traefik and routed by SNI.
type TCPConfig struct {
	Routers     map[string]TCPRouter     `json:"routers"`
	Services    map[string]TCPService    `json:"services"`
	Middlewares map[string]TCPMiddleware `json:"middlewares,omitempty"`
}

type TCPRouter struct {
	Rule        string     `json:"rule"`
	EntryPoints []string   `json:"entryPoints"`
	Middlewares []string   `json:"middlewares,omitempty"`
	Service     string     `json:"service"`
	TLS         *RouterTLS `json:"tls,omitempty"`
}

type TCPService struct {
	LoadBalancer TCPLoadBalancer `json:"loadBalancer"`
}

type TCPLoadBalancer struct {
	Servers []TCPServer `json:"servers"`
}

type TCPServer struct {
	Address string `json:"address"`
}

type TCPMiddleware struct {
	IPAllowList *TCPIPAllowList `json:"ipAllowList,omitempty"`
}

type TCPIPAllowList struct {
	SourceRange []string `json:"sourceRange"`
}

// TCPRoute is one public TLS endpoint (a database's), or with Plain a
// public TCP port of a service passed through as is (Phase 15c).
type TCPRoute struct {
	// Host picks the route by SNI; empty takes any TLS connection on the
	// entrypoint (a dedicated port).
	Name, Entrypoint, Host string
	Servers                []string // host:port
	Allow                  []string // client CIDRs (empty: anyone)
	Plain                  bool     // no TLS: any plain connection on the entrypoint
}

// UDPRoute is a public UDP port of a service.
type UDPRoute struct {
	Name, Entrypoint string
	Servers          []string // host:port
}

type HTTPConfig struct {
	Routers     map[string]Router     `json:"routers"`
	Services    map[string]Service    `json:"services"`
	Middlewares map[string]Middleware `json:"middlewares,omitempty"`
}

type Router struct {
	Rule        string     `json:"rule"`
	Priority    int        `json:"priority,omitempty"`
	EntryPoints []string   `json:"entryPoints"`
	Middlewares []string   `json:"middlewares,omitempty"`
	Service     string     `json:"service"`
	TLS         *RouterTLS `json:"tls,omitempty"`
}

type RouterTLS struct{}

type Service struct {
	LoadBalancer LoadBalancer `json:"loadBalancer"`
}

type LoadBalancer struct {
	Servers        []Server     `json:"servers"`
	PassHostHeader bool         `json:"passHostHeader"`
	HealthCheck    *HealthCheck `json:"healthCheck,omitempty"`
}

// HealthCheck is Traefik's active check of each server.
type HealthCheck struct {
	Path     string `json:"path"`
	Interval string `json:"interval"`
	Timeout  string `json:"timeout"`
}

type Server struct {
	URL string `json:"url"`
}

type Middleware struct {
	StripPrefix    *StripPrefix    `json:"stripPrefix,omitempty"`
	RedirectScheme *RedirectScheme `json:"redirectScheme,omitempty"`
	RedirectRegex  *RedirectRegex  `json:"redirectRegex,omitempty"`
	Retry          *Retry          `json:"retry,omitempty"`
	RateLimit      *RateLimit      `json:"rateLimit,omitempty"`
	BasicAuth      *BasicAuth      `json:"basicAuth,omitempty"`
	IPAllowList    *IPAllowList    `json:"ipAllowList,omitempty"`
	Headers        *Headers        `json:"headers,omitempty"`
	Compress       *Compress       `json:"compress,omitempty"`
	CircuitBreaker *CircuitBreaker `json:"circuitBreaker,omitempty"`
	Buffering      *Buffering      `json:"buffering,omitempty"`
}

type StripPrefix struct {
	Prefixes []string `json:"prefixes"`
}

type Retry struct {
	Attempts        int    `json:"attempts"`
	InitialInterval string `json:"initialInterval,omitempty"`
}

// ServiceRoute is one public route to a service's tasks (§5.7).
type ServiceRoute struct {
	Name    string
	Host    string
	Servers []string
	// Middlewares are attached presets, in the order to apply them. With
	// OwnRetry set, one of them replaces the default retry.
	Middlewares []string
	OwnRetry    bool
	// HealthPath makes Traefik probe each task too ("" = no check).
	HealthPath string
	// Path limits the route to a prefix ("" = the whole host); StripPrefix
	// removes it before forwarding. RedirectTo answers with a permanent
	// redirect to that host instead of forwarding (Phase 15c).
	Path        string
	StripPrefix bool
	RedirectTo  string
}

type RedirectScheme struct {
	Scheme    string `json:"scheme"`
	Port      string `json:"port,omitempty"`
	Permanent bool   `json:"permanent"`
}

type RedirectRegex struct {
	Regex       string `json:"regex"`
	Replacement string `json:"replacement"`
	Permanent   bool   `json:"permanent"`
}

type TLSConfig struct {
	Certificates []Certificate         `json:"certificates,omitempty"`
	Options      map[string]TLSOptions `json:"options,omitempty"`
	Stores       map[string]TLSStore   `json:"stores,omitempty"`
}

// TLSStore sets the certificate for connections no other one matches,
// such as clients that send no SNI.
type TLSStore struct {
	DefaultCertificate *Certificate `json:"defaultCertificate,omitempty"`
}

// Certificate holds PEM content inline (Traefik accepts content or a path).
type Certificate struct {
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

// Provider serves the dynamic config to Traefik.
type Provider struct {
	// Token must be presented in TokenHeader (the config carries TLS keys).
	Token       string
	TokenHeader string
	// ControllerURL is where Traefik reaches the controller.
	ControllerURL string
	// RegistryURL is where Traefik reaches the registry ("" disables its route).
	RegistryURL string
	// BaseDomain returns the current base domain ("" until one is set, §5.0.2).
	BaseDomain func() string
	// HTTPSPort is the public HTTPS port when it is not 443 (development).
	HTTPSPort string
	// Certificates returns the TLS certificates to serve.
	Certificates func() []Certificate
	// DefaultCertificate is served when no other certificate matches the
	// SNI, or there is none (nil: Traefik's self-signed one).
	DefaultCertificate func() *Certificate
	// ServiceRoutes returns the routes of user services.
	ServiceRoutes func() []ServiceRoute
	// Middlewares returns the preset middlewares routes refer to.
	Middlewares func() map[string]Middleware
	// Custom returns the validated raw configuration merged into the
	// generated one ("Advanced", §5.7).
	Custom func() map[string]any
	// MeshControllerURL is the controller as edge nodes reach it over the
	// private network (§8.5).
	MeshControllerURL func() string
	// Settings returns the global settings (nil: the defaults).
	Settings func() Settings
	// TCPRoutes returns the public database endpoints (only served with a
	// base domain: they need certificates) and public TCP ports of services.
	TCPRoutes func() []TCPRoute
	// UDPRoutes returns public UDP ports of services.
	UDPRoutes func() []UDPRoute
	// GitHost returns the built-in Git server's hostname ("" when it is
	// off, §5.8); GitServerURL is where Traefik reaches it.
	GitHost      func() string
	GitServerURL string
}

const (
	svcController = "syncloud-controller"
	svcRegistry   = "syncloud-registry"
	svcGit        = "syncloud-git"
	// DevRegistryHost is the registry's hostname before a base domain exists.
	DevRegistryHost = "registry.localhost"
)

// Config builds the current dynamic configuration.
func (p *Provider) Config() Dynamic {
	d := Dynamic{HTTP: HTTPConfig{Routers: map[string]Router{}, Services: map[string]Service{}, Middlewares: map[string]Middleware{}}}
	d.HTTP.Services[svcController] = Service{LoadBalancer: LoadBalancer{Servers: []Server{{URL: p.ControllerURL}}, PassHostHeader: true}}
	if p.RegistryURL != "" {
		d.HTTP.Services[svcRegistry] = Service{LoadBalancer: LoadBalancer{Servers: []Server{{URL: p.RegistryURL}}, PassHostHeader: true}}
	}
	web, websecure := []string{"web"}, []string{"websecure"}

	base := p.BaseDomain()
	var routes []ServiceRoute
	if p.ServiceRoutes != nil {
		routes = p.ServiceRoutes()
	}
	set := DefaultSettings()
	if p.Settings != nil {
		set = p.Settings()
	}
	d.TLS = &TLSConfig{Options: set.tlsOptions()}
	var defaults []string
	retry := false
	if len(routes) > 0 {
		// Global defaults first; retry on another task when one fails
		// mid-request (idempotent requests only, §5.7) comes last.
		defaults, retry = set.defaults(&d)
	}
	if p.Middlewares != nil {
		for name, m := range p.Middlewares() {
			d.HTTP.Middlewares[name] = m
		}
	}
	for _, r := range routes {
		chain := append(append([]string{}, defaults...), r.Middlewares...)
		if retry && !r.OwnRetry {
			chain = append(chain, mwRetry)
		}
		rule := host(r.Host)
		if r.Path != "" {
			rule += " && " + pathPrefix(r.Path)
			if r.StripPrefix {
				mw := r.Name + "-strip"
				d.HTTP.Middlewares[mw] = Middleware{StripPrefix: &StripPrefix{Prefixes: []string{r.Path}}}
				chain = append([]string{mw}, chain...)
			}
		}
		if r.RedirectTo != "" {
			mw := r.Name + "-redirect"
			d.HTTP.Middlewares[mw] = Middleware{RedirectRegex: &RedirectRegex{
				Regex: `^https?://[^/]+(.*)`, Replacement: "https://" + hostOnly(r.RedirectTo) + "${1}", Permanent: true}}
			chain = []string{mw}
		}
		servers := make([]Server, 0, len(r.Servers))
		for _, u := range r.Servers {
			servers = append(servers, Server{URL: u})
		}
		lb := LoadBalancer{Servers: servers, PassHostHeader: true}
		if r.HealthPath != "" {
			// Probed by every Traefik replica: a task whose node crashed is
			// out of rotation within seconds, before the controller notices.
			lb.HealthCheck = &HealthCheck{Path: r.HealthPath, Interval: "2s", Timeout: "1s"}
		}
		d.HTTP.Services[r.Name] = Service{LoadBalancer: lb}
		if base == "" {
			d.HTTP.Routers[r.Name] = Router{Rule: rule, EntryPoints: web, Middlewares: chain, Service: r.Name}
			continue
		}
		d.HTTP.Routers[r.Name] = Router{Rule: rule, EntryPoints: websecure, Middlewares: chain, Service: r.Name, TLS: &RouterTLS{}}
		if set.RedirectHTTPS {
			d.HTTP.Routers[r.Name+"-http"] = Router{Rule: rule, EntryPoints: web, Middlewares: []string{"syncloud-https"}, Service: r.Name}
		} else {
			d.HTTP.Routers[r.Name+"-http"] = Router{Rule: rule, EntryPoints: web, Middlewares: chain, Service: r.Name}
		}
	}

	if base == "" {
		// No base domain yet: plain HTTP, the dashboard answers on any host.
		d.HTTP.Routers["syncloud-dashboard"] = Router{Rule: "PathPrefix(`/`)", Priority: 1, EntryPoints: web, Service: svcController}
		if p.RegistryURL != "" {
			d.HTTP.Routers["syncloud-registry"] = Router{Rule: host(DevRegistryHost), EntryPoints: web, Service: svcRegistry}
		}
		// Plain TCP and UDP ports need no certificates.
		if p.TCPRoutes != nil {
			var plain []TCPRoute
			for _, r := range p.TCPRoutes() {
				if r.Plain {
					plain = append(plain, r)
				}
			}
			d.TCP = tcpConfig(plain)
		}
		if p.UDPRoutes != nil {
			d.UDP = udpConfig(p.UDPRoutes())
		}
		return d
	}

	registryHost := "registry." + base
	redirect := host(base) + " || " + host(registryHost)
	if p.GitHost != nil {
		if gh := p.GitHost(); gh != "" {
			d.HTTP.Services[svcGit] = Service{LoadBalancer: LoadBalancer{Servers: []Server{{URL: p.GitServerURL}}, PassHostHeader: true}}
			d.HTTP.Routers[svcGit] = Router{Rule: host(gh), EntryPoints: websecure, Service: svcGit, TLS: &RouterTLS{}}
			redirect += " || " + host(gh)
		}
	}
	// ACME HTTP-01 challenges must stay on plain HTTP, ahead of the redirects.
	d.HTTP.Routers["syncloud-acme"] = Router{
		Rule: "PathPrefix(`/.well-known/acme-challenge/`)", Priority: 100000, EntryPoints: web, Service: svcController,
	}
	d.HTTP.Middlewares["syncloud-https"] = Middleware{RedirectScheme: &RedirectScheme{Scheme: "https", Port: p.HTTPSPort, Permanent: true}}
	d.HTTP.Routers["syncloud-https-redirect"] = Router{
		Rule: redirect, EntryPoints: web, Middlewares: []string{"syncloud-https"}, Service: svcController,
	}
	// Anything else on HTTP (the bare IP, unknown hosts) goes to the dashboard.
	target := "https://" + base
	if p.HTTPSPort != "" {
		target += ":" + p.HTTPSPort
	}
	d.HTTP.Middlewares["syncloud-to-dashboard"] = Middleware{RedirectRegex: &RedirectRegex{
		Regex: `^https?://[^/]+/(.*)`, Replacement: target + "/${1}", Permanent: false,
	}}
	d.HTTP.Routers["syncloud-catchall"] = Router{
		Rule: "PathPrefix(`/`)", Priority: 1, EntryPoints: web, Middlewares: []string{"syncloud-to-dashboard"}, Service: svcController,
	}
	d.HTTP.Routers["syncloud-dashboard"] = Router{Rule: host(base), EntryPoints: websecure, Service: svcController, TLS: &RouterTLS{}}
	if p.RegistryURL != "" {
		d.HTTP.Routers["syncloud-registry"] = Router{Rule: host(registryHost), EntryPoints: websecure, Service: svcRegistry, TLS: &RouterTLS{}}
	}
	if p.Certificates != nil {
		d.TLS.Certificates = p.Certificates()
	}
	if p.DefaultCertificate != nil {
		if c := p.DefaultCertificate(); c != nil {
			d.TLS.Stores = map[string]TLSStore{"default": {DefaultCertificate: c}}
		}
	}
	if p.TCPRoutes != nil {
		d.TCP = tcpConfig(p.TCPRoutes())
	}
	if p.UDPRoutes != nil {
		d.UDP = udpConfig(p.UDPRoutes())
	}
	return d
}

func udpConfig(routes []UDPRoute) *UDPConfig {
	c := &UDPConfig{Routers: map[string]UDPRouter{}, Services: map[string]UDPService{}}
	for _, r := range routes {
		if len(r.Servers) == 0 {
			continue
		}
		lb := UDPLoadBalancer{}
		for _, a := range r.Servers {
			lb.Servers = append(lb.Servers, TCPServer{Address: a})
		}
		c.Services[r.Name] = UDPService{LoadBalancer: lb}
		c.Routers[r.Name] = UDPRouter{EntryPoints: []string{r.Entrypoint}, Service: r.Name}
	}
	if len(c.Routers) == 0 {
		return nil
	}
	return c
}

func tcpConfig(routes []TCPRoute) *TCPConfig {
	if len(routes) == 0 {
		return nil
	}
	c := &TCPConfig{Routers: map[string]TCPRouter{}, Services: map[string]TCPService{}, Middlewares: map[string]TCPMiddleware{}}
	for _, r := range routes {
		if len(r.Servers) == 0 {
			continue // nothing to serve yet (no running primary)
		}
		lb := TCPLoadBalancer{}
		for _, a := range r.Servers {
			lb.Servers = append(lb.Servers, TCPServer{Address: a})
		}
		c.Services[r.Name] = TCPService{LoadBalancer: lb}
		rt := TCPRouter{Rule: hostSNI(r.Host), EntryPoints: []string{r.Entrypoint}, Service: r.Name, TLS: &RouterTLS{}}
		if r.Host == "" {
			rt.Rule = "HostSNI(`*`)" // TLS with any name, or none
		}
		if r.Plain {
			rt.Rule, rt.TLS = "HostSNI(`*`)", nil // raw TCP: no TLS, any client
		}
		if len(r.Allow) > 0 {
			mw := r.Name + "-allow"
			c.Middlewares[mw] = TCPMiddleware{IPAllowList: &TCPIPAllowList{SourceRange: r.Allow}}
			rt.Middlewares = []string{mw}
		}
		c.Routers[r.Name] = rt
	}
	if len(c.Routers) == 0 {
		return nil
	}
	return c
}

// hostSNI is host's TLS counterpart.
func hostSNI(h string) string {
	if !hostSafe.MatchString(h) {
		h = "invalid.invalid"
	}
	return "HostSNI(`" + h + "`)"
}

var hostSafe = regexp.MustCompile(`^[a-z0-9.-]+$`)

var pathSafe = regexp.MustCompile(`^(/[A-Za-z0-9._~!$&'()*+,;=:@%-]+)+$`)

// pathPrefix builds a PathPrefix matcher; paths are validated before they
// are stored.
func pathPrefix(p string) string {
	if !pathSafe.MatchString(p) || strings.Contains(p, "`") {
		p = "/invalid.invalid"
	}
	return "PathPrefix(`" + p + "`)"
}

// hostOnly guards a redirect target.
func hostOnly(h string) string {
	if !hostSafe.MatchString(h) {
		return "invalid.invalid"
	}
	return h
}

// host builds a Host matcher. Domains are validated before they are stored;
// this is a last guard against breaking out of the rule's backticks.
func host(h string) string {
	if !hostSafe.MatchString(h) {
		h = "invalid.invalid"
	}
	return "Host(`" + h + "`)"
}

func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(p.TokenHeader)), []byte(p.Token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("edge") != "" {
		_ = json.NewEncoder(w).Encode(p.EdgeConfig())
		return
	}
	_ = json.NewEncoder(w).Encode(p.Merged())
}

// EdgeConfig is the configuration for an edge node's Traefik (§8.5): the
// same routes and certificates, with the controller reached over the
// private network; the registry stays on the controller's own Traefik.
func (p *Provider) EdgeConfig() map[string]any {
	out := p.Merged()
	h, _ := out["http"].(map[string]any)
	if h == nil {
		return out
	}
	if svcs, ok := h["services"].(map[string]any); ok {
		delete(svcs, svcRegistry)
		delete(svcs, svcGit)
		if p.MeshControllerURL != nil {
			svcs[svcController] = map[string]any{"loadBalancer": map[string]any{"servers": []any{map[string]any{"url": p.MeshControllerURL()}}, "passHostHeader": true}}
		}
	}
	if routers, ok := h["routers"].(map[string]any); ok {
		delete(routers, "syncloud-registry")
		delete(routers, svcGit)
	}
	return out
}

// Generated is the configuration without the custom part, as a map.
func (p *Provider) Generated() map[string]any {
	var out map[string]any
	b, _ := json.Marshal(p.Config())
	_ = json.Unmarshal(b, &out)
	return out
}

// Merged is the generated configuration with the custom one added.
func (p *Provider) Merged() map[string]any {
	var out map[string]any
	b, _ := json.Marshal(p.Config())
	_ = json.Unmarshal(b, &out)
	if p.Custom != nil {
		MergeCustom(out, p.Custom())
	}
	return out
}

// MergeCustom adds custom routers, services, middlewares and transports
// (validated not to collide with generated names) to a configuration.
func MergeCustom(out, custom map[string]any) {
	for proto, sections := range custom {
		secs, ok := sections.(map[string]any)
		if !ok {
			continue
		}
		dst, _ := out[proto].(map[string]any)
		if dst == nil {
			dst = map[string]any{}
			out[proto] = dst
		}
		for sec, entries := range secs {
			ents, ok := entries.(map[string]any)
			if !ok {
				continue
			}
			d, _ := dst[sec].(map[string]any)
			if d == nil {
				d = map[string]any{}
				dst[sec] = d
			}
			for name, v := range ents {
				if _, taken := d[name]; !taken {
					d[name] = v
				}
			}
		}
	}
}

// Redacted is the served configuration for display: certificates and keys
// are replaced by their sizes.
func (p *Provider) Redacted() map[string]any {
	out := p.Merged()
	if tls, ok := out["tls"].(map[string]any); ok {
		certs, _ := tls["certificates"].([]any)
		if stores, ok := tls["stores"].(map[string]any); ok {
			for _, st := range stores {
				if m, ok := st.(map[string]any); ok && m["defaultCertificate"] != nil {
					certs = append(certs, m["defaultCertificate"])
				}
			}
		}
		for _, c := range certs {
			if m, ok := c.(map[string]any); ok {
				for _, k := range []string{"certFile", "keyFile"} {
					if s, ok := m[k].(string); ok {
						m[k] = fmt.Sprintf("(PEM, %d bytes, redacted)", len(s))
					}
				}
			}
		}
	}
	if http, ok := out["http"].(map[string]any); ok {
		if mws, ok := http["middlewares"].(map[string]any); ok {
			for _, mw := range mws {
				if m, ok := mw.(map[string]any); ok {
					if ba, ok := m["basicAuth"].(map[string]any); ok {
						if users, ok := ba["users"].([]any); ok {
							for i, u := range users {
								if s, ok := u.(string); ok {
									name, _, _ := strings.Cut(s, ":")
									users[i] = name + ":(hash redacted)"
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}
