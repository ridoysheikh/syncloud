package secgroup

import (
	"context"
	"encoding/json"

	"syncloud/internal/mesh"
	"syncloud/internal/store"
)

// Load builds the model from the database: every group, service, task and
// job run address, and the cluster's node addresses.
func Load(ctx context.Context, st *store.Store) (*Model, error) {
	m := &Model{Envs: map[string][]string{}}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	envNames := map[string][2]string{} // environment ID -> project, env
	for _, p := range projects {
		envs, err := st.ListEnvironments(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, e := range envs {
			m.Envs[p.Name] = append(m.Envs[p.Name], e.Name)
			envNames[e.ID] = [2]string{p.Name, e.Name}
		}
	}
	svcs, err := st.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	ips := map[string][]string{}
	tasks, err := st.ActiveTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.IP != "" {
			ips[t.ServiceID] = append(ips[t.ServiceID], t.IP)
		}
	}
	runs, err := st.ActiveRunAddresses(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if r.ServiceID != "" {
			ips[r.ServiceID] = append(ips[r.ServiceID], r.IP)
		} else if pe, ok := envNames[r.EnvironmentID]; ok {
			m.Standalone = append(m.Standalone, Standalone{Project: pe[0], Env: pe[1], IP: r.IP})
		}
	}
	for _, sv := range svcs {
		m.Services = append(m.Services, Service{ID: sv.ID, Project: sv.Project, Env: sv.Environment, Name: sv.Name, IPs: ips[sv.ID]})
	}
	// Managed databases (Phase 12) use their own access list instead of
	// their project's groups: a synthesized group attached only to them
	// lets their members talk to each other and lets the listed peers in.
	dbs, err := st.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}
	members, err := st.AllDatabaseMembers(ctx)
	if err != nil {
		return nil, err
	}
	for _, mb := range members {
		if mb.IP != "" {
			ips[mb.DatabaseID] = append(ips[mb.DatabaseID], mb.IP)
		}
	}
	var pgIPs []string
	for _, d := range dbs {
		m.Services = append(m.Services, Service{ID: d.ID, Project: d.Project, Env: d.Environment, Name: d.Name, IPs: ips[d.ID]})
		m.Groups = append(m.Groups, DatabaseGroup(d))
		if d.Engine == "postgres" {
			pgIPs = append(pgIPs, ips[d.ID]...)
		}
	}
	// The platform etcd (Phase 13): its members talk to each other, and
	// PostgreSQL members (Patroni) reach its client port.
	ems, err := st.EtcdMembers(ctx)
	if err != nil {
		return nil, err
	}
	if len(ems) > 0 {
		var eips []string
		for _, e := range ems {
			if e.IP != "" {
				eips = append(eips, e.IP)
			}
		}
		m.Services = append(m.Services, Service{ID: EtcdServiceID, Name: "etcd", IPs: eips})
		m.Groups = append(m.Groups, EtcdGroup(pgIPs))
	}
	groups, err := st.ListSecurityGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		m.Groups = append(m.Groups, FromStore(g))
	}
	nets, err := st.ListNodeNetworks(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nets {
		ip := mesh.MeshAddr(n.MeshIndex).String()
		m.ClusterIPs = append(m.ClusterIPs, ip)
		// Node hosts are platform infrastructure: Traefik on the controller
		// and edge nodes, health checks, and operators debugging from a node.
		m.PlatformIPs = append(m.PlatformIPs, ip)
	}
	return m, nil
}

// DatabaseGroup is the security group a database uses: its own members on
// any port, and its access list over TCP. Outbound is open (replication,
// Sentinel and DNS).
func DatabaseGroup(d store.Database) Group {
	in := []Rule{{Protocol: "any", Peers: []string{KindSelf}, Description: "members of the database"}}
	if acc := d.ParseNetwork().Access; len(acc) > 0 {
		in = append(in, Rule{Protocol: "tcp", Peers: acc, Description: "access list"})
	}
	return Group{ID: "dbsg_" + d.ID, Project: d.Project, Name: "database-" + d.Name, Inbound: in,
		Outbound: []Rule{{Protocol: "any", Peers: []string{KindAny}, Description: "all outbound traffic"}}, Services: []string{d.ID}}
}

// EtcdServiceID matches the platform etcd's containers (label
// syncloud.service_id).
const EtcdServiceID = "etcd"

// EtcdGroup is the platform etcd's group: its members on any port, and the
// PostgreSQL members on the client port.
func EtcdGroup(pgIPs []string) Group {
	in := []Rule{{Protocol: "any", Peers: []string{KindSelf}, Description: "etcd members"}}
	if len(pgIPs) > 0 {
		var peers []string
		for _, ip := range pgIPs {
			peers = append(peers, ip+"/32")
		}
		in = append(in, Rule{Protocol: "tcp", Ports: "2379", Peers: peers, Description: "PostgreSQL members (Patroni)"})
	}
	return Group{ID: "sg_etcd", Name: "platform-etcd", Inbound: in,
		Outbound: []Rule{{Protocol: "any", Peers: []string{KindAny}, Description: "all outbound traffic"}}, Services: []string{EtcdServiceID}}
}

// FromStore parses a stored group's rules.
func FromStore(g store.SecurityGroup) Group {
	out := Group{ID: g.ID, Project: g.Project, Name: g.Name, Default: g.Default, Services: g.ServiceIDs}
	_ = json.Unmarshal([]byte(g.Inbound), &out.Inbound)
	_ = json.Unmarshal([]byte(g.Outbound), &out.Outbound)
	return out
}
