package api

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"syncloud/internal/discovery"
	"syncloud/internal/mesh"
)

type ipamAddress struct {
	IP      string    `json:"ip"`
	Owner   string    `json:"owner"` // project/env/service
	OwnerID string    `json:"ownerId"`
	Kind    string    `json:"kind"` // task | run
	Since   time.Time `json:"since"`
}

type ipamNode struct {
	NodeID    string        `json:"nodeId"`
	Node      string        `json:"node"`
	MeshIP    string        `json:"meshIp"`
	Subnet    string        `json:"subnet"`
	Gateway   string        `json:"gateway"`
	Used      int           `json:"used"`
	Capacity  int           `json:"capacity"`
	Addresses []ipamAddress `json:"addresses"`
}

type ipamVIP struct {
	ServiceID string `json:"serviceId"`
	Service   string `json:"service"`
	VIP       string `json:"vip"`
	DNSName   string `json:"dnsName"`
	Backends  int    `json:"backends"`
}

type ipamReleased struct {
	Kind       string    `json:"kind"`
	Address    string    `json:"address"`
	ReleasedAt time.Time `json:"releasedAt"`
	ReusableAt time.Time `json:"reusableAt"`
}

// handleIPAM shows the address plan (§8.2): each node's mesh address and
// container subnet with the addresses in use, service VIPs, and addresses
// in their cool-down.
func (s *Server) handleIPAM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nets, err := s.store.ListNodeNetworks(ctx)
	if err != nil {
		s.internalError(w, "list node networks", err)
		return
	}
	svcs, err := s.store.ListServices(ctx)
	if err != nil {
		s.internalError(w, "list services", err)
		return
	}
	names := map[string]string{}
	for _, sv := range svcs {
		names[sv.ID] = sv.Project + "/" + sv.Environment + "/" + sv.Name
	}
	byNode := map[string][]ipamAddress{}
	tasks, err := s.store.ActiveTasks(ctx)
	if err != nil {
		s.internalError(w, "list tasks", err)
		return
	}
	for _, t := range tasks {
		if t.IP == "" {
			continue
		}
		since := t.CreatedAt
		if t.StartedAt != nil {
			since = *t.StartedAt
		}
		byNode[t.NodeID] = append(byNode[t.NodeID], ipamAddress{IP: t.IP, Owner: names[t.ServiceID], OwnerID: t.ID, Kind: "task", Since: since})
	}
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		s.internalError(w, "list runs", err)
		return
	}
	addrs, _ := s.store.ActiveRunAddresses(ctx)
	runIP := map[string]string{}
	for _, a := range addrs {
		runIP[a.ID] = a.IP
	}
	for _, x := range runs {
		if ip := runIP[x.ID]; ip != "" {
			owner := "job run"
			if x.ServiceID != "" {
				owner = names[x.ServiceID] + " (job run)"
			}
			since := x.CreatedAt
			if x.StartedAt != nil {
				since = *x.StartedAt
			}
			byNode[x.NodeID] = append(byNode[x.NodeID], ipamAddress{IP: ip, Owner: owner, OwnerID: x.ID, Kind: "run", Since: since})
		}
	}
	nodes := []ipamNode{}
	for _, n := range nets {
		sub := mesh.Subnet(n.SubnetIndex)
		a := byNode[n.NodeID]
		sort.Slice(a, func(i, j int) bool {
			x, _ := netip.ParseAddr(a[i].IP)
			y, _ := netip.ParseAddr(a[j].IP)
			return x.Less(y)
		})
		if a == nil {
			a = []ipamAddress{}
		}
		nodes = append(nodes, ipamNode{NodeID: n.NodeID, Node: n.NodeName, MeshIP: mesh.MeshAddr(n.MeshIndex).String(), Subnet: sub.String(),
			Gateway: sub.Addr().Next().String(), Used: len(a), Capacity: 253, Addresses: a})
	}
	vips := []ipamVIP{}
	if s.discovery != nil {
		backends := s.discovery.Backends()
		for _, sv := range svcs {
			if v := s.discovery.VIP(sv.ID); v != "" {
				vips = append(vips, ipamVIP{ServiceID: sv.ID, Service: names[sv.ID], VIP: v, DNSName: discovery.ServiceName(sv), Backends: backends[sv.ID]})
			}
		}
		sort.Slice(vips, func(i, j int) bool { return vips[i].Service < vips[j].Service })
	}
	rel, err := s.store.ListReleased(ctx)
	if err != nil {
		s.internalError(w, "list released", err)
		return
	}
	released := []ipamReleased{}
	now := s.now()
	for _, x := range rel {
		until := x.ReleasedAt.Add(mesh.Cooldown)
		if until.Before(now) {
			continue
		}
		var addr string
		switch x.Kind {
		case "mesh":
			addr = mesh.MeshAddr(x.Index).String()
		case "subnet":
			addr = mesh.Subnet(x.Index).String()
		case "vip":
			addr = discovery.VIPAddr(x.Index).String()
		default:
			addr = strconv.Itoa(x.Index)
		}
		released = append(released, ipamReleased{Kind: x.Kind, Address: addr, ReleasedAt: x.ReleasedAt, ReusableAt: until})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meshCidr": mesh.MeshCIDR.String(), "containerCidr": mesh.ContainerCIDR.String(), "serviceCidr": mesh.ServiceCIDR.String(),
		"cooldownSeconds": int(mesh.Cooldown.Seconds()), "nodes": nodes, "vips": vips, "released": released,
	})
}

