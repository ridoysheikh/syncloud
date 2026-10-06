package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"syncloud/internal/iam"
	"syncloud/internal/store"
)

// Authorization (§7): every authenticated route is an IAM action, named
// after its API operation ("service:ScaleService"), on a resource derived
// from its path ("srn:syncloud:project/shop/env/production/service/web").

// tagPrefix maps an OpenAPI tag to its action namespace.
var tagPrefix = map[string]string{
	"alerts": "alert", "auth": "auth", "autoscaling": "autoscaling", "backups": "backup", "builds": "build", "firewall": "firewall",
	"health": "health", "iam": "iam", "jobs": "job", "logs": "logs", "metrics": "metrics", "network": "network", "nodes": "node",
	"projects": "project", "registry": "registry", "services": "service", "settings": "settings", "system": "system", "tasks": "task",
	"traefik": "traefik", "traffic": "traffic", "quotas": "quota", "usage": "usage", "audit": "audit", "sts": "sts", "shell": "shell",
	"docs": "docs", "nodepools": "nodepool",
}

type opInfo struct{ id, tag string }

var (
	opsOnce sync.Once
	ops     map[string]opInfo // "METHOD /path" -> operation
)

func loadOps() {
	opsOnce.Do(func() {
		ops = map[string]opInfo{}
		var doc struct {
			Paths map[string]map[string]struct {
				OperationID string   `json:"operationId"`
				Tags        []string `json:"tags"`
			} `json:"paths"`
		}
		_ = json.Unmarshal(OpenAPISpec, &doc)
		for path, methods := range doc.Paths {
			for m, o := range methods {
				tag := ""
				if len(o.Tags) > 0 {
					tag = o.Tags[0]
				}
				ops[strings.ToUpper(m)+" "+path] = opInfo{o.OperationID, tag}
			}
		}
	})
}

// ActionFor returns the IAM action of a route ("" when it has none).
func ActionFor(method, path string) string {
	loadOps()
	o, ok := ops[method+" "+path]
	if !ok || o.id == "" {
		return ""
	}
	prefix := tagPrefix[o.tag]
	if prefix == "" {
		prefix = o.tag
	}
	return prefix + ":" + strings.ToUpper(o.id[:1]) + o.id[1:]
}

// Actions every signed-in principal may use on itself, whatever its policies.
var selfService = map[string]bool{
	"auth:Whoami": true, "auth:Logout": true, "system:Stream": true, "system:GetOpenAPI": true,
	"iam:ListAccessKeys": true, "iam:CreateAccessKey": true, "iam:DeleteAccessKey": true,
	"iam:ListTokens": true, "iam:CreateToken": true, "iam:DeleteToken": true,
	"iam:BeginMFA": true, "iam:EnableMFA": true, "iam:DisableMFA": true, "iam:ChangePassword": true,
	"iam:ListMyPermissions": true, "sts:AssumeRole": true, "auth:ApproveDevice": true, "docs:ListCommands": true,
	"shell:StartShell": true, "shell:GetShell": true, "shell:StopShell": true, "shell:ExecShell": true,
}

// listOps are global listings: allowed when some policy grants the action
// at all; the handler then keeps only the items the principal may read.
var listOps = map[string]bool{
	"service:ListAllServices": true, "task:ListTasks": true, "job:ListAllJobs": true, "build:ListBuilds": true,
	"build:ListGitSources": true, "network:ListAllSecurityGroups": true, "traefik:ListAllMiddlewares": true,
	"health:ListServiceHealth": true, "health:ListIncidents": true, "logs:QueryLogs": true, "logs:TailLogs": true,
	"traffic:GetTraffic": true, "traffic:GetTrafficMap": true, "project:ListProjects": true, "project:CreateProject": true,
	"registry:ListRepositories": true, "quota:ListQuotas": true, "usage:GetUsage": true,
	"usage:ExportUsage": true, "network:CheckReachability": true,
}

// Resource names.
func projectSRN(p string) string { return "srn:syncloud:project/" + p }
func envSRN(p, e string) string  { return projectSRN(p) + "/env/" + e }
func serviceSRN(p, e, s string) string {
	return envSRN(p, e) + "/service/" + s
}

var areaOf = regexp.MustCompile(`^/api/v1/([a-z-]+)`)

