// Package nodepool manages node pools and the cluster autoscaler (§6.5):
// provider-backed pools create servers that join with cloud-init, grow when
// tasks cannot be placed or capacity runs low, and shrink by draining and
// deleting underused nodes.
package nodepool

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/cloud"
	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/nodes"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// Spec is a pool's settings.
type Spec struct {
	// Servers created by the provider.
	Region  string   `json:"region,omitempty"`
	Type    string   `json:"type,omitempty"`
	Image   string   `json:"image,omitempty"`
	SSHKeys []string `json:"sshKeys,omitempty"`
	// Capacity of one new node, for planning (cores, MiB).
	NodeCPU       float64 `json:"nodeCpu,omitempty"`
	NodeMemoryMiB int     `json:"nodeMemoryMiB,omitempty"`
	// Cluster autoscaling.
	Autoscale    bool `json:"autoscale"`
	Headroom     int  `json:"headroom,omitempty"`     // % reserved above which to add a node (default 80)
	ScaleInBelow int  `json:"scaleInBelow,omitempty"` // % reserved below which a node may go (default 40)
	ScaleInAfter int  `json:"scaleInAfter,omitempty"` // seconds below that first (default 600)
	PendingAfter int  `json:"pendingAfter,omitempty"` // seconds tasks wait unplaced before adding nodes (default 60)
	MaxStep      int  `json:"maxStep,omitempty"`      // nodes added or removed at once (default 2)
	JoinTimeout  int  `json:"joinTimeout,omitempty"`  // seconds a new server has to join (default 600)
}

