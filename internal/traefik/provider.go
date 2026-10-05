// Package traefik generates Traefik's dynamic configuration (§5.7). Traefik
// polls it through its HTTP provider; users never write Traefik config.
package traefik

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// Dynamic is the subset of Traefik's dynamic configuration we generate.
type Dynamic struct {
	HTTP HTTPConfig `json:"http"`
}

type HTTPConfig struct {
	Routers  map[string]Router  `json:"routers"`
	Services map[string]Service `json:"services"`
}

type Router struct {
	Rule        string     `json:"rule"`
	Priority    int        `json:"priority,omitempty"`
	EntryPoints []string   `json:"entryPoints"`
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

// Provider serves the dynamic config to Traefik.
type Provider struct {
	// Token must be presented in TokenHeader (the config will carry TLS keys).
	Token       string
	TokenHeader string
	// ControllerURL is where Traefik reaches the controller.
	ControllerURL string
	// BaseDomain returns the current base domain ("" until one is set, §5.0.2).
	BaseDomain func() string
}

// Config builds the current dynamic configuration.
func (p *Provider) Config() Dynamic {
	d := Dynamic{HTTP: HTTPConfig{Routers: map[string]Router{}, Services: map[string]Service{}}}
	d.HTTP.Services["syncloud-controller"] = Service{LoadBalancer: LoadBalancer{
		Servers: []Server{{URL: p.ControllerURL}}, PassHostHeader: true,
	}}
	rule := "PathPrefix(`/`)" // no base domain yet: the dashboard answers on any host
	if domain := p.BaseDomain(); domain != "" {
		rule = "Host(`" + domain + "`)"
	}
	d.HTTP.Routers["syncloud-dashboard"] = Router{
		Rule: rule, Priority: 1, EntryPoints: []string{"web"}, Service: "syncloud-controller",
	}
	return d
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
