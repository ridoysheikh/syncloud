// Package traefik generates Traefik's dynamic configuration (§5.7). Traefik
// polls it through its HTTP provider; users never write Traefik config.
package traefik

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"regexp"
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
	_ = json.NewEncoder(w).Encode(p.Config())
}