func (s *Spec) defaults() {
	if s.Headroom == 0 {
		s.Headroom = 80
	}
	if s.ScaleInBelow == 0 {
		s.ScaleInBelow = 40
	}
	if s.ScaleInAfter == 0 {
		s.ScaleInAfter = 600
	}
	if s.PendingAfter == 0 {
		s.PendingAfter = 60
	}
	if s.MaxStep == 0 {
		s.MaxStep = 2
	}
	if s.JoinTimeout == 0 {
		s.JoinTimeout = 600
	}
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// Validate checks a pool before it is stored.
func Validate(name, role string, provider bool, s *Spec, min, max int) error {
	if !nameRE.MatchString(name) || name == "default" {
		return errors.New("name must be lowercase letters, digits and hyphens (at most 32), and not \"default\"")
	}
	if role != "worker" && role != "edge" {
		return errors.New("role must be worker or edge")
	}
	if min < 0 || max < min || max > 100 {
		return errors.New("need 0 ≤ min ≤ max ≤ 100")
	}
	s.defaults()
	if provider {
		if s.Type == "" || s.Image == "" || s.Region == "" {
			return errors.New("a provider pool needs region, type and image")
		}
		if s.NodeCPU <= 0 || s.NodeMemoryMiB < 256 {
			return errors.New("give the capacity of one server (nodeCpu cores, nodeMemoryMiB) for planning")
		}
	} else if s.Autoscale {
		return errors.New("only provider-backed pools autoscale")
	}
	if s.Headroom < 10 || s.Headroom > 100 || s.ScaleInBelow < 0 || s.ScaleInBelow >= s.Headroom {
		return errors.New("need 0 ≤ scaleInBelow < headroom ≤ 100")
	}
	if s.ScaleInAfter < 30 || s.PendingAfter < 10 || s.MaxStep < 1 || s.MaxStep > 20 || s.JoinTimeout < 60 {
		return errors.New("scaleInAfter ≥ 30s, pendingAfter ≥ 10s, maxStep 1–20, joinTimeout ≥ 60s")
	}
	return nil
}

// Manager runs pools.
type Manager struct {
	st  *store.Store
	box *secrets.Box
	reg *nodes.Registry
	wl  *workload.Manager
	bus *events.Bus
	log *slog.Logger
	now func() time.Time
	// ControllerURL is the address new servers join through (public).
	ControllerURL func() string
	// Pin is the controller certificate's key pin while it is self-signed.
	Pin func() string

	mu       sync.Mutex
	pools    map[string]store.NodePool // by ID
	lowSince map[string]time.Time      // node ID -> below the scale-in threshold since
	draining map[string]string         // node ID -> pool ID, being removed
}

func New(st *store.Store, box *secrets.Box, reg *nodes.Registry, wl *workload.Manager, bus *events.Bus, log *slog.Logger) *Manager {
	return &Manager{st: st, box: box, reg: reg, wl: wl, bus: bus, log: log, now: time.Now,
		pools: map[string]store.NodePool{}, lowSince: map[string]time.Time{}, draining: map[string]string{}}
}

// Reload refreshes the pool cache (after any change).
func (m *Manager) Reload(ctx context.Context) error {
	ps, err := m.st.ListNodePools(ctx)
	if err != nil {
		return err
	}
	by := map[string]store.NodePool{}
	for _, p := range ps {
		by[p.ID] = p
	}
	m.mu.Lock()
	m.pools = by
	m.mu.Unlock()
	return nil
}

// PoolOf names a node's pool and role (workload.Manager.NodePool).
func (m *Manager) PoolOf(nodeID string) (string, string) {
	v, ok := m.reg.Get(nodeID)
	if !ok || v.PoolID == "" {
		return "", "worker"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[v.PoolID]; ok {
		return p.Name, p.Role
	}
	return "", "worker"
}

// EdgeNodes lists the nodes of edge pools (they run Traefik replicas, §8.5).
func (m *Manager) EdgeNodes() []nodes.View {
	m.mu.Lock()
	edge := map[string]bool{}
	for id, p := range m.pools {
		if p.Role == "edge" {
			edge[id] = true
		}
	}
	m.mu.Unlock()
	var out []nodes.View
	for _, v := range m.reg.List() {
		if edge[v.PoolID] {
			out = append(out, v)
		}
	}
	return out
}

// Provider returns a pool provider's client.
func (m *Manager) Provider(ctx context.Context, id string) (cloud.Provider, error) {
	ps, err := m.st.ListCloudProviders(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.ID == id {
			raw, err := m.box.Open(p.ConfigEnc, []byte("provider:"+p.ID))
			if err != nil {
				return nil, err
			}
			var c cloud.Config
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			return cloud.New(p.Type, c)
		}
	}
	return nil, store.ErrNotFound
}

// SealProvider seals a provider configuration.
func (m *Manager) SealProvider(id string, c cloud.Config) []byte {
	b, _ := json.Marshal(c)
	return m.box.Seal(b, []byte("provider:"+id))
}

func randName() string {
	const a = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = a[int(b[i])%len(a)]
	}
	return string(b)
}

// JoinToken creates a single-use token that puts a node in a pool.
func (m *Manager) JoinToken(ctx context.Context, poolID, nodeName string, ttl time.Duration) (string, error) {
	tok := auth.NewToken("SYN-JOIN-")
	now := m.now().Truncate(time.Second)
	err := m.st.CreateJoinToken(ctx, store.JoinToken{ID: auth.NewID("jt_"), TokenHash: auth.HashToken(tok), Description: "node pool " + poolID,
		CreatedAt: now, ExpiresAt: now.Add(ttl), SingleUse: true, PoolID: poolID, NodeName: nodeName})
	return tok, err
}

// UserData is the cloud-init script a new server runs to join.
func UserData(controller, token, name, pin string) string {
	if pin != "" { // a self-signed controller certificate: pin its key
		return fmt.Sprintf("#!/bin/bash\n# SynCloud node pool server: join the cluster (§6.5)\ncurl -fsSLk --pinnedpubkey '%s' %s/join.sh | bash -s -- --pin '%s' --token %s --name %s\n", pin, controller, pin, token, name)
	}
	return fmt.Sprintf("#!/bin/bash\n# SynCloud node pool server: join the cluster (§6.5)\ncurl -fsSL %s/join.sh | bash -s -- --token %s --name %s\n", controller, token, name)
}

func (m *Manager) event(ctx context.Context, poolID, kind, msg string) {
	_ = m.st.AddPoolEvent(ctx, store.PoolEvent{PoolID: poolID, At: m.now().UTC(), Kind: kind, Message: msg})
	m.log.Info("node pool", "pool", poolID, "kind", kind, "msg", msg)
	m.bus.Publish("nodepool.event", map[string]string{"poolId": poolID, "kind": kind, "message": msg})
}

// Run reconciles pools every 15 seconds until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := m.Reload(ctx); err != nil {
			continue
		}
		m.mu.Lock()
		pools := make([]store.NodePool, 0, len(m.pools))
		for _, p := range m.pools {
			pools = append(pools, p)
		}
		m.mu.Unlock()
		for _, p := range pools {
			if p.ProviderID != "" {
				m.reconcile(ctx, p)
			}
		}
	}
}