// handleIPHistory lists which task held which address, and when.
func (s *Server) handleIPHistory(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip != "" {
		if _, err := netip.ParseAddr(ip); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "ip must be an IP address")
			return
		}
	}
	items, err := s.store.AddressHistory(r.Context(), ip, 200)
	if err != nil {
		s.internalError(w, "address history", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type dnsRecordView struct {
	Name string   `json:"name"`
	Kind string   `json:"kind"` // service | tasks | node
	IPs  []string `json:"ips"`
}

func dnsKind(name string) string {
	switch {
	case strings.HasPrefix(name, "tasks."):
		return "tasks"
	case strings.HasSuffix(name, ".node."+discovery.Zone):
		return "node"
	}
	return "service"
}

// handleDNSRecords lists the internal zone as served on every node (§8.1).
func (s *Server) handleDNSRecords(w http.ResponseWriter, r *http.Request) {
	items := []dnsRecordView{}
	if s.discovery != nil {
		for _, rec := range s.discovery.Records() {
			items = append(items, dnsRecordView{Name: rec.GetName(), Kind: dnsKind(rec.GetName()), IPs: nonNil(rec.GetIps())})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"zone": discovery.Zone, "items": items})
}

type dnsAnswer struct {
	Name   string   `json:"name"`
	Found  bool     `json:"found"`
	Source string   `json:"source"` // internal | upstream
	Kind   string   `json:"kind,omitempty"`
	IPs    []string `json:"ips"`
	Error  string   `json:"error,omitempty"`
}

// handleDNSLookup answers a name like a task would get it: internal names
// from the directory (short names are completed with the zone), others from
// the controller's resolver.
func (s *Server) handleDNSLookup(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name"))), ".")
	if name == "" || len(name) > 253 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name is required")
		return
	}
	internal := name
	if !strings.HasSuffix(internal, "."+discovery.Zone) && strings.Count(internal, ".") >= 2 && !strings.Contains(internal, "..") {
		if _, err := netip.ParseAddr(internal); err != nil {
			internal += "." + discovery.Zone // web.production.shop
		}
	}
	if strings.HasSuffix(internal, "."+discovery.Zone) && s.discovery != nil {
		for _, rec := range s.discovery.Records() {
			if rec.GetName() == internal {
				writeJSON(w, http.StatusOK, dnsAnswer{Name: internal, Found: true, Source: "internal", Kind: dnsKind(internal), IPs: nonNil(rec.GetIps())})
				return
			}
		}
		writeJSON(w, http.StatusOK, dnsAnswer{Name: internal, Source: "internal", IPs: []string{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, name)
	if err != nil {
		writeJSON(w, http.StatusOK, dnsAnswer{Name: name, Source: "upstream", IPs: []string{}, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, dnsAnswer{Name: name, Found: true, Source: "upstream", IPs: ips})
}
