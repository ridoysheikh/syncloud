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
	TLS  *TLSConfig `json:"tls,omitempty"`
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
	Servers        []Server `json:"servers"`
	PassHostHeader bool     `json:"passHostHeader"`
}

type Server struct {
	URL string `json:"url"`
}

type Middleware struct {
	RedirectScheme *RedirectScheme `json:"redirectScheme,omitempty"`
	RedirectRegex  *RedirectRegex  `json:"redirectRegex,omitempty"`
	Retry          *Retry          `json:"retry,omitempty"`
	RateLimit      *RateLimit      `json:"rateLimit,omitempty"`
	BasicAuth      *BasicAuth      `json:"basicAuth,omitempty"`
	IPAllowList    *IPAllowList    `json:"ipAllowList,omitempty"`
	Headers        *Headers        `json:"headers,omitempty"`
	Compress       *Compress       `json:"compress,omitempty"`
	CircuitBreaker *CircuitBreaker `json:"circuitBreaker,omitempty"`
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
	Certificates []Certificate `json:"certificates"`
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
	// ServiceRoutes returns the routes of user services.
	ServiceRoutes func() []ServiceRoute
	// Middlewares returns the preset middlewares routes refer to.
	Middlewares func() map[string]Middleware
	// Custom returns the validated raw configuration merged into the
	// generated one ("Advanced", §5.7).
	Custom func() map[string]any
}

const (
	svcController = "syncloud-controller"
	svcRegistry   = "syncloud-registry"
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
	if len(routes) > 0 {
		// Retry on another task when one fails mid-request (idempotent requests only, §5.7).
		d.HTTP.Middlewares["syncloud-retry"] = Middleware{Retry: &Retry{Attempts: 2, InitialInterval: "100ms"}}
	}
	if p.Middlewares != nil {
		for name, m := range p.Middlewares() {
			d.HTTP.Middlewares[name] = m
		}
	}
	for _, r := range routes {
		chain := append([]string{}, r.Middlewares...)
		if !r.OwnRetry {
			chain = append(chain, "syncloud-retry")
		}
		servers := make([]Server, 0, len(r.Servers))
		for _, u := range r.Servers {
			servers = append(servers, Server{URL: u})
		}
		d.HTTP.Services[r.Name] = Service{LoadBalancer: LoadBalancer{Servers: servers, PassHostHeader: true}}
		if base == "" {
			d.HTTP.Routers[r.Name] = Router{Rule: host(r.Host), EntryPoints: web, Middlewares: chain, Service: r.Name}
			continue
		}
		d.HTTP.Routers[r.Name] = Router{Rule: host(r.Host), EntryPoints: websecure, Middlewares: chain, Service: r.Name, TLS: &RouterTLS{}}
		d.HTTP.Routers[r.Name+"-http"] = Router{Rule: host(r.Host), EntryPoints: web, Middlewares: []string{"syncloud-https"}, Service: r.Name}
	}

	if base == "" {
		// No base domain yet: plain HTTP, the dashboard answers on any host.
		d.HTTP.Routers["syncloud-dashboard"] = Router{Rule: "PathPrefix(`/`)", Priority: 1, EntryPoints: web, Service: svcController}
		if p.RegistryURL != "" {
			d.HTTP.Routers["syncloud-registry"] = Router{Rule: host(DevRegistryHost), EntryPoints: web, Service: svcRegistry}
		}
		return d
	}

	registryHost := "registry." + base
	// ACME HTTP-01 challenges must stay on plain HTTP, ahead of the redirects.
	d.HTTP.Routers["syncloud-acme"] = Router{
		Rule: "PathPrefix(`/.well-known/acme-challenge/`)", Priority: 100000, EntryPoints: web, Service: svcController,
	}
	d.HTTP.Middlewares["syncloud-https"] = Middleware{RedirectScheme: &RedirectScheme{Scheme: "https", Port: p.HTTPSPort, Permanent: true}}
	d.HTTP.Routers["syncloud-https-redirect"] = Router{
		Rule: host(base) + " || " + host(registryHost), EntryPoints: web, Middlewares: []string{"syncloud-https"}, Service: svcController,
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
		if certs := p.Certificates(); len(certs) > 0 {
			d.TLS = &TLSConfig{Certificates: certs}
		}
	}
	return d
}

var hostSafe = regexp.MustCompile(`^[a-z0-9.-]+$`)

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
	_ = json.NewEncoder(w).Encode(p.Merged())
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
		if certs, ok := tls["certificates"].([]any); ok {
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