// load is a node's reservation against its allocatable capacity.
type load struct {
	cpu, allocCPU float64
	mem, allocMem int
	tasks         int
}

func (l load) pct() float64 {
	p := 0.0
	if l.allocCPU > 0 {
		p = l.cpu / l.allocCPU
	}
	if l.allocMem > 0 {
		p = math.Max(p, float64(l.mem)/float64(l.allocMem))
	}
	return p * 100
}

func (m *Manager) loads(ctx context.Context, members []nodes.View) map[string]*load {
	out := map[string]*load{}
	for _, v := range members {
		cpu, mem := workload.Allocatable(v.Info.CPUCores, v.Info.MemoryBytes)
		out[v.ID] = &load{allocCPU: cpu, allocMem: mem}
	}
	tasks, err := m.st.ActiveTasks(ctx)
	if err != nil {
		return out
	}
	for _, t := range tasks {
		l := out[t.NodeID]
		if l == nil || t.Desired != "running" {
			continue
		}
		if s, err := m.wl.SpecFor(ctx, t.ServiceID, t.Revision); err == nil {
			l.cpu += s.Resources.CPU
			l.mem += s.Resources.Memory
		}
		l.tasks++
	}
	return out
}

func (m *Manager) reconcile(ctx context.Context, p store.NodePool) {
	var spec Spec
	_ = json.Unmarshal([]byte(p.Spec), &spec)
	spec.defaults()
	prov, err := m.Provider(ctx, p.ProviderID)
	if err != nil {
		m.log.Warn("node pool provider", "pool", p.Name, "err", err)
		return
	}
	now := m.now().UTC()
	var members []nodes.View
	byName := map[string]nodes.View{}
	for _, v := range m.reg.List() {
		if v.PoolID == p.ID {
			members = append(members, v)
		}
		byName[v.Name] = v
	}

	// New servers: joined, or given up on after the join timeout.
	servers, _ := m.st.ListPoolServers(ctx, p.ID)
	creating := 0
	for _, s := range servers {
		switch s.State {
		case "creating":
			if v, ok := byName[s.Name]; ok {
				s.State, s.NodeID, s.UpdatedAt = "joined", v.ID, now
				_ = m.st.PutPoolServer(ctx, s)
				m.event(ctx, p.ID, "info", fmt.Sprintf("server %s joined as node %s", s.Name, v.Name))
				continue
			}
			if now.Sub(s.CreatedAt) > time.Duration(spec.JoinTimeout)*time.Second {
				err := prov.DeleteServer(ctx, s.ServerID)
				s.State, s.UpdatedAt, s.Message = "failed", now, fmt.Sprintf("did not join within %ds", spec.JoinTimeout)
				if err != nil {
					s.Message += "; delete failed: " + err.Error()
				}
				_ = m.st.PutPoolServer(ctx, s)
				m.event(ctx, p.ID, "failed", fmt.Sprintf("server %s %s and was deleted", s.Name, s.Message))
				continue
			}
			creating++
		case "joined":
			if _, ok := byName[s.Name]; !ok { // node removed by hand: the server goes too
				_ = prov.DeleteServer(ctx, s.ServerID)
				_ = m.st.DeletePoolServer(ctx, p.ID, s.ServerID)
			}
		}
	}

	// Nodes being removed: delete once their tasks have moved.
	m.finishDrains(ctx, p, prov, servers)

	if !spec.Autoscale && len(members)+creating >= p.Min {
		return
	}
	count := len(members) + creating
	need, reason := 0, ""
	if count < p.Min {
		need, reason = p.Min-count, fmt.Sprintf("below the minimum of %d nodes", p.Min)
	}
	allocCPU, allocMem := workload.Allocatable(int(math.Ceil(spec.NodeCPU)), uint64(spec.NodeMemoryMiB)<<20)
	if spec.Autoscale {
		// Tasks no node can take, waiting long enough.
		var cpu float64
		var mem, n int
		for _, u := range m.wl.UnplacedDemand() {
			if now.Sub(u.Since) < time.Duration(spec.PendingAfter)*time.Second || !m.eligible(p, u.Pools) {
				continue
			}
			cpu += u.CPU * float64(u.Count)
			mem += u.MemoryMiB * u.Count
			n += u.Count
		}
		if n > 0 && allocCPU > 0 && allocMem > 0 {
			nodesNeeded := int(math.Max(math.Ceil(cpu/allocCPU), math.Ceil(float64(mem)/float64(allocMem))))
			nodesNeeded = max(nodesNeeded, 1) - creating
			if nodesNeeded > need {
				need, reason = nodesNeeded, fmt.Sprintf("%d task(s) waiting for capacity (%.2g cores, %d MiB)", n, cpu, mem)
			}
		}
		// Headroom over the pool's ready nodes.
		loads := m.loads(ctx, members)
		var used, alloc float64
		for _, v := range members {
			if l := loads[v.ID]; l != nil && v.Status == store.NodeReady && v.Schedulable {
				used += l.pct() * (l.allocCPU + 1)
				alloc += 100 * (l.allocCPU + 1)
			}
		}
		if creating == 0 && need == 0 && alloc > 0 && used/alloc*100 > float64(spec.Headroom) && count < p.Max {
			need, reason = 1, fmt.Sprintf("reservations at %.0f%% of the pool (headroom %d%%)", used/alloc*100, spec.Headroom)
		}
	}
	need = min(need, spec.MaxStep, p.Max-count)
	if need > 0 {
		m.scaleOut(ctx, p, spec, prov, need, reason)
		return
	}
	if spec.Autoscale && creating == 0 {
		m.maybeScaleIn(ctx, p, spec, members)
	}
}

