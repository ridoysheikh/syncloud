package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/cloud"
	"syncloud/internal/nodepool"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

// Node pools and cloud providers (§6.5).

func (s *Server) requirePools(w http.ResponseWriter) bool {
	if s.pools == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "node pools are not enabled")
		return false
	}
	return true
}

type providerView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListCloudProviders(r.Context())
	if err != nil {
		s.internalError(w, "list providers", err)
		return
	}
	out := []providerView{}
	for _, p := range ps {
		out = append(out, providerView{p.ID, p.Name, p.Type, p.Summary, p.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "types": cloud.Types})
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requirePools(w) {
		return
	}
	var req struct {
		Name   string       `json:"name"`
		Type   string       `json:"type"`
		Config cloud.Config `json:"config"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !policyNameRE.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name must be lowercase letters, digits and hyphens")
		return
	}
	summary, err := req.Config.Validate(req.Type)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	id := auth.NewID("cp_")
	p := store.CloudProvider{ID: id, Name: req.Name, Type: req.Type, ConfigEnc: s.pools.SealProvider(id, req.Config), Summary: summary, CreatedAt: s.now().Truncate(time.Second)}
	if err := s.store.PutCloudProvider(r.Context(), p); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a provider named "+req.Name+" already exists")
		return
	} else if err != nil {
		s.internalError(w, "save provider", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "nodepool:CreateProvider", "srn:syncloud:cloud-provider/"+id, map[string]any{"type": req.Type})
	writeJSON(w, http.StatusCreated, providerView{p.ID, p.Name, p.Type, p.Summary, p.CreatedAt})
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch err := s.store.DeleteCloudProvider(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such provider")
		return
	case errors.Is(err, store.ErrInUse):
		writeError(w, http.StatusConflict, CodeConflict, "node pools use this provider")
		return
	case err != nil:
		s.internalError(w, "delete provider", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "nodepool:DeleteProvider", "srn:syncloud:cloud-provider/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

type poolNode struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Reserved    float64 `json:"reservedPercent"`
	Tasks       int     `json:"tasks"`
	Protected   bool    `json:"scaleInProtected"`
	Draining    bool    `json:"draining"`
	PublicIP    string  `json:"address,omitempty"`
	Schedulable bool    `json:"schedulable"`
}

type poolView struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Role     string             `json:"role"`
	Provider string             `json:"provider"` // name; "" for a manual pool
	Spec     nodepool.Spec      `json:"spec"`
	Min      int                `json:"min"`
	Max      int                `json:"max"`
	Nodes    []poolNode         `json:"nodes"`
	Servers  []store.PoolServer `json:"servers"`
	CPU      [2]float64         `json:"cpu"`    // reserved, allocatable (cores)
	Memory   [2]int             `json:"memory"` // reserved, allocatable (MiB)
}

func (s *Server) poolViews(r *http.Request) ([]poolView, error) {
	ctx := r.Context()
	pools, err := s.store.ListNodePools(ctx)
	if err != nil {
		return nil, err
	}
	provs, _ := s.store.ListCloudProviders(ctx)
	pname := map[string]string{}
	for _, p := range provs {
		pname[p.ID] = p.Name
	}
	tasks, _ := s.store.ActiveTasks(ctx)
	type use struct {
		cpu   float64
		mem   int
		tasks int
	}
	used := map[string]*use{}
	for _, t := range tasks {
		if t.Desired != "running" {
			continue
		}
		u := used[t.NodeID]
		if u == nil {
			u = &use{}
			used[t.NodeID] = u
		}
		if spec, err := s.workloads.SpecFor(ctx, t.ServiceID, t.Revision); err == nil {
			u.cpu += spec.Resources.CPU
			u.mem += spec.Resources.Memory
		}
		u.tasks++
	}
	views := s.nodes.List()
	addr := map[string]string{} // node ID -> public address (its WireGuard endpoint)
	if nets, err := s.store.ListNodeNetworks(ctx); err == nil {
		for _, n := range nets {
			if host, _, err := net.SplitHostPort(n.Endpoint); err == nil {
				addr[n.NodeID] = host
			}
		}
	}
	out := []poolView{}
	// The default pool: nodes outside any pool.
	all := append([]store.NodePool{{ID: "", Name: "default", Role: "worker"}}, pools...)
	for _, p := range all {
		var spec nodepool.Spec
		_ = json.Unmarshal([]byte(p.Spec), &spec)
		v := poolView{ID: p.ID, Name: p.Name, Role: p.Role, Provider: pname[p.ProviderID], Spec: spec, Min: p.Min, Max: p.Max, Nodes: []poolNode{}, Servers: []store.PoolServer{}}
		for _, n := range views {
			if n.PoolID != p.ID {
				continue
			}
			ac, am := workload.Allocatable(n.Info.CPUCores, n.Info.MemoryBytes)
			pn := poolNode{ID: n.ID, Name: n.Name, Status: n.Status, Protected: n.ScaleInProtected, Draining: n.Draining, PublicIP: addr[n.ID], Schedulable: n.Schedulable}
			if u := used[n.ID]; u != nil {
				pn.Tasks = u.tasks
				pct := 0.0
				if ac > 0 {
					pct = u.cpu / ac
				}
				if am > 0 && float64(u.mem)/float64(am) > pct {
					pct = float64(u.mem) / float64(am)
				}
				pn.Reserved = float64(int(pct*1000)) / 10
				v.CPU[0] += u.cpu
				v.Memory[0] += u.mem
			}
			v.CPU[1] += ac
			v.Memory[1] += am
			v.Nodes = append(v.Nodes, pn)
		}
		if p.ID != "" {
			srv, _ := s.store.ListPoolServers(ctx, p.ID)
			for _, x := range srv {
				if x.State != "joined" {
					v.Servers = append(v.Servers, x)
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) handleListNodePools(w http.ResponseWriter, r *http.Request) {
	if !s.requirePools(w) {
		return
	}
	vs, err := s.poolViews(r)
	if err != nil {
		s.internalError(w, "list pools", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": vs})
}

type poolRequest struct {
	Name     string        `json:"name"`
	Role     string        `json:"role"`
	Provider string        `json:"provider"` // name; "" for manual
	Spec     nodepool.Spec `json:"spec"`
	Min      int           `json:"min"`
	Max      int           `json:"max"`
}

func (s *Server) findPool(r *http.Request, ref string) (store.NodePool, bool) {
	ps, _ := s.store.ListNodePools(r.Context())
	for _, p := range ps {
		if p.ID == ref || p.Name == ref {
			return p, true
		}
	}
	return store.NodePool{}, false
}

func (s *Server) handleCreateNodePool(w http.ResponseWriter, r *http.Request) {
	s.saveNodePool(w, r, "")
}

func (s *Server) handleUpdateNodePool(w http.ResponseWriter, r *http.Request) {
	s.saveNodePool(w, r, r.PathValue("pool"))
}

func (s *Server) saveNodePool(w http.ResponseWriter, r *http.Request, ref string) {
	if !s.requirePools(w) {
		return
	}
	var req poolRequest
	req.Role = "worker"
	if !decodeJSON(w, r, &req) {
		return
	}
	var existing store.NodePool
	if ref != "" {
		var ok bool
		if existing, ok = s.findPool(r, ref); !ok {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such node pool")
			return
		}
		if req.Name == "" {
			req.Name = existing.Name
		}
	}
	providerID := ""
	if req.Provider != "" {
		ps, _ := s.store.ListCloudProviders(r.Context())
		for _, p := range ps {
			if p.Name == req.Provider || p.ID == req.Provider {
				providerID = p.ID
			}
		}
		if providerID == "" {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "no provider "+req.Provider)
			return
		}
	}
	if !req.Spec.Autoscale && providerID != "" && req.Max == 0 {
		req.Max = req.Min
	}
	if err := nodepool.Validate(req.Name, req.Role, providerID != "", &req.Spec, req.Min, req.Max); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	spec, _ := json.Marshal(req.Spec)
	now := s.now().Truncate(time.Second)
	p := store.NodePool{ID: auth.NewID("pool_"), Name: req.Name, Role: req.Role, ProviderID: providerID, Spec: string(spec), Min: req.Min, Max: req.Max, CreatedAt: now, UpdatedAt: now}
	status, action := http.StatusCreated, "nodepool:CreateNodePool"
	if ref != "" {
		p.ID, p.CreatedAt, status, action = existing.ID, existing.CreatedAt, http.StatusOK, "nodepool:UpdateNodePool"
	}
	if err := s.store.PutNodePool(r.Context(), p); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a node pool named "+p.Name+" already exists")
		return
	} else if err != nil {
		s.internalError(w, "save pool", err)
		return
	}
	_ = s.pools.Reload(r.Context())
	if s.workloads != nil {
		s.workloads.EnqueueAll()
	}
	if s.mesh != nil {
		s.mesh.Changed(r.Context()) // edge pools open 80/443
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, action, "srn:syncloud:node-pool/"+p.Name, map[string]any{"role": p.Role, "min": p.Min, "max": p.Max})
	vs, _ := s.poolViews(r)
	for _, v := range vs {
		if v.ID == p.ID {
			writeJSON(w, status, v)
			return
		}
	}
	w.WriteHeader(status)
}

func (s *Server) handleDeleteNodePool(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findPool(r, r.PathValue("pool"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node pool")
		return
	}
	if err := s.store.DeleteNodePool(r.Context(), p.ID); errors.Is(err, store.ErrInUse) {
		writeError(w, http.StatusConflict, CodeConflict, "the pool still has nodes: set min and max to 0 and wait, or move its nodes")
		return
	} else if err != nil {
		s.internalError(w, "delete pool", err)
		return
	}
	_ = s.pools.Reload(r.Context())
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "nodepool:DeleteNodePool", "srn:syncloud:node-pool/"+p.Name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNodePoolEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findPool(r, r.PathValue("pool"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node pool")
		return
	}
	es, err := s.store.ListPoolEvents(r.Context(), p.ID, 200)
	if err != nil {
		s.internalError(w, "pool events", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": es})
}

// handlePoolJoinCommand returns the one-line join for a manual pool.
func (s *Server) handlePoolJoinCommand(w http.ResponseWriter, r *http.Request) {
	if !s.requirePools(w) {
		return
	}
	p, ok := s.findPool(r, r.PathValue("pool"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node pool")
		return
	}
	tok, err := s.pools.JoinToken(r.Context(), p.ID, "", 24*time.Hour)
	if err != nil {
		s.internalError(w, "join token", err)
		return
	}
	base := ""
	if s.domains != nil {
		base = s.domains.Endpoints().DashboardURL
	}
	if base == "" {
		scheme := "http"
		if isHTTPS(r) {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "nodepool:CreatePoolJoinToken", "srn:syncloud:node-pool/"+p.Name, nil)
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "command": "curl -fsSL " + base + "/join.sh | sudo bash -s -- --token " + tok, "expiresIn": 86400})
}

// handleSetNodePool moves a node into a pool and sets its scale-in protection.
func (s *Server) handleSetNodePool(w http.ResponseWriter, r *http.Request) {
	if !s.requirePools(w) {
		return
	}
	var req struct {
		Pool             string `json:"pool"` // name; "" or "default" for none
		ScaleInProtected bool   `json:"scaleInProtected"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	poolID := ""
	if req.Pool != "" && req.Pool != "default" {
		p, ok := s.findPool(r, req.Pool)
		if !ok {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "no node pool "+req.Pool)
			return
		}
		poolID = p.ID
	}
	id := r.PathValue("id")
	if err := s.store.SetNodePool(r.Context(), id, poolID, req.ScaleInProtected); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node")
		return
	} else if err != nil {
		s.internalError(w, "set node pool", err)
		return
	}
	s.nodes.SetPool(r.Context(), id, poolID, req.ScaleInProtected)
	if s.workloads != nil {
		s.workloads.EnqueueAll()
	}
	if s.mesh != nil {
		s.mesh.Changed(r.Context())
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "node:SetNodePool", "srn:syncloud:node/"+id, map[string]any{"pool": strings.TrimSpace(req.Pool), "scaleInProtected": req.ScaleInProtected})
	v, _ := s.nodes.Get(id)
	writeJSON(w, http.StatusOK, v)
}