// resourceOf names the resource a request acts on.
func (s *Server) resourceOf(r *http.Request, action string) string {
	pv := r.PathValue
	if p := pv("project"); p != "" {
		res := projectSRN(p)
		if e := pv("env"); e != "" {
			res = envSRN(p, e)
			switch {
			case pv("service") != "":
				res += "/service/" + pv("service")
			case pv("job") != "":
				res += "/job/" + pv("job")
			}
		}
		switch {
		case pv("group") != "":
			res += "/security-group/" + pv("group")
		case pv("middleware") != "":
			res += "/middleware/" + pv("middleware")
		}
		return res
	}
	ctx := r.Context()
	path := r.URL.Path
	id := pv("id")
	switch {
	case strings.HasPrefix(path, "/api/v1/tasks/") && id != "":
		if t, err := s.store.TaskByID(ctx, id); err == nil {
			if sv, err := s.store.ServiceByID(ctx, t.ServiceID); err == nil {
				return serviceSRN(sv.Project, sv.Environment, sv.Name) + "/task/" + id
			}
		}
		return "srn:syncloud:task/" + id
	case strings.HasPrefix(path, "/api/v1/runs/") && id != "":
		if run, err := s.store.RunByID(ctx, id); err == nil {
			if run.ServiceID != "" {
				if sv, err := s.store.ServiceByID(ctx, run.ServiceID); err == nil {
					return serviceSRN(sv.Project, sv.Environment, sv.Name) + "/run/" + id
				}
			}
			if e, err := s.store.EnvironmentByID(ctx, run.EnvironmentID); err == nil {
				if p, perr := s.projectNameByID(ctx, e.ProjectID); perr == nil {
					return envSRN(p, e.Name) + "/run/" + id
				}
			}
		}
		return "srn:syncloud:run/" + id
	case strings.HasPrefix(path, "/api/v1/builds/") && id != "":
		if b, err := s.store.BuildByID(ctx, id); err == nil {
			if sv, err := s.store.ServiceByID(ctx, b.ServiceID); err == nil {
				return serviceSRN(sv.Project, sv.Environment, sv.Name) + "/build/" + id
			}
		}
		return "srn:syncloud:build/" + id
	case strings.HasPrefix(path, "/api/v1/nodes/") && id != "", strings.HasPrefix(path, "/api/v1/firewall/nodes/") && id != "":
		return "srn:syncloud:node/" + id
	case strings.HasPrefix(path, "/api/v1/registry/"):
		if repo := r.URL.Query().Get("repository"); repo != "" {
			return "srn:syncloud:registry/" + repo
		}
		return "srn:syncloud:registry/*"
	case strings.HasPrefix(path, "/api/v1/iam/users/") && id != "":
		return "srn:syncloud:user/" + id
	case strings.HasPrefix(path, "/api/v1/iam/access-keys"), strings.HasPrefix(path, "/api/v1/iam/tokens"), strings.HasPrefix(path, "/api/v1/iam/mfa"):
		if p, ok := principal(ctx); ok {
			if u := r.URL.Query().Get("userId"); u != "" {
				return "srn:syncloud:user/" + u
			}
			return "srn:syncloud:user/" + p.User.ID
		}
	}
	area := "cluster"
	if m := areaOf.FindStringSubmatch(path); m != nil {
		area = m[1]
	}
	if id != "" {
		return "srn:syncloud:" + area + "/" + id
	}
	return "srn:syncloud:" + area
}

func (s *Server) projectNameByID(ctx context.Context, id string) (string, error) {
	var name string
	err := s.store.R.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, id).Scan(&name)
	return name, err
}

// ── statements per principal ────────────────────────────────────────────────

type stmtCache struct {
	gen   atomic.Uint64
	mu    sync.Mutex
	at    uint64
	cache map[string][]iam.Attached // principal key -> statements
}

// iamChanged invalidates cached policies (any IAM change).
func (s *Server) iamChanged() { s.iamCache.gen.Add(1) }

// policyDocument resolves an attached policy name to its document.
func (s *Server) policyDocument(ctx context.Context, name string, customer map[string]store.IAMPolicy) (iam.Document, bool) {
	if strings.HasPrefix(name, "pol_") {
		p, ok := customer[name]
		if !ok {
			return iam.Document{}, false
		}
		d, err := iam.Parse([]byte(p.Document))
		return d, err == nil
	}
	return iam.ManagedDocument(name)
}