// eligible reports whether demand with these placement pools may use p.
func (m *Manager) eligible(p store.NodePool, pools []string) bool {
	if len(pools) == 0 {
		return p.Role == "worker"
	}
	return slices.Contains(pools, p.Name)
}

func (m *Manager) scaleOut(ctx context.Context, p store.NodePool, spec Spec, prov cloud.Provider, n int, reason string) {
	controller := ""
	if m.ControllerURL != nil {
		controller = m.ControllerURL()
	}
	pin := ""
	if m.Pin != nil {
		pin = m.Pin()
	}
	var created []string
	for range n {
		name := p.Name + "-" + randName()
		tok, err := m.JoinToken(ctx, p.ID, name, time.Duration(spec.JoinTimeout+600)*time.Second)
		if err != nil {
			m.event(ctx, p.ID, "failed", "join token: "+err.Error())
			return
		}
		srv, err := prov.CreateServer(ctx, cloud.ServerSpec{Name: name, Region: spec.Region, Type: spec.Type, Image: spec.Image, SSHKeys: spec.SSHKeys,
			UserData: UserData(controller, tok, name, pin), Labels: map[string]string{"syncloud-pool": p.Name, "syncloud-node": name}})
		if err != nil {
			m.event(ctx, p.ID, "failed", fmt.Sprintf("create server %s: %v", name, err))
			return
		}
		now := m.now().UTC()
		_ = m.st.PutPoolServer(ctx, store.PoolServer{PoolID: p.ID, ServerID: srv.ID, Name: name, State: "creating", CreatedAt: now, UpdatedAt: now})
		created = append(created, name)
	}
	m.event(ctx, p.ID, "scale-out", fmt.Sprintf("+%d: %s (%s)", len(created), reason, strings.Join(created, ", ")))
}

