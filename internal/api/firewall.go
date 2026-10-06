package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/firewall"
	"syncloud/internal/store"
)

var policyNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

type policyRequest struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Targets     []string             `json:"targets"`
	Rules       []store.FirewallRule `json:"rules"`
}

func (s *Server) validatePolicy(ctx context.Context, req *policyRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	if !policyNameRE.MatchString(req.Name) {
		return errors.New("name must be lowercase letters, digits and hyphens (at most 64)")
	}
	if len(req.Description) > 500 {
		return errors.New("description is too long")
	}
	if len(req.Targets) == 0 {
		req.Targets = []string{"*"}
	}
	for _, t := range req.Targets {
		if t == "*" {
			continue
		}
		if _, err := s.store.NodeByID(ctx, t); err != nil {
			return fmt.Errorf("target %q is not a node ID (or \"*\" for all nodes)", t)
		}
	}
	if len(req.Rules) > 200 {
		return errors.New("at most 200 rules per policy")
	}
	if req.Rules == nil {
		req.Rules = []store.FirewallRule{}
	}
	for i := range req.Rules {
		r := &req.Rules[i]
		r.Protocol = strings.ToLower(strings.TrimSpace(r.Protocol))
		r.Ports = strings.TrimSpace(r.Ports)
		if r.Sources == nil {
			r.Sources = []string{}
		}
		for j := range r.Sources {
			r.Sources[j] = strings.TrimSpace(r.Sources[j])
		}
		if err := firewall.ValidateRule(r.Protocol, r.Ports, r.Sources); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
		if len(r.Description) > 200 {
			return fmt.Errorf("rule %d: description is too long", i+1)
		}
	}
	return nil
}

func (s *Server) firewallChanged(ctx context.Context) {
	if s.mesh != nil {
		s.mesh.Changed(context.WithoutCancel(ctx))
	}
}

func (s *Server) handleListFirewallPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListFirewallPolicies(r.Context())
	if err != nil {
		s.internalError(w, "list firewall policies", err)
		return
	}
	if items == nil {
		items = []store.FirewallPolicy{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) savePolicy(w http.ResponseWriter, r *http.Request, id string) {
	var req policyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.validatePolicy(r.Context(), &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	now := s.now().UTC().Truncate(time.Second)
	p := store.FirewallPolicy{ID: id, Name: req.Name, Description: req.Description, Targets: req.Targets, Rules: req.Rules, CreatedAt: now, UpdatedAt: now}
	status, action := http.StatusOK, "firewall:EditHostPolicy"
	if id == "" {
		p.ID, status, action = auth.NewID("fwp_"), http.StatusCreated, "firewall:CreateHostPolicy"
	} else {
		old, err := s.store.FirewallPolicy(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such policy")
			return
		} else if err != nil {
			s.internalError(w, "get policy", err)
			return
		}
		p.CreatedAt = old.CreatedAt
	}
	if err := s.store.PutFirewallPolicy(r.Context(), p); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a policy named "+p.Name+" already exists")
		return
	} else if err != nil {
		s.internalError(w, "save policy", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, action, "srn:syncloud:firewall-policy/"+p.ID, map[string]any{"name": p.Name, "rules": len(p.Rules)})
	s.firewallChanged(r.Context())
	writeJSON(w, status, p)
}

func (s *Server) handleCreateFirewallPolicy(w http.ResponseWriter, r *http.Request) {
	s.savePolicy(w, r, "")
}

func (s *Server) handleUpdateFirewallPolicy(w http.ResponseWriter, r *http.Request) {
	s.savePolicy(w, r, r.PathValue("id"))
}

func (s *Server) handleDeleteFirewallPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteFirewallPolicy(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such policy")
		return
	} else if err != nil {
		s.internalError(w, "delete policy", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "firewall:DeleteHostPolicy", "srn:syncloud:firewall-policy/"+id, nil)
	s.firewallChanged(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

type effectiveRule struct {
	ID          string   `json:"id"`
	Protocol    string   `json:"protocol"`
	Ports       string   `json:"ports"`
	Sources     []string `json:"sources"`
	Description string   `json:"description"`
}

// handleEffectiveFirewall shows exactly what a node enforces (§8.3).
func (s *Server) handleEffectiveFirewall(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "networking is not enabled")
		return
	}
	cfg, err := s.mesh.NodeConfig(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "the node has not joined the private network yet")
		return
	} else if err != nil {
		s.internalError(w, "node config", err)
		return
	}
	ruleset, err := firewall.Render(cfg, cfg.GetFirewall(), nil)
	if err != nil {
		s.internalError(w, "render firewall", err)
		return
	}
	rules := []effectiveRule{}
	for _, x := range cfg.GetFirewall().GetRules() {
		srcs := x.GetSources()
		if srcs == nil {
			srcs = []string{}
		}
		rules = append(rules, effectiveRule{ID: x.GetId(), Protocol: x.GetProtocol(), Ports: x.GetPorts(), Sources: srcs, Description: x.GetDescription()})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": cfg.GetFirewall() != nil, "rules": rules, "clusterSources": nonNil(cfg.GetFirewall().GetClusterSources()), "ruleset": ruleset,
	})
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
