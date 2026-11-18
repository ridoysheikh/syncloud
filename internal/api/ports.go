package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/ports"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// The port table (Phase 26): every public port on every load balancer.

// portRow is one claim with its owner's name.
type portRow struct {
	store.PortClaim
	Owner   string `json:"owner"`             // project/env/service, or the database's name
	Project string `json:"project,omitempty"` // the owner's project ("" for standalone databases)
}

func (s *Server) requirePorts(w http.ResponseWriter) bool {
	if s.ports == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "the port table is not enabled")
		return false
	}
	return true
}

// ownerName names a port's holder.
func (s *Server) ownerName(r *http.Request, kind, id string) (name, project string) {
	switch kind {
	case store.PortOwnerService:
		if sv, err := s.store.ServiceByID(r.Context(), id); err == nil {
			return sv.Project + "/" + sv.Environment + "/" + sv.Name, sv.Project
		}
	case store.PortOwnerDatabase:
		if d, err := s.store.DatabaseByID(r.Context(), id); err == nil {
			return d.Name, d.Project
		}
	}
	return id, ""
}

// handleListPublicPorts is the whole port table, the reserved ports and the
// ports a resync could not claim.
func (s *Server) handleListPublicPorts(w http.ResponseWriter, r *http.Request) {
	if !s.requirePorts(w) {
		return
	}
	claims, err := s.ports.Table(r.Context())
	if err != nil {
		s.internalError(w, "list public ports", err)
		return
	}
	names := map[string][2]string{}
	row := func(c store.PortClaim) portRow {
		k := c.OwnerKind + "/" + c.OwnerID
		n, ok := names[k]
		if !ok {
			a, b := s.ownerName(r, c.OwnerKind, c.OwnerID)
			n = [2]string{a, b}
			names[k] = n
		}
		return portRow{PortClaim: c, Owner: n[0], Project: n[1]}
	}
	items := make([]portRow, 0, len(claims))
	for _, c := range claims {
		items = append(items, row(c))
	}
	conflicts := []portRow{}
	for _, c := range s.ports.Conflicts() {
		conflicts = append(conflicts, row(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "reserved": s.ports.ReservedList(), "conflicts": conflicts})
}

// handleCheckProjectPublicPort says whether a port can be a public port of
// a service of the project, on every node of the project's load balancer.
// ?service= (a service of the project, in ?environment=) leaves that
// service's own ports out. Holders outside the project are not named.
func (s *Server) handleCheckProjectPublicPort(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok || !s.requirePorts(w) {
		return
	}
	q := r.URL.Query()
	protocol := strings.ToLower(q.Get("protocol"))
	if protocol == "" {
		protocol = "tcp"
	}
	if protocol != "tcp" && protocol != "udp" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "protocol: tcp or udp")
		return
	}
	port, err := strconv.Atoi(q.Get("port"))
	if err != nil || port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "port: a number from 1 to 65535")
		return
	}
	owner := ports.Owner{Kind: store.PortOwnerService, Project: p.Name}
	if name := q.Get("service"); name != "" {
		e, err := s.store.EnvironmentByName(r.Context(), p.ID, q.Get("environment"))
		if err == nil {
			if sv, err := s.store.ServiceByName(r.Context(), e.ID, name); err == nil {
				owner.ID = sv.ID
			} else if !errors.Is(err, store.ErrNotFound) {
				s.internalError(w, "get service", err)
				return
			}
		}
	}
	states, err := s.ports.Check(r.Context(), owner, protocol, port, false)
	if err != nil {
		s.internalError(w, "check public port", err)
		return
	}
	state, reason := "free", ""
	for i, st := range states {
		if st.State == "taken" {
			name, project := s.ownerName(r, st.OwnerKind, st.OwnerID)
			if project != p.Name {
				name = "another project"
			} else if j := strings.Index(name, "/"); j >= 0 {
				name = name[j+1:] // env/service
			}
			states[i].Reason, states[i].OwnerID = name, ""
		}
		if state == "free" && st.State != "free" {
			state, reason = st.State, states[i].Reason
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"protocol": protocol, "port": port, "state": state, "reason": reason, "nodes": states})
}