// maybeScaleIn drains one node that has been underused for long enough and
// whose tasks fit on the pool's other nodes.
func (m *Manager) maybeScaleIn(ctx context.Context, p store.NodePool, spec Spec, members []nodes.View) {
	m.mu.Lock()
	for id, pool := range m.draining {
		if pool == p.ID {
			m.mu.Unlock()
			return // one at a time
		}
		_ = id
	}
	m.mu.Unlock()
	if len(members) <= p.Min {
		return
	}
	if e, err := m.st.LastPoolEvent(ctx, p.ID, "scale-out"); err == nil && m.now().Sub(e.At) < time.Duration(spec.ScaleInAfter)*time.Second {
		return // let new nodes settle
	}
	loads := m.loads(ctx, members)
	now := m.now()
	var victim *nodes.View
	var victimPct float64
	for i := range members {
		v := members[i]
		l := loads[v.ID]
		if l == nil || v.Status != store.NodeReady || v.ScaleInProtected || !v.Schedulable {
			continue
		}
		pct := l.pct()
		m.mu.Lock()
		if pct >= float64(spec.ScaleInBelow) {
			delete(m.lowSince, v.ID)
			m.mu.Unlock()
			continue
		}
		since, ok := m.lowSince[v.ID]
		if !ok {
			m.lowSince[v.ID] = now
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()
		if now.Sub(since) < time.Duration(spec.ScaleInAfter)*time.Second {
			continue
		}
		// Its reservations must fit in the free room of the others.
		var freeCPU float64
		var freeMem int
		for _, o := range members {
			if o.ID == v.ID || o.Status != store.NodeReady || !o.Schedulable {
				continue
			}
			if ol := loads[o.ID]; ol != nil {
				freeCPU += ol.allocCPU - ol.cpu
				freeMem += ol.allocMem - ol.mem
			}
		}
		if l.cpu > freeCPU || l.mem > freeMem {
			continue
		}
		if victim == nil || pct < victimPct {
			victim, victimPct = &v, pct
		}
	}
	if victim == nil {
		return
	}
	if err := m.st.DrainNode(ctx, victim.ID); err != nil {
		return
	}
	m.reg.SetSchedulable(ctx, victim.ID, false, true)
	m.wl.EnqueueAll()
	m.mu.Lock()
	m.draining[victim.ID] = p.ID
	delete(m.lowSince, victim.ID)
	m.mu.Unlock()
	m.event(ctx, p.ID, "scale-in", fmt.Sprintf("draining %s: reservations at %.0f%% for %ds, its tasks fit on the other nodes", victim.Name, victimPct, spec.ScaleInAfter))
}

func (m *Manager) finishDrains(ctx context.Context, p store.NodePool, prov cloud.Provider, servers []store.PoolServer) {
	m.mu.Lock()
	var ids []string
	for id, pool := range m.draining {
		if pool == p.ID {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		tasks, err := m.st.NodeTasks(ctx, id)
		if err != nil || len(tasks) > 0 {
			continue
		}
		v, _ := m.reg.Get(id)
		for _, s := range servers {
			if s.Name == v.Name || s.NodeID == id {
				if err := prov.DeleteServer(ctx, s.ServerID); err != nil {
					m.event(ctx, p.ID, "failed", fmt.Sprintf("delete server %s: %v", s.Name, err))
					continue
				}
				_ = m.st.DeletePoolServer(ctx, p.ID, s.ServerID)
			}
		}
		if err := m.st.DeleteNode(ctx, id); err == nil || errors.Is(err, store.ErrNotFound) {
			m.reg.Removed(id)
		}
		m.mu.Lock()
		delete(m.draining, id)
		m.mu.Unlock()
		m.event(ctx, p.ID, "scale-in", fmt.Sprintf("removed %s and deleted its server", v.Name))
	}
}

// Draining lists nodes the autoscaler is removing.
func (m *Manager) Draining() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.draining {
		out[k] = v
	}
	return out
}