// statementsFor collects what applies to a principal: a role's policies for
// role sessions, otherwise the user's and their groups'.
func (s *Server) statementsFor(ctx context.Context, p Principal) ([]iam.Attached, error) {
	key := "user:" + p.User.ID
	if p.RoleID != "" {
		key = "role:" + p.RoleID
	}
	c := &s.iamCache
	gen := c.gen.Load()
	c.mu.Lock()
	if c.at != gen || c.cache == nil {
		c.cache, c.at = map[string][]iam.Attached{}, gen
	}
	if st, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return st, nil
	}
	c.mu.Unlock()

	atts, err := s.store.ListAttachments(ctx)
	if err != nil {
		return nil, err
	}
	pols, err := s.store.ListIAMPolicies(ctx)
	if err != nil {
		return nil, err
	}
	customer := map[string]store.IAMPolicy{}
	for _, x := range pols {
		customer[x.ID] = x
	}
	principals := map[string]bool{}
	if p.RoleID != "" {
		principals["role:"+p.RoleID] = true
	} else {
		principals["user:"+p.User.ID] = true
		u, err := s.store.IAMUser(ctx, p.User.ID)
		if err != nil {
			return nil, err
		}
		for _, g := range u.Groups {
			principals["group:"+g] = true
		}
	}
	var out []iam.Attached
	for _, a := range atts {
		if !principals[a.PrincipalType+":"+a.PrincipalID] {
			continue
		}
		d, ok := s.policyDocument(ctx, a.Policy, customer)
		if !ok {
			continue
		}
		name := a.Policy
		if cp, ok := customer[a.Policy]; ok {
			name = cp.Name
		}
		for i, st := range d.Statement {
			out = append(out, iam.Attached{Policy: name, Statement: st, Index: i})
		}
	}
	c.mu.Lock()
	if c.at == gen {
		c.cache[key] = out
	}
	c.mu.Unlock()
	return out, nil
}

func (p Principal) iamContext(r *http.Request) iam.Context {
	ct := p.CredType
	return iam.Context{MFA: p.MFA, SourceIP: clientIP(r), CredType: ct}
}

// isRoot is the root account using its own credentials (not a role).
func (p Principal) isRoot() bool { return p.User.IsRoot && p.RoleID == "" }

// decide evaluates one action on one resource for the request's principal.
func (s *Server) decide(r *http.Request, action, resource string) iam.Decision {
	p, ok := principal(r.Context())
	if !ok {
		return iam.Decision{Reason: "not signed in"}
	}
	if p.isRoot() {
		return iam.Decision{Allowed: true, Reason: "root account"}
	}
	st, err := s.statementsFor(r.Context(), p)
	if err != nil {
		s.log.Error("load policies", "err", err)
		return iam.Decision{Reason: "policies unavailable"}
	}
	return iam.Evaluate(st, action, resource, p.iamContext(r))
}

// can is decide for filtering lists.
func (s *Server) can(r *http.Request, action, resource string) bool {
	return s.decide(r, action, resource).Allowed
}

// canAny reports whether the principal may use action on some resource.
func (s *Server) canAny(r *http.Request, action string) bool {
	p, ok := principal(r.Context())
	if !ok {
		return false
	}
	if p.isRoot() {
		return true
	}
	st, err := s.statementsFor(r.Context(), p)
	if err != nil {
		return false
	}
	return iam.AllowsAny(st, action, p.iamContext(r))
}

// canReadProject reports whether any read of a project's resources is allowed
// (for events and listings that span many kinds of resource).
func (s *Server) canReadService(r *http.Request, project, env, service string) bool {
	return s.can(r, "service:GetService", serviceSRN(project, env, service))
}

type actionKey int

const authzActionKey actionKey = iota

