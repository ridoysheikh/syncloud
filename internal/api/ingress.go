package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/ingress"
	"github.com/ridoysheikh/syncloud/internal/mesh"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// ingressNode is one of a project's load balancers, with its replica's health.
type ingressNode struct {
	Name    string `json:"name"`
	Address string `json:"address"` // public (point DNS here)
	State   string `json:"state"`   // healthy | unhealthy | starting | unknown
}

type ingressView struct {
	Project string        `json:"project"`
	Nodes   []ingressNode `json:"nodes"` // empty: the controller and edge pools
	// Targets are the addresses the project's custom domains must resolve
	// to; Base is the base domain of its generated addresses.
	Targets []string `json:"targets"`
	Base    string   `json:"base"`
	// Move is the change in progress: the previous ingress serves Pending
	// domains until their DNS moves.
	Move    *store.IngressMove `json:"move,omitempty"`
	Pending []string           `json:"pending"`
}

func (s *Server) ingressView(r *http.Request, p store.Project) ingressView {
	v := ingressView{Project: p.Name, Nodes: []ingressNode{}, Targets: []string{}, Pending: []string{}}
	if s.ingress == nil {
		return v
	}
	for _, n := range s.ingress.Nodes(p.Name) {
		in := ingressNode{Name: n, Address: s.ingress.Addr(n), State: "unknown"}
		if s.edges != nil {
			if ok, known := s.edges.Healthy(n); known {
				in.State = map[bool]string{true: "healthy", false: "unhealthy"}[ok]
			}
		}
		v.Nodes = append(v.Nodes, in)
	}
	if t := s.ingress.Expected(r.Context(), p.Name); t != nil {
		v.Targets = t
	}
	v.Base = s.ingress.Base(p.Name, s.base())
	v.Move = s.ingress.Move(p.Name)
	if pend := s.ingress.Pending(r.Context(), p.Name); pend != nil {
		v.Pending = pend
	}
	return v
}

func (s *Server) requireIngress(w http.ResponseWriter) bool {
	if s.ingress == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "project load balancers are not enabled")
		return false
	}
	return true
}

// handleGetProjectIngress shows who serves a project's public traffic.
func (s *Server) handleGetProjectIngress(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok || !s.requireIngress(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.ingressView(r, p))
}

// handleSetProjectIngress chooses the nodes that serve a project's public
// traffic (empty = the controller and edge pools, D22). The previous
// ingress keeps serving each custom domain until its DNS moves.
func (s *Server) handleSetProjectIngress(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok || !s.requireIngress(w) {
		return
	}
	var req struct {
		Nodes []string `json:"nodes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	nodes := []string{}
	for _, n := range req.Nodes {
		n = strings.TrimSpace(n)
		if !workload.ValidNodeName(n) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("%q is not a node name", n))
			return
		}
		if n == mesh.ControllerNode {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "the controller is the default load balancer: choose no nodes to use it")
			return
		}
		if _, err := s.store.NodeByName(r.Context(), n); errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "no node "+n)
			return
		} else if err != nil {
			s.internalError(w, "get node", err)
			return
		}
		if !slices.Contains(nodes, n) {
			nodes = append(nodes, n) // the order stays: the first gives the generated address
		}
	}
	if len(nodes) > ingress.MaxNodes {
		writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("at most %d nodes", ingress.MaxNodes))
		return
	}
	p, err := s.ingress.Set(r.Context(), p.ID, nodes)
	if err != nil && !errors.Is(err, ingress.ErrNoChange) {
		s.internalError(w, "set project ingress", err)
		return
	}
	if err == nil {
		u, _ := currentUser(r.Context())
		s.audit(r, u.ID, "project:SetProjectIngress", projectSRN(p.Name), map[string]any{"nodes": nodes})
	}
	writeJSON(w, http.StatusOK, s.ingressView(r, p))
}

// handleCutOverProjectIngress stops the previous ingress serving a project
// now, whether or not its domains' DNS has moved.
func (s *Server) handleCutOverProjectIngress(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok || !s.requireIngress(w) {
		return
	}
	p, err := s.ingress.CutOver(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, "cut over project ingress", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:CutOverProjectIngress", projectSRN(p.Name), nil)
	writeJSON(w, http.StatusOK, s.ingressView(r, p))
}
