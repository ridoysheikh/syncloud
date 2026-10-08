package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ridoysheikh/syncloud/internal/upgrade"
	"github.com/ridoysheikh/syncloud/internal/upgrade/rollout"
	"github.com/ridoysheikh/syncloud/internal/version"
)

// systemTaskGrace is how long system tasks may take to start before the
// controller reports itself not ready.
const systemTaskGrace = 90 * time.Second

// handleHealth is the readiness check upgrades (and load balancers) use:
// 200 when the database answers and every system task runs, else 503.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	h := upgrade.Health{Version: version.Version, Problems: []string{}}
	if err := s.store.W.PingContext(r.Context()); err != nil {
		h.Problems = append(h.Problems, "database: "+err.Error())
	}
	if s.system != nil && s.system.NodeID() != "" && time.Since(s.startedAt) > systemTaskGrace {
		for _, t := range s.system.List() {
			if t.State != "running" || t.Health == "unhealthy" {
				h.Problems = append(h.Problems, fmt.Sprintf("system task %s is %s", t.Name, t.State))
			}
		}
	}
	h.Ready = len(h.Problems) == 0
	status := http.StatusOK
	if !h.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, h)
}

type upgradeResponse struct {
	Current     string         `json:"current"`
	Channel     string         `json:"channel"`
	Latest      string         `json:"latest,omitempty"`
	LatestError string         `json:"latestError,omitempty"`
	Available   bool           `json:"available"`
	State       *upgrade.State `json:"state"`
}

func (s *Server) handleGetUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.upgrades == nil {
		writeError(w, http.StatusNotFound, "not_found", "upgrades are not available")
		return
	}
	resp := upgradeResponse{Current: version.Version, Channel: s.upgrades.Channel}
	latest, err := s.upgrades.Latest(r.Context(), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		resp.LatestError = err.Error()
	} else {
		resp.Latest, resp.Available = latest, latest != version.Version
	}
	if st, ok := s.upgrades.State(); ok {
		st.Run = upgrade.RunInfo{} // how the controller runs (argv, environment) stays private
		resp.State = &st
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStartUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.upgrades == nil {
		writeError(w, http.StatusNotFound, "not_found", "upgrades are not available")
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.upgrades.Start(r.Context(), req.Version); err != nil {
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	s.handleGetUpgradeStatus(w, r, http.StatusAccepted)
}

func (s *Server) handleGetUpgradeStatus(w http.ResponseWriter, _ *http.Request, status int) {
	resp := upgradeResponse{Current: version.Version, Channel: s.upgrades.Channel}
	if st, ok := s.upgrades.State(); ok {
		st.Run = upgrade.RunInfo{}
		resp.State = &st
	}
	writeJSON(w, status, resp)
}

type agentNode struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Arch      string `json:"arch"`
	Connected bool   `json:"connected"`
	Outdated  bool   `json:"outdated"`
}

func (s *Server) agentUpgradeView() map[string]any {
	target := version.Version
	var nodes []agentNode
	for _, v := range s.nodes.List() {
		nodes = append(nodes, agentNode{ID: v.ID, Name: v.Name, Version: v.Info.AgentVersion, Arch: v.Info.Arch, Connected: v.Connected,
			Outdated: v.Info.AgentVersion != "" && v.Info.AgentVersion != target})
	}
	var ro *rollout.Rollout
	if s.agentRollout != nil {
		ro = s.agentRollout.Status()
	}
	return map[string]any{"target": target, "nodes": nodes, "rollout": ro}
}

func (s *Server) handleGetAgentUpgrade(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.agentUpgradeView())
}

func (s *Server) handleStartAgentUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.agentRollout == nil {
		writeError(w, http.StatusNotFound, "not_found", "agent upgrades are not available")
		return
	}
	var req struct {
		Nodes []string `json:"nodes"`
		Force bool     `json:"force"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := s.agentRollout.Start(req.Nodes, req.Force); errors.Is(err, rollout.ErrRunning) {
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.agentUpgradeView())
}
