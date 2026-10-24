package api

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/fwstats"
	"github.com/ridoysheikh/syncloud/internal/hostfw"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func (s *Server) requireHostFW(w http.ResponseWriter) bool {
	if s.hostFW == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "the host firewall is not enabled")
		return false
	}
	return true
}

func (s *Server) handleGetFirewallSSH(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostFW(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.hostFW.SSH())
}

// handleSetFirewallSSH changes how SSH is reached on every node. It stays
// open on 22 and sshd's ports whatever the settings (disaster recovery).
func (s *Server) handleSetFirewallSSH(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostFW(w) {
		return
	}
	var req hostfw.SSH
	if !decodeJSON(w, r, &req) {
		return
	}
	out, err := s.hostFW.SetSSH(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "firewall:SetFirewallSSH", "srn:syncloud:firewall/ssh", map[string]any{"sources": out.Sources, "rate": out.Rate, "autoBan": out.AutoBan})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListFirewallBlocks(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostFW(w) {
		return
	}
	node := r.URL.Query().Get("node")
	out := []store.FirewallBlock{}
	for _, b := range s.hostFW.Blocks() {
		if node == "" || b.Node == "" || b.Node == node {
			out = append(out, b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleCreateFirewallBlock drops a source on every node (or one) before
// anything else, for a while or until removed.
func (s *Server) handleCreateFirewallBlock(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostFW(w) {
		return
	}
	var req struct {
		Source  string `json:"source"`
		Node    string `json:"node"`
		Minutes int    `json:"minutes"` // 0 = until removed
		Reason  string `json:"reason"`
		Force   bool   `json:"force"` // block the caller's own address too
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	if req.Minutes < 0 || req.Minutes > 366*24*60 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "minutes must be 0 (until removed) to 527040")
		return
	}
	if len(req.Reason) > 200 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "reason: at most 200 characters")
		return
	}
	if req.Node != "" {
		if _, err := s.store.NodeByName(r.Context(), req.Node); errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "no node "+req.Node)
			return
		}
	}
	if !req.Force {
		if me, err := netip.ParseAddr(clientIP(r)); err == nil {
			if p, err := parseSourcePrefix(req.Source); err == nil && p.Contains(me.Unmap()) {
				writeError(w, http.StatusConflict, CodeConflict, "this would block your own address ("+me.String()+"); set force to do it anyway")
				return
			}
		}
	}
	u, _ := currentUser(r.Context())
	b, err := s.hostFW.Block(r.Context(), hostfw.BlockInput{Source: req.Source, Node: req.Node, For: time.Duration(req.Minutes) * time.Minute,
		Reason: req.Reason, By: u.Email})
	if errors.Is(err, hostfw.ErrProtected) {
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	s.audit(r, u.ID, "firewall:CreateFirewallBlock", "srn:syncloud:firewall/block/"+b.ID, map[string]any{"source": b.Source, "node": b.Node, "minutes": req.Minutes})
	writeJSON(w, http.StatusCreated, b)
}

func parseSourcePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

func (s *Server) handleDeleteFirewallBlock(w http.ResponseWriter, r *http.Request) {
	if !s.requireHostFW(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.hostFW.Unblock(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such block")
		return
	} else if err != nil {
		s.internalError(w, "unblock", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "firewall:DeleteFirewallBlock", "srn:syncloud:firewall/block/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

type nodeFirewallRule struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"` // syncloud | ssh | service | database | load-balancer | policy
	Protocol    string   `json:"protocol"`
	Ports       string   `json:"ports"`
	Sources     []string `json:"sources"`
	Description string   `json:"description"`
	Packets     uint64   `json:"packets"`
}

func ruleKind(id string) string {
	switch {
	case id == hostfw.RuleSSH:
		return "ssh"
	case strings.HasPrefix(id, "builtin:controller-"), id == "builtin:wireguard":
		return "syncloud"
	case strings.Contains(id, "service-"):
		return "service"
	case strings.Contains(id, "database"):
		return "database"
	case strings.HasPrefix(id, "builtin:edge-"):
		return "load-balancer"
	case strings.HasPrefix(id, "builtin:"):
		return "syncloud"
	}
	return "policy"
}

// handleNodeFirewall is one node's firewall at a glance (Phase 24): state,
// what is open and why, what listens and whether it is reachable, recent
// unauthorized attempts and the blocks that apply.
func (s *Server) handleNodeFirewall(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "networking is not enabled")
		return
	}
	id := r.PathValue("id")
	v, ok := s.nodes.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node")
		return
	}
	cfg, err := s.mesh.NodeConfig(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "the node has not joined the private network yet")
		return
	} else if err != nil {
		s.internalError(w, "node config", err)
		return
	}
	fw := cfg.GetFirewall()
	out := map[string]any{"enabled": fw != nil, "node": v.Name}
	if views, err := s.mesh.List(r.Context()); err == nil {
		for _, mv := range views {
			if mv.NodeID == id {
				out["state"] = map[string]any{"generation": mv.Generation, "applied": mv.AppliedGen, "pending": mv.FirewallPending,
					"error": mv.Error, "driftCorrections": mv.Drift, "updatedAt": mv.StatusUpdatedAt}
			}
		}
	}
	counters := map[string]uint64{}
	if s.fwStats != nil {
		for k, c := range s.fwStats.NodeCounters(v.Name) {
			counters[k] = c.Packets
		}
	}
	rules := []nodeFirewallRule{}
	if fw != nil {
		rules = append(rules, nodeFirewallRule{ID: "builtin:wireguard", Kind: "syncloud", Protocol: "udp", Ports: strconv.Itoa(int(cfg.GetListenPort())),
			Sources: []string{"cluster"}, Description: "private network (WireGuard)", Packets: counters["builtin:wireguard"]})
		for _, x := range fw.GetRules() {
			srcs := x.GetSources()
			if srcs == nil {
				srcs = []string{}
			}
			rules = append(rules, nodeFirewallRule{ID: x.GetId(), Kind: ruleKind(x.GetId()), Protocol: x.GetProtocol(), Ports: x.GetPorts(),
				Sources: srcs, Description: x.GetDescription(), Packets: counters[x.GetId()]})
		}
	}
	out["rules"] = rules
	guards := []map[string]any{}
	for _, g := range fw.GetGuards() {
		guards = append(guards, map[string]any{"id": g.GetId(), "protocol": g.GetProtocol(), "ports": g.GetPorts(), "rate": g.GetRate(), "unit": g.GetUnit(),
			"burst": g.GetBurst(), "maxConnections": g.GetMaxConnections(), "dropped": counters[g.GetId()]})
	}
	out["guards"] = guards
	out["blocked"] = counters["builtin:blocked"]
	out["denied"] = counters["default deny"] + counters["builtin:forward-seal"]
	listeners, at := []hostfw.Listener{}, time.Time{}
	if s.hostFW != nil {
		listeners, at = s.hostFW.Listeners(v.Name, fw, cfg.GetListenPort())
		out["sshPorts"] = s.hostFW.SSHPorts(v.Name)
		blocks := []store.FirewallBlock{}
		for _, b := range s.hostFW.Blocks() {
			if b.Node == "" || b.Node == v.Name {
				blocks = append(blocks, b)
			}
		}
		out["blocks"] = blocks
	}
	out["listeners"] = listeners
	if !at.IsZero() {
		out["listenersAt"] = at
	}
	drops := []fwstats.Drop{}
	if s.fwStats != nil {
		drops = s.fwStats.Drops(fwstats.DropFilter{Node: v.Name}, 200)
		if drops == nil {
			drops = []fwstats.Drop{}
		}
	}
	out["drops"] = drops
	writeJSON(w, http.StatusOK, out)
}