// handleListEdges shows the edge nodes' Traefik replicas and their health
// (§8.5), with the addresses public DNS should point at.
func (s *Server) handleListEdges(w http.ResponseWriter, r *http.Request) {
	if s.edges == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.edges.List()})
}

// handleRejoinToken issues a single-use token that re-joins an existing node
// (a reinstalled or restored host keeps the node's ID, pool and tasks).
func (s *Server) handleRejoinToken(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.NodeByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		n, err = s.store.NodeByName(r.Context(), r.PathValue("id"))
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node")
		return
	} else if err != nil {
		s.internalError(w, "node", err)
		return
	}
	tok := auth.NewToken("SYN-JOIN-")
	now := s.now()
	u, _ := currentUser(r.Context())
	if err := s.store.CreateJoinToken(r.Context(), store.JoinToken{
		ID: auth.NewID("jt_"), TokenHash: auth.HashToken(tok), Description: "re-join " + n.Name, CreatedBy: u.ID,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), SingleUse: true, NodeName: n.Name, PoolID: n.PoolID,
	}); err != nil {
		s.internalError(w, "join token", err)
		return
	}
	base := ""
	if s.domains != nil {
		base = s.domains.Endpoints().DashboardURL
	}
	if base == "" {
		scheme := "http"
		if isHTTPS(r) {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	s.audit(r, u.ID, "node:CreateRejoinToken", "srn:syncloud:node/"+n.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "command": "curl -fsSL " + base + "/join.sh | sudo bash -s -- --token " + tok + " --name " + n.Name, "expiresIn": 86400})
}