// authorize checks the route's action before the handler runs.
func (s *Server) authorize(method, path string, next http.HandlerFunc) http.HandlerFunc {
	action := ActionFor(method, path)
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := principal(r.Context())
		if action == "" || selfService[action] {
			next(w, r)
			return
		}
		if s.requireMFAEnrollment(r, p) {
			writeError(w, http.StatusForbidden, CodeMFARequired, "multi-factor authentication is required: enable it under IAM › My security first")
			return
		}
		if listOps[action] {
			if !s.canAny(r, action) {
				s.denied(w, r, action, "srn:syncloud:*", "no policy allows "+action)
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), authzActionKey, action)))
			return
		}
		res := s.resourceOf(r, action)
		if d := s.decide(r, action, res); !d.Allowed {
			s.denied(w, r, action, res, d.Reason)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), authzActionKey, action)))
	}
}

func (s *Server) denied(w http.ResponseWriter, r *http.Request, action, resource, reason string) {
	p, _ := principal(r.Context())
	s.audit(r, p.User.ID, action, resource, map[string]any{"denied": true, "reason": reason})
	writeError(w, http.StatusForbidden, CodeForbidden, "not allowed: "+action+" on "+resource+" ("+reason+")")
}

// SettingRequireMFA makes MFA mandatory for every person (not service accounts).
const SettingRequireMFA = "iam.require_mfa"

// requireMFAEnrollment blocks people without MFA when it is mandatory; they
// can still set it up (self-service actions are not checked).
func (s *Server) requireMFAEnrollment(r *http.Request, p Principal) bool {
	return s.requireMFA.Load() && p.Kind != store.UserService && p.CredType == CredSession && !p.MFAEnabled
}

// Kinds of list item, for filterItems.
const (
	itemService      = "service"       // project, environment, name
	itemServiceField = "service-field" // project, environment, service (or serviceId)
	itemJob          = "job"           // project, environment, name
	itemProject      = "project"       // name
	itemSecGroup     = "security-group"
	itemMiddleware   = "middleware"
	itemRepo         = "repo"          // name
	itemProjectField = "project-field" // project (and environment)
)

// filterItems keeps the list items the principal may see with the list's
// action. Callers with the action everywhere get items back untouched.
func (s *Server) filterItems(r *http.Request, items any, kind string) any {
	action, _ := r.Context().Value(authzActionKey).(string)
	p, _ := principal(r.Context())
	if action == "" || p.isRoot() || s.can(r, action, "srn:syncloud:*") {
		return items
	}
	raw, _ := json.Marshal(items)
	var list []map[string]any
	if json.Unmarshal(raw, &list) != nil {
		return items
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	names := map[string]store.Service{}
	out := []map[string]any{}
	for _, m := range list {
		var res string
		switch kind {
		case itemService:
			res = serviceSRN(str(m, "project"), str(m, "environment"), str(m, "name"))
		case itemJob:
			res = envSRN(str(m, "project"), str(m, "environment")) + "/job/" + str(m, "name")
		case itemProject:
			res = projectSRN(str(m, "name"))
		case itemSecGroup:
			res = projectSRN(str(m, "project")) + "/security-group/" + str(m, "name")
		case itemMiddleware:
			res = projectSRN(str(m, "project")) + "/middleware/" + str(m, "name")
		case itemRepo:
			res = "srn:syncloud:registry/" + str(m, "name")
		case itemProjectField:
			res = projectSRN(str(m, "project"))
			if e := str(m, "environment"); e != "" {
				res = envSRN(str(m, "project"), e)
			}
		default:
			if pr, svc := str(m, "project"), str(m, "service"); pr != "" && svc != "" {
				res = serviceSRN(pr, str(m, "environment"), svc)
			} else if id := str(m, "serviceId"); id != "" {
				sv, ok := names[id]
				if !ok {
					sv, _ = s.store.ServiceByID(r.Context(), id)
					names[id] = sv
				}
				res = serviceSRN(sv.Project, sv.Environment, sv.Name)
			}
		}
		if res != "" && s.can(r, action, res) {
			out = append(out, m)
		}
	}
	return out
}

// requireEverywhere is for cluster-wide views that cannot be split by
// project (totals): the action must be allowed on every resource.
func (s *Server) requireEverywhere(w http.ResponseWriter, r *http.Request) bool {
	action, _ := r.Context().Value(authzActionKey).(string)
	p, _ := principal(r.Context())
	if action == "" || p.isRoot() || s.can(r, action, "srn:syncloud:*") {
		return true
	}
	s.denied(w, r, action, "srn:syncloud:*", "this view spans every project; use a project's or service's page")
	return false
}
