package main

import (
	"sort"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/alerts"
	"github.com/ridoysheikh/syncloud/internal/edge"
	"github.com/ridoysheikh/syncloud/internal/ingress"
	"github.com/ridoysheikh/syncloud/internal/traefik"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// ingressRoutes gives each route the replicas that serve it (D22): a
// generated address its project's current ingress, a custom domain also the
// previous one until its DNS moves. While a project moves, its old
// generated addresses keep answering on the previous ingress as twins.
func ingressRoutes(routes []workload.Route, ing *ingress.Manager, base string, chain func(serviceID string) ([]string, bool)) []traefik.ServiceRoute {
	out := make([]traefik.ServiceRoute, 0, len(routes))
	for _, r := range routes {
		mws, own := chain(r.ServiceID)
		sr := traefik.ServiceRoute{Name: r.Name, Host: r.Host, Servers: r.Servers, Middlewares: mws, OwnRetry: own, HealthPath: r.HealthPath,
			Path: r.Path, StripPrefix: r.StripPrefix, RedirectTo: r.RedirectTo}
		if !r.Generated {
			t := ing.Domain(r.Project, r.Host)
			sr.Ingress = &t
			out = append(out, sr)
			continue
		}
		t := ing.Current(r.Project)
		prev, moving := ing.Previous(r.Project)
		if moving {
			pb, _ := ing.PreviousBase(r.Project, base)
			switch old := ingress.SwapBase(r.Host, ing.Base(r.Project, base), pb); {
			case old == r.Host:
				t = t.Union(prev) // the same address on both
			case old != "":
				tw := sr
				tw.Name, tw.Host, tw.Ingress = r.Name+"_prev", old, &prev
				out = append(out, tw)
			}
		}
		sr.Ingress = &t
		out = append(out, sr)
	}
	return out
}

// noChain is for when only the routes' hosts matter.
func noChain(string) ([]string, bool) { return nil, false }

// ingressComponents reports each project's load balancers as one system
// component: down when every one of them fails its checks, as the project is
// then unreachable (its DNS points there, so there is no fallback).
func ingressComponents(ing *ingress.Manager, edges *edge.Manager) []alerts.ComponentState {
	byProject := map[string][]string{}
	for node, ps := range ing.Assigned() {
		for _, p := range ps {
			byProject[p] = append(byProject[p], node)
		}
	}
	var out []alerts.ComponentState
	for p := range byProject {
		nodes := ing.Nodes(p)
		if len(nodes) == 0 {
			continue // only the previous ingress of a move back to the controller
		}
		healthy, checked := 0, 0
		for _, n := range nodes {
			if ok, known := edges.Healthy(n); known {
				checked++
				if ok {
					healthy++
				}
			}
		}
		c := alerts.ComponentState{ID: "ingress:" + p, Name: "load balancers of " + p, State: "running"}
		if checked == len(nodes) && healthy == 0 {
			c.State, c.Error = "down", "no load balancer answers ("+strings.Join(nodes, ", ")+"): the project is unreachable"
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
