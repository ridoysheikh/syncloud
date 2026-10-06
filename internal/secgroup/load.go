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

// FromStore parses a stored group's rules.
func FromStore(g store.SecurityGroup) Group {
	out := Group{ID: g.ID, Project: g.Project, Name: g.Name, Default: g.Default, Services: g.ServiceIDs}
	_ = json.Unmarshal([]byte(g.Inbound), &out.Inbound)
	_ = json.Unmarshal([]byte(g.Outbound), &out.Outbound)
	return out
}
