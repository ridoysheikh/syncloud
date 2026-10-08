package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/fwstats"
	"github.com/ridoysheikh/syncloud/internal/secgroup"
	"github.com/ridoysheikh/syncloud/internal/store"
)

type sgRuleView struct {
	secgroup.Rule
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

type sgView struct {
	ID          string       `json:"id"`
	Project     string       `json:"project"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Default     bool         `json:"default"`
	Inbound     []sgRuleView `json:"inbound"`
	Outbound    []sgRuleView `json:"outbound"`
	// Services are the explicitly attached services (env/name).
	Services []string `json:"services"`
	// Members are every service using the group (for the default group,
	// those with no other group), env/name.
	Members   []string  `json:"members"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type sgRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Inbound     []secgroup.Rule `json:"inbound"`
	Outbound    []secgroup.Rule `json:"outbound"`
	Services    []string        `json:"services"` // env/name
}

func (s *Server) requireSecurityGroups(w http.ResponseWriter) bool {
	if !s.securityGroups {
		writeError(w, http.StatusNotFound, CodeNotFound, "security groups are turned off (--security-groups=false)")
		return false
	}
	return true
}

func (s *Server) securityChanged() {
	if s.onSecurityChange != nil {
		s.onSecurityChange()
	}
}

func (s *Server) sgViews(ctx context.Context, m *secgroup.Model, project string) ([]sgView, error) {
	groups, err := s.store.ListSecurityGroups(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, sv := range m.Services {
		names[sv.ID] = sv.Env + "/" + sv.Name
	}
	var counters map[string]fwCounts
	if s.fwStats != nil {
		counters = map[string]fwCounts{}
		for k, c := range s.fwStats.Counters() {
			counters[k] = fwCounts{c.Packets, c.Bytes}
		}
	}
	out := []sgView{}
	for _, g := range groups {
		if project != "" && g.Project != project {
			continue
		}
		pg := secgroup.FromStore(g)
		v := sgView{ID: g.ID, Project: g.Project, Name: g.Name, Description: g.Description, Default: g.Default,
			Services: []string{}, Members: []string{}, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt}
		for i, r := range pg.Inbound {
			c := counters[g.ID+":in:"+strconv.Itoa(i)]
			v.Inbound = append(v.Inbound, sgRuleView{r, c.packets, c.bytes})
		}
		for i, r := range pg.Outbound {
			c := counters[g.ID+":out:"+strconv.Itoa(i)]
			v.Outbound = append(v.Outbound, sgRuleView{r, c.packets, c.bytes})
		}
		v.Inbound, v.Outbound = nonNil(v.Inbound), nonNil(v.Outbound)
		for _, id := range g.ServiceIDs {
			v.Services = append(v.Services, names[id])
		}
		for i := range m.Groups {
			if m.Groups[i].ID == g.ID {
				for _, sv := range m.Members(&m.Groups[i]) {
					v.Members = append(v.Members, sv.Env+"/"+sv.Name)
				}
			}
		}
		sort.Strings(v.Members)
		out = append(out, v)
	}
	return out, nil
}

type fwCounts struct{ packets, bytes uint64 }

func (s *Server) handleListAllSecurityGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecurityGroups(w) {
		return
	}
	m, err := secgroup.Load(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load security groups", err)
		return
	}
	items, err := s.sgViews(r.Context(), m, "")
	if err != nil {
		s.internalError(w, "list security groups", err)
		return
	}
	resp := map[string]any{"items": s.filterItems(r, items, itemSecGroup), "nodeErrors": map[string]string{}}
	if s.fwStats != nil {
		resp["nodeErrors"] = s.fwStats.SecurityErrors()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListSecurityGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecurityGroups(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	m, err := secgroup.Load(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load security groups", err)
		return
	}
	items, err := s.sgViews(r.Context(), m, p.Name)
	if err != nil {
		s.internalError(w, "list security groups", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetSecurityGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecurityGroups(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	m, err := secgroup.Load(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load security groups", err)
		return
	}
	items, err := s.sgViews(r.Context(), m, p.Name)
	if err != nil {
		s.internalError(w, "list security groups", err)
		return
	}
	for _, g := range items {
		if g.Name == r.PathValue("group") {
			writeJSON(w, http.StatusOK, g)
			return
		}
	}
	writeError(w, http.StatusNotFound, CodeNotFound, "no such security group")
}

// buildGroup validates a request into a stored group (not yet saved).
func (s *Server) buildGroup(ctx context.Context, p store.Project, req sgRequest, existing *store.SecurityGroup) (store.SecurityGroup, error) {
	req.Name = strings.TrimSpace(req.Name)
	if !policyNameRE.MatchString(req.Name) {
		return store.SecurityGroup{}, errors.New("name must be lowercase letters, digits and hyphens (at most 64)")
	}
	if existing != nil && existing.Default && req.Name != existing.Name {
		return store.SecurityGroup{}, errors.New("the default group cannot be renamed")
	}
	if len(req.Description) > 500 {
		return store.SecurityGroup{}, errors.New("description is too long")
	}
	exists := func(peer secgroup.Peer) bool { return s.peerExists(ctx, peer) }
	in, err := secgroup.Validate(p.Name, req.Inbound, exists)
	if err != nil {
		return store.SecurityGroup{}, errors.New("inbound " + err.Error())
	}
	out, err := secgroup.Validate(p.Name, req.Outbound, exists)
	if err != nil {
		return store.SecurityGroup{}, errors.New("outbound " + err.Error())
	}
	var ids []string
	seen := map[string]bool{}
	for _, ref := range req.Services {
		env, name, ok := strings.Cut(strings.TrimSpace(ref), "/")
		if !ok {
			return store.SecurityGroup{}, errors.New("services are written ENV/NAME, e.g. production/api")
		}
		e, err := s.store.EnvironmentByName(ctx, p.ID, env)
		if err != nil {
			return store.SecurityGroup{}, errors.New("no environment " + env + " in project " + p.Name)
		}
		sv, err := s.store.ServiceByName(ctx, e.ID, name)
		if err != nil {
			return store.SecurityGroup{}, errors.New("no service " + ref + " in project " + p.Name)
		}
		if !seen[sv.ID] {
			seen[sv.ID] = true
			ids = append(ids, sv.ID)
		}
	}
	inJSON, _ := json.Marshal(in)
	outJSON, _ := json.Marshal(out)
	now := s.now().UTC().Truncate(time.Second)
	g := store.SecurityGroup{ID: auth.NewID("sg_"), ProjectID: p.ID, Project: p.Name, Name: req.Name, Description: req.Description,
		Inbound: string(inJSON), Outbound: string(outJSON), ServiceIDs: ids, CreatedAt: now, UpdatedAt: now}
	if existing != nil {
		g.ID, g.Default, g.CreatedAt = existing.ID, existing.Default, existing.CreatedAt
	}
	return g, nil
}

func (s *Server) peerExists(ctx context.Context, p secgroup.Peer) bool {
	switch p.Kind {
	case secgroup.KindProject, secgroup.KindEnvironment, secgroup.KindService, secgroup.KindGroup:
	default:
		return true
	}
	pr, err := s.store.ProjectByName(ctx, p.Project)
	if err != nil {
		return false
	}
	switch p.Kind {
	case secgroup.KindGroup:
		_, err := s.store.SecurityGroupByName(ctx, pr.ID, p.Name)
		return err == nil
	case secgroup.KindEnvironment, secgroup.KindService:
		e, err := s.store.EnvironmentByName(ctx, pr.ID, p.Env)
		if err != nil {
			return false
		}
		if p.Kind == secgroup.KindService {
			_, err = s.store.ServiceByName(ctx, e.ID, p.Name)
		}
		return err == nil
	}
	return true
}

func (s *Server) handleCreateSecurityGroup(w http.ResponseWriter, r *http.Request) {
	s.saveSecurityGroup(w, r, "")
}

func (s *Server) handleUpdateSecurityGroup(w http.ResponseWriter, r *http.Request) {
	s.saveSecurityGroup(w, r, r.PathValue("group"))
}

func (s *Server) saveSecurityGroup(w http.ResponseWriter, r *http.Request, name string) {
	if !s.requireSecurityGroups(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req sgRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var existing *store.SecurityGroup
	status, action := http.StatusCreated, "firewall:CreateSecurityGroup"
	if name != "" {
		g, err := s.store.SecurityGroupByName(r.Context(), p.ID, name)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such security group")
			return
		} else if err != nil {
			s.internalError(w, "get security group", err)
			return
		}
		existing, status, action = &g, http.StatusOK, "firewall:EditSecurityGroup"
		if req.Name == "" {
			req.Name = g.Name
		}
	}
	g, err := s.buildGroup(r.Context(), p, req, existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	if err := s.store.PutSecurityGroup(r.Context(), g); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a security group named "+g.Name+" already exists in "+p.Name)
		return
	} else if err != nil {
		s.internalError(w, "save security group", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, action, "srn:syncloud:project/"+p.Name+"/security-group/"+g.Name, map[string]any{"services": req.Services})
	s.securityChanged()
	m, err := secgroup.Load(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load security groups", err)
		return
	}
	items, _ := s.sgViews(r.Context(), m, p.Name)
	for _, v := range items {
		if v.ID == g.ID {
			writeJSON(w, status, v)
			return
		}
	}
	w.WriteHeader(status)
}

func (s *Server) handleDeleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecurityGroups(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	g, err := s.store.SecurityGroupByName(r.Context(), p.ID, r.PathValue("group"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such security group")
		return
	} else if err != nil {
		s.internalError(w, "get security group", err)
		return
	}
	if g.Default {
		writeError(w, http.StatusConflict, CodeConflict, "the default group cannot be deleted; edit its rules instead")
		return
	}
	// Rules elsewhere that name this group would silently stop matching.
	m, err := secgroup.Load(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load security groups", err)
		return
	}
	ref := "group:" + p.Name + "/" + g.Name
	for _, o := range m.Groups {
		for _, rules := range [][]secgroup.Rule{o.Inbound, o.Outbound} {
			for _, rl := range rules {
				for _, peer := range rl.Peers {
					if peer == ref && o.ID != g.ID {
						writeError(w, http.StatusConflict, CodeConflict, "security group "+o.Project+"/"+o.Name+" has a rule for "+ref+"; remove it first")
						return
					}
				}
			}
		}
	}
	if err := s.store.DeleteSecurityGroup(r.Context(), g.ID); err != nil {
		s.internalError(w, "delete security group", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "firewall:DeleteSecurityGroup", "srn:syncloud:project/"+p.Name+"/security-group/"+g.Name, nil)
	s.securityChanged()
	w.WriteHeader(http.StatusNoContent)
}

type previewChange struct {
	Service string   `json:"service"` // env/name
	Lines   []string `json:"lines"`   // "+ …", "- …" or "  …"
	Tasks   int      `json:"tasks"`
	Nodes   []string `json:"nodes"`
}

// handlePreviewSecurityGroup shows what saving a group would change: each
// affected service's effective rules as a diff, and where its tasks run.
func (s *Server) handlePreviewSecurityGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecurityGroups(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		sgRequest
		Existing string `json:"existing"` // the group being edited ("" = new)
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	var existing *store.SecurityGroup
	if req.Existing != "" {
		g, err := s.store.SecurityGroupByName(r.Context(), p.ID, req.Existing)
		if err != nil {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such security group")
			return
		}
		existing = &g
	}
	g, err := s.buildGroup(r.Context(), p, req.sgRequest, existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	before, err := secgroup.Load(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load security groups", err)
		return
	}
	after := *before
	after.Groups = nil
	replaced := false
	for _, o := range before.Groups {
		if o.ID == g.ID {
			after.Groups = append(after.Groups, secgroup.FromStore(g))
			replaced = true
			continue
		}
		after.Groups = append(after.Groups, o)
	}
	if !replaced {
		after.Groups = append(after.Groups, secgroup.FromStore(g))
	}
	after.ResetIndex()
	tasks, _ := s.store.ActiveTasks(r.Context())
	nodeNames := map[string]string{}
	if ns, err := s.store.ListNodes(r.Context()); err == nil {
		for _, n := range ns {
			nodeNames[n.ID] = n.Name
		}
	}
	changes := []previewChange{}
	for _, sv := range before.Services {
		if sv.Project != p.Name {
			continue
		}
		b, a := before.EffectiveLines(sv.ID), after.EffectiveLines(sv.ID)
		if strings.Join(a, "\n") == strings.Join(b, "\n") {
			continue
		}
		c := previewChange{Service: sv.Env + "/" + sv.Name, Lines: lineDiff(b, a), Nodes: []string{}}
		nodes := map[string]bool{}
		for _, t := range tasks {
			if t.ServiceID == sv.ID && t.Desired == "running" {
				c.Tasks++
				if n := nodeNames[t.NodeID]; n != "" {
					nodes[n] = true
				}
			}
		}
		for n := range nodes {
			c.Nodes = append(c.Nodes, n)
		}
		sort.Strings(c.Nodes)
		changes = append(changes, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": changes})
}

// lineDiff marks lines only in b with "- " and only in a with "+ "; lines in
// both keep their place with "  ".
func lineDiff(b, a []string) []string {
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, l := range a {
		inA[l] = true
	}
	for _, l := range b {
		inB[l] = true
	}
	var out []string
	for _, l := range b {
		if inA[l] {
			out = append(out, "  "+l)
		} else {
			out = append(out, "- "+l)
		}
	}
	for _, l := range a {
		if !inB[l] {
			out = append(out, "+ "+l)
		}
	}
	return out
}

// endpointOf parses a reachability endpoint: "platform", an IPv4 address,
// or a service "project/env/name".
func (s *Server) endpointOf(ctx context.Context, ref string) (secgroup.Endpoint, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "platform" {
		return secgroup.Endpoint{Platform: true}, "the platform (Traefik)", nil
	}
	if a, err := netip.ParseAddr(ref); err == nil {
		name := ""
		if s.fwStats != nil {
			name = s.fwStats.Name(a.String())
		}
		return secgroup.Endpoint{IP: a.String()}, strings.TrimSpace(a.String() + " " + name), nil
	}
	parts := strings.Split(ref, "/")
	if len(parts) != 3 {
		return secgroup.Endpoint{}, "", errors.New(ref + ": use PROJECT/ENV/SERVICE, an IP address or platform")
	}
	pr, err := s.store.ProjectByName(ctx, parts[0])
	if err != nil {
		return secgroup.Endpoint{}, "", errors.New("no project " + parts[0])
	}
	e, err := s.store.EnvironmentByName(ctx, pr.ID, parts[1])
	if err != nil {
		return secgroup.Endpoint{}, "", errors.New("no environment " + parts[0] + "/" + parts[1])
	}
	sv, err := s.store.ServiceByName(ctx, e.ID, parts[2])
	if err != nil {
		return secgroup.Endpoint{}, "", errors.New("no service " + ref)
	}
	return secgroup.Endpoint{ServiceID: sv.ID}, ref, nil
}

// handleReachability answers "can A reach B on proto/port?" (§8.3), like
// AWS's Reachability Analyzer.
func (s *Server) handleReachability(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecurityGroups(w) {
		return
	}
	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Protocol string `json:"protocol"`
		Port     int    `json:"port"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Protocol = strings.ToLower(req.Protocol)
	if req.Protocol == "" {
		req.Protocol = "tcp"
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" && req.Protocol != "icmp" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "protocol must be tcp, udp or icmp")
		return
	}
	if req.Protocol != "icmp" && (req.Port < 1 || req.Port > 65535) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "port must be between 1 and 65535")
		return
	}
	src, srcName, err := s.endpointOf(r.Context(), req.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "from: "+err.Error())
		return
	}
	dst, dstName, err := s.endpointOf(r.Context(), req.To)
	if err != nil || dst.ServiceID == "" {
		msg := "to: the destination must be a service PROJECT/ENV/SERVICE"
		if err != nil {
			msg = "to: " + err.Error()
		}
		writeError(w, http.StatusBadRequest, CodeBadRequest, msg)
		return
	}
	m, err := secgroup.Load(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load security groups", err)
		return
	}
	v := m.Evaluate(src, dst, req.Protocol, req.Port)
	writeJSON(w, http.StatusOK, map[string]any{"from": srcName, "to": dstName, "protocol": req.Protocol, "port": req.Port, "verdict": v})
}

// handleServiceSecurity shows the groups and rules that apply to a service.
func (s *Server) handleServiceSecurity(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecurityGroups(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	m, err := secgroup.Load(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load security groups", err)
		return
	}
	items, err := s.sgViews(r.Context(), m, sv.Project)
	if err != nil {
		s.internalError(w, "list security groups", err)
		return
	}
	groups := []sgView{}
	for _, g := range m.GroupsOf(sv.ID) {
		for _, v := range items {
			if v.ID == g.ID {
				groups = append(groups, v)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "lines": m.EffectiveLines(sv.ID)})
}

// handleFirewallDrops is the drop log (§8.3): connection attempts a default
// deny dropped, newest first.
func (s *Server) handleFirewallDrops(w http.ResponseWriter, r *http.Request) {
	if s.fwStats == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "networking is not enabled")
		return
	}
	q := r.URL.Query()
	since := time.Hour
	if v := q.Get("since"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > 30*24*time.Hour {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "since must be a duration like 15m or 24h (at most 30 days)")
			return
		}
		since = d
	}
	f := fwstats.DropFilter{Node: q.Get("node"), Direction: q.Get("direction"), IP: q.Get("ip"), Since: s.now().Add(-since)}
	limit := 500
	items, err := s.fwStats.DropsFromLogs(r.Context(), f, since, limit)
	source := "logs"
	if err != nil { // VictoriaLogs unavailable: what this controller saw since it started
		items, source = s.fwStats.Drops(f, limit), "memory"
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "source": source})
}

type counterView struct {
	ID      string `json:"id"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// handleFirewallCounters returns every rule's hit counters summed over the
// nodes (since each agent started), or one node's with ?node=.
func (s *Server) handleFirewallCounters(w http.ResponseWriter, r *http.Request) {
	if s.fwStats == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "networking is not enabled")
		return
	}
	c := s.fwStats.Counters()
	if n := r.URL.Query().Get("node"); n != "" {
		c = s.fwStats.NodeCounters(n)
	}
	items := []counterView{}
	for k, v := range c {
		items = append(items, counterView{k, v.Packets, v.Bytes})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
