package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// Ports and domains (Phase 15c).

func (s *Server) base() string {
	if s.domains == nil {
		return ""
	}
	return s.domains.Base()
}

func (s *Server) handleGetRouting(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	out, err := s.workloads.Routing(r.Context(), sv.ID, s.base())
	if err != nil {
		s.workloadError(w, "get routing", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleSetRouting(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Ports map[string]workload.RoutingInput `json:"ports"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	out, err := s.workloads.SetRouting(r.Context(), sv.ID, req.Ports, s.base())
	if err != nil {
		s.workloadError(w, "set routing", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "service:SetRouting", srnOf(sv), map[string]any{"ports": req.Ports})
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// projectAddress is one way into a project's service: a generated address,
// a custom domain or a public TCP/UDP port.
type projectAddress struct {
	Kind        string       `json:"kind"` // generated | domain | public
	Service     string       `json:"service"`
	Environment string       `json:"environment"`
	Port        string       `json:"port"`
	Protocol    string       `json:"protocol"`
	Address     string       `json:"address"` // host, host/path, or host:port
	DomainID    string       `json:"domainId,omitempty"`
	RedirectTo  string       `json:"redirectTo,omitempty"`
	StripPrefix bool         `json:"stripPrefix,omitempty"`
	Label       bool         `json:"label,omitempty"` // a generated address renamed by a label
	Allow       []string     `json:"allow,omitempty"`
	DNS         *domainCheck `json:"dns,omitempty"`
	Certificate *certState   `json:"certificate,omitempty"`
}

type certState struct {
	Issuer   string    `json:"issuer"`
	Status   string    `json:"status"`
	NotAfter time.Time `json:"notAfter"`
	Error    string    `json:"error,omitempty"`
}

// handleProjectAddresses lists every address of a project's services in
// one environment (Project › Domains).
func (s *Server) handleProjectAddresses(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	envName := r.URL.Query().Get("environment")
	e, err := s.store.EnvironmentByName(r.Context(), p.ID, envName)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no environment "+envName+" in project "+p.Name)
		return
	} else if err != nil {
		s.internalError(w, "get environment", err)
		return
	}
	svcs, err := s.store.ListServicesIn(r.Context(), e.ID)
	if err != nil {
		s.internalError(w, "list services", err)
		return
	}
	certs := map[string]store.Certificate{}
	if cs, err := s.store.ListCertificates(r.Context()); err == nil {
		for _, c := range cs {
			certs[c.Host] = c
		}
	}
	cert := func(host string) *certState {
		if c, ok := certs[host]; ok {
			return &certState{Issuer: c.Issuer, Status: c.Status, NotAfter: c.NotAfter, Error: c.LastError}
		}
		return nil
	}
	base := s.base()
	out := []projectAddress{}
	var checks []*projectAddress
	for _, sv := range svcs {
		if sv.Deleting {
			continue
		}
		routes, err := s.workloads.Routing(r.Context(), sv.ID, base)
		if err != nil {
			continue
		}
		for _, pr := range routes {
			switch {
			case pr.Protocol == "http" && pr.Host != "":
				out = append(out, projectAddress{Kind: "generated", Service: sv.Name, Environment: e.Name, Port: pr.Port, Protocol: "http",
					Address: pr.Host, Label: pr.Label != "", Certificate: cert(pr.Host)})
			case pr.Public:
				out = append(out, projectAddress{Kind: "public", Service: sv.Name, Environment: e.Name, Port: pr.Port, Protocol: pr.Protocol,
					Address: pr.Address, Allow: pr.Allow})
			}
		}
		ds, err := s.store.ListDomains(r.Context(), sv.ID)
		if err != nil {
			continue
		}
		for _, d := range ds {
			out = append(out, projectAddress{Kind: "domain", Service: sv.Name, Environment: e.Name, Port: d.PortName, Protocol: "http",
				Address: d.Host + d.Path, DomainID: d.ID, RedirectTo: d.RedirectTo, StripPrefix: d.StripPrefix, Certificate: cert(d.Host)})
		}
	}
	for i := range out {
		if out[i].Kind == "domain" {
			checks = append(checks, &out[i])
		}
	}
	// DNS checks of custom domains, a few at a time.
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	hosts := map[string]*domainCheck{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, a := range checks {
		host := a.Address
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		mu.Lock()
		_, seen := hosts[host]
		hosts[host] = nil
		mu.Unlock()
		if seen {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			c := s.checkDomain(ctx, host)
			<-sem
			mu.Lock()
			hosts[host] = &c
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, a := range checks {
		host := a.Address
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		a.DNS = hosts[host]
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Address < out[j].Address
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
