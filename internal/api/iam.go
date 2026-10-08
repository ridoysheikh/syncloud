package api

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/iam"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// IAM administration (§7): users and service accounts, groups, policies and
// their attachments, roles, MFA, STS, the policy simulator and the audit log.

var iamNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_+=,.@-]{0,63}$`)

type iamUserView struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	IsRoot      bool       `json:"isRoot"`
	Disabled    bool       `json:"disabled"`
	MFAEnabled  bool       `json:"mfaEnabled"`
	Groups      []string   `json:"groups"`   // group names
	Policies    []string   `json:"policies"` // attached directly
	LastLoginAt *time.Time `json:"lastLoginAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// iamIndex holds names for views.
type iamIndex struct {
	groups   map[string]string // id -> name
	policies map[string]string // id -> name (customer)
	atts     []store.Attachment
}

func (s *Server) iamIndex(ctx context.Context) (iamIndex, error) {
	x := iamIndex{groups: map[string]string{}, policies: map[string]string{}}
	gs, err := s.store.ListIAMGroups(ctx)
	if err != nil {
		return x, err
	}
	for _, g := range gs {
		x.groups[g.ID] = g.Name
	}
	ps, err := s.store.ListIAMPolicies(ctx)
	if err != nil {
		return x, err
	}
	for _, p := range ps {
		x.policies[p.ID] = p.Name
	}
	x.atts, err = s.store.ListAttachments(ctx)
	return x, err
}

func (x iamIndex) policyName(p string) string {
	if n, ok := x.policies[p]; ok {
		return n
	}
	return p
}

func (x iamIndex) attached(typ, id string) []string {
	out := []string{}
	for _, a := range x.atts {
		if a.PrincipalType == typ && a.PrincipalID == id {
			out = append(out, x.policyName(a.Policy))
		}
	}
	return out
}

func (x iamIndex) userView(u store.IAMUser) iamUserView {
	v := iamUserView{ID: u.ID, Email: u.Email, Name: u.Name, Kind: u.Kind, IsRoot: u.IsRoot, Disabled: u.Disabled, MFAEnabled: u.MFAEnabled,
		Groups: []string{}, Policies: x.attached("user", u.ID), LastLoginAt: utcPtr(u.LastLoginAt), CreatedAt: u.CreatedAt}
	for _, g := range u.Groups {
		v.Groups = append(v.Groups, x.groups[g])
	}
	return v
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.store.ListIAMUsers(r.Context())
	if err != nil {
		s.internalError(w, "list users", err)
		return
	}
	x, err := s.iamIndex(r.Context())
	if err != nil {
		s.internalError(w, "iam index", err)
		return
	}
	out := []iamUserView{}
	for _, u := range us {
		out = append(out, x.userView(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type userRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // user (default) | service
	Password string `json:"password"`
	Disabled bool   `json:"disabled"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req userRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Email, req.Name = strings.TrimSpace(req.Email), strings.TrimSpace(req.Name)
	if req.Kind == "" {
		req.Kind = store.UserHuman
	}
	if req.Kind != store.UserHuman && req.Kind != store.UserService {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "kind must be user or service")
		return
	}
	if req.Kind == store.UserService {
		// Service accounts have no mailbox; give them a stable unique name.
		if !iamNameRE.MatchString(req.Name) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "a service account name is letters, digits and _+=,.@- (at most 64)")
			return
		}
		req.Email = req.Name + "@service.syncloud.internal"
	} else if a, err := mail.ParseAddress(req.Email); err != nil || a.Address != req.Email {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "email is not valid")
		return
	}
	if req.Name == "" || len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name must be 1–100 characters")
		return
	}
	hash := ""
	if req.Kind == store.UserHuman {
		if len(req.Password) < 12 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "password must be at least 12 characters")
			return
		}
		var err error
		if hash, err = auth.HashPassword(req.Password); err != nil {
			s.internalError(w, "hash password", err)
			return
		}
	} else {
		hash = "!" // never matches: no console login
	}
	u := store.IAMUser{User: store.User{ID: auth.NewID("usr_"), Email: req.Email, Name: req.Name, PasswordHash: hash, CreatedAt: s.now().Truncate(time.Second)}, Kind: req.Kind}
	if err := s.store.CreateUser(r.Context(), u); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a user with this email or name already exists")
		return
	} else if err != nil {
		s.internalError(w, "create user", err)
		return
	}
	p, _ := currentUser(r.Context())
	s.audit(r, p.ID, "iam:CreateUser", "srn:syncloud:user/"+u.ID, map[string]any{"email": u.Email, "kind": u.Kind})
	full, _ := s.store.IAMUser(r.Context(), u.ID)
	x, _ := s.iamIndex(r.Context())
	writeJSON(w, http.StatusCreated, x.userView(full))
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.store.IAMUser(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such user")
		return
	} else if err != nil {
		s.internalError(w, "get user", err)
		return
	}
	var req userRequest
	req.Name, req.Disabled = u.Name, u.Disabled
	if !decodeJSON(w, r, &req) {
		return
	}
	if u.IsRoot {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the root account is changed from its own security page")
		return
	}
	hash := ""
	if req.Password != "" {
		if u.Kind == store.UserService {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "service accounts have no password")
			return
		}
		if len(req.Password) < 12 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "password must be at least 12 characters")
			return
		}
		if hash, err = auth.HashPassword(req.Password); err != nil {
			s.internalError(w, "hash password", err)
			return
		}
	}
	if err := s.store.UpdateUser(r.Context(), id, strings.TrimSpace(req.Name), req.Disabled, hash); err != nil {
		s.internalError(w, "update user", err)
		return
	}
	p, _ := currentUser(r.Context())
	s.audit(r, p.ID, "iam:UpdateUser", "srn:syncloud:user/"+id, map[string]any{"disabled": req.Disabled, "passwordReset": hash != ""})
	full, _ := s.store.IAMUser(r.Context(), id)
	x, _ := s.iamIndex(r.Context())
	writeJSON(w, http.StatusOK, x.userView(full))
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _ := currentUser(r.Context())
	if id == p.ID {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "you cannot delete yourself")
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such user (the root account cannot be deleted)")
		return
	} else if err != nil {
		s.internalError(w, "delete user", err)
		return
	}
	s.iamChanged()
	s.audit(r, p.ID, "iam:DeleteUser", "srn:syncloud:user/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── groups ──────────────────────────────────────────────────────────────────

type groupView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Members     []string  `json:"members"` // user IDs
	Policies    []string  `json:"policies"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.store.ListIAMGroups(r.Context())
	if err != nil {
		s.internalError(w, "list groups", err)
		return
	}
	x, _ := s.iamIndex(r.Context())
	out := []groupView{}
	for _, g := range gs {
		out = append(out, groupView{ID: g.ID, Name: g.Name, Description: g.Description, Members: nonNil(g.Members), Policies: x.attached("group", g.ID), CreatedAt: g.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) { s.saveGroup(w, r, "") }
func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	s.saveGroup(w, r, r.PathValue("id"))
}

func (s *Server) saveGroup(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Members     []string `json:"members"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !iamNameRE.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name is letters, digits and _+=,.@- (at most 64)")
		return
	}
	for _, m := range req.Members {
		if _, err := s.store.UserByID(r.Context(), m); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "no user "+m)
			return
		}
	}
	g := store.IAMGroup{ID: id, Name: req.Name, Description: req.Description, Members: req.Members, CreatedAt: s.now().Truncate(time.Second)}
	status, action := http.StatusOK, "iam:UpdateGroup"
	if id == "" {
		g.ID, status, action = auth.NewID("grp_"), http.StatusCreated, "iam:CreateGroup"
	} else {
		found := false
		gs, _ := s.store.ListIAMGroups(r.Context())
		for _, x := range gs {
			if x.ID == id {
				found, g.CreatedAt = true, x.CreatedAt
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such group")
			return
		}
	}
	if err := s.store.PutIAMGroup(r.Context(), g); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a group named "+g.Name+" already exists")
		return
	} else if err != nil {
		s.internalError(w, "save group", err)
		return
	}
	s.iamChanged()
	p, _ := currentUser(r.Context())
	s.audit(r, p.ID, action, "srn:syncloud:group/"+g.ID, map[string]any{"name": g.Name, "members": len(g.Members)})
	x, _ := s.iamIndex(r.Context())
	writeJSON(w, status, groupView{ID: g.ID, Name: g.Name, Description: g.Description, Members: nonNil(g.Members), Policies: x.attached("group", g.ID), CreatedAt: g.CreatedAt})
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteIAMGroup(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such group")
		return
	} else if err != nil {
		s.internalError(w, "delete group", err)
		return
	}
	s.iamChanged()
	p, _ := currentUser(r.Context())
	s.audit(r, p.ID, "iam:DeleteGroup", "srn:syncloud:group/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── policies ────────────────────────────────────────────────────────────────

type policyView struct {
	ID          string          `json:"id"`   // pol_… or the managed name
	Name        string          `json:"name"` // managed: ReadOnly, Developer
	Description string          `json:"description"`
	Managed     bool            `json:"managed"`
	PerProject  bool            `json:"perProject"` // a template attached as Name:project
	Document    json.RawMessage `json:"document,omitempty"`
	AttachedTo  int             `json:"attachedTo"`
	UpdatedAt   *time.Time      `json:"updatedAt,omitempty"`
}

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	x, err := s.iamIndex(r.Context())
	if err != nil {
		s.internalError(w, "iam index", err)
		return
	}
	count := map[string]int{}
	for _, a := range x.atts {
		base, _, _ := strings.Cut(a.Policy, ":")
		count[base]++
	}
	out := []policyView{}
	for _, m := range iam.ManagedPolicies() {
		doc, _ := iam.ManagedDocument(m.Name + map[bool]string{true: ":PROJECT", false: ""}[m.PerProject])
		b, _ := json.Marshal(doc)
		out = append(out, policyView{ID: m.Name, Name: m.Name, Description: m.Description, Managed: true, PerProject: m.PerProject, Document: b, AttachedTo: count[m.Name]})
	}
	ps, err := s.store.ListIAMPolicies(r.Context())
	if err != nil {
		s.internalError(w, "list policies", err)
		return
	}
	for _, p := range ps {
		t := p.UpdatedAt
		out = append(out, policyView{ID: p.ID, Name: p.Name, Description: p.Description, Document: json.RawMessage(p.Document), AttachedTo: count[p.ID], UpdatedAt: &t})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	s.savePolicyDoc(w, r, "")
}
func (s *Server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	s.savePolicyDoc(w, r, r.PathValue("id"))
}

func (s *Server) savePolicyDoc(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Document    json.RawMessage `json:"document"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !iamNameRE.MatchString(req.Name) || strings.Contains(req.Name, ":") {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name is letters, digits and _+=,.@- (at most 64)")
		return
	}
	for _, m := range iam.ManagedPolicies() {
		if strings.EqualFold(m.Name, req.Name) {
			writeError(w, http.StatusConflict, CodeConflict, req.Name+" is a managed policy name")
			return
		}
	}
	if _, err := iam.Parse(req.Document); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	now := s.now().Truncate(time.Second)
	p := store.IAMPolicy{ID: id, Name: req.Name, Description: req.Description, Document: string(req.Document), CreatedAt: now, UpdatedAt: now}
	status, action := http.StatusOK, "iam:UpdatePolicy"
	if id == "" {
		p.ID, status, action = auth.NewID("pol_"), http.StatusCreated, "iam:CreatePolicy"
	} else {
		found := false
		ps, _ := s.store.ListIAMPolicies(r.Context())
		for _, x := range ps {
			if x.ID == id {
				found, p.CreatedAt = true, x.CreatedAt
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such policy (managed policies cannot be edited)")
			return
		}
	}
	if err := s.store.PutIAMPolicy(r.Context(), p); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a policy named "+p.Name+" already exists")
		return
	} else if err != nil {
		s.internalError(w, "save policy", err)
		return
	}
	s.iamChanged()
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, action, "srn:syncloud:policy/"+p.ID, map[string]any{"name": p.Name})
	writeJSON(w, status, policyView{ID: p.ID, Name: p.Name, Description: p.Description, Document: req.Document, UpdatedAt: &now})
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteIAMPolicy(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such policy (managed policies cannot be deleted)")
		return
	} else if err != nil {
		s.internalError(w, "delete policy", err)
		return
	}
	s.iamChanged()
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "iam:DeletePolicy", "srn:syncloud:policy/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── attachments ─────────────────────────────────────────────────────────────

type attachRequest struct {
	PrincipalType string `json:"principalType"` // user | group | role
	PrincipalID   string `json:"principalId"`
	Policy        string `json:"policy"` // pol_… or a managed name; templates as Developer:shop
}

func (s *Server) validAttachment(ctx context.Context, a attachRequest) error {
	switch a.PrincipalType {
	case "user":
		if _, err := s.store.UserByID(ctx, a.PrincipalID); err != nil {
			return errors.New("no such user")
		}
	case "group":
		gs, _ := s.store.ListIAMGroups(ctx)
		if !slicesContainsFunc(gs, func(g store.IAMGroup) bool { return g.ID == a.PrincipalID }) {
			return errors.New("no such group")
		}
	case "role":
		rs, _ := s.store.ListIAMRoles(ctx)
		if !slicesContainsFunc(rs, func(x store.IAMRole) bool { return x.ID == a.PrincipalID }) {
			return errors.New("no such role")
		}
	default:
		return errors.New("principalType must be user, group or role")
	}
	if strings.HasPrefix(a.Policy, "pol_") {
		ps, _ := s.store.ListIAMPolicies(ctx)
		if !slicesContainsFunc(ps, func(p store.IAMPolicy) bool { return p.ID == a.Policy }) {
			return errors.New("no such policy")
		}
		return nil
	}
	if _, ok := iam.ManagedDocument(a.Policy); !ok {
		return errors.New("unknown policy " + a.Policy + " (project templates are attached as Name:project, e.g. Developer:shop)")
	}
	if _, project, scoped := strings.Cut(a.Policy, ":"); scoped {
		if _, err := s.store.ProjectByName(ctx, project); err != nil {
			return errors.New("no project " + project)
		}
	}
	return nil
}

func slicesContainsFunc[T any](xs []T, f func(T) bool) bool {
	for _, x := range xs {
		if f(x) {
			return true
		}
	}
	return false
}

func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	atts, err := s.store.ListAttachments(r.Context())
	if err != nil {
		s.internalError(w, "list attachments", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(atts)})
}

func (s *Server) handleAttachPolicy(w http.ResponseWriter, r *http.Request) {
	var req attachRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.validAttachment(r.Context(), req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	if err := s.store.Attach(r.Context(), store.Attachment(req), s.now()); err != nil {
		s.internalError(w, "attach", err)
		return
	}
	s.iamChanged()
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "iam:AttachPolicy", "srn:syncloud:"+req.PrincipalType+"/"+req.PrincipalID, map[string]any{"policy": req.Policy})
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) handleDetachPolicy(w http.ResponseWriter, r *http.Request) {
	var req attachRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.store.Detach(r.Context(), store.Attachment(req)); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such attachment")
		return
	} else if err != nil {
		s.internalError(w, "detach", err)
		return
	}
	s.iamChanged()
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "iam:DetachPolicy", "srn:syncloud:"+req.PrincipalType+"/"+req.PrincipalID, map[string]any{"policy": req.Policy})
	w.WriteHeader(http.StatusNoContent)
}

// ── roles and STS ───────────────────────────────────────────────────────────

type roleView struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Trust             store.RoleTrust `json:"trust"`
	MaxSessionSeconds int             `json:"maxSessionSeconds"`
	Policies          []string        `json:"policies"`
	CreatedAt         time.Time       `json:"createdAt"`
}

func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	rs, err := s.store.ListIAMRoles(r.Context())
	if err != nil {
		s.internalError(w, "list roles", err)
		return
	}
	x, _ := s.iamIndex(r.Context())
	out := []roleView{}
	for _, ro := range rs {
		out = append(out, roleView{ID: ro.ID, Name: ro.Name, Description: ro.Description, Trust: trustNonNil(ro.Trust), MaxSessionSeconds: ro.MaxSessionSeconds,
			Policies: x.attached("role", ro.ID), CreatedAt: ro.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func trustNonNil(t store.RoleTrust) store.RoleTrust {
	t.Users, t.Groups = nonNil(t.Users), nonNil(t.Groups)
	return t
}

func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) { s.saveRole(w, r, "") }
func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	s.saveRole(w, r, r.PathValue("id"))
}

func (s *Server) saveRole(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Name              string          `json:"name"`
		Description       string          `json:"description"`
		Trust             store.RoleTrust `json:"trust"`
		MaxSessionSeconds int             `json:"maxSessionSeconds"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !iamNameRE.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name is letters, digits and _+=,.@- (at most 64)")
		return
	}
	if req.MaxSessionSeconds == 0 {
		req.MaxSessionSeconds = 3600
	}
	if req.MaxSessionSeconds < 900 || req.MaxSessionSeconds > 43200 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "maxSessionSeconds must be between 900 (15 min) and 43200 (12 h)")
		return
	}
	for _, u := range req.Trust.Users {
		if _, err := s.store.UserByID(r.Context(), u); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "trust: no user "+u)
			return
		}
	}
	gs, _ := s.store.ListIAMGroups(r.Context())
	for _, g := range req.Trust.Groups {
		if !slicesContainsFunc(gs, func(x store.IAMGroup) bool { return x.ID == g }) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "trust: no group "+g)
			return
		}
	}
	ro := store.IAMRole{ID: id, Name: req.Name, Description: req.Description, Trust: req.Trust, MaxSessionSeconds: req.MaxSessionSeconds, CreatedAt: s.now().Truncate(time.Second)}
	status, action := http.StatusOK, "iam:UpdateRole"
	if id == "" {
		ro.ID, status, action = auth.NewID("role_"), http.StatusCreated, "iam:CreateRole"
	} else {
		rs, _ := s.store.ListIAMRoles(r.Context())
		found := false
		for _, x := range rs {
			if x.ID == id {
				found, ro.CreatedAt = true, x.CreatedAt
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such role")
			return
		}
	}
	if err := s.store.PutIAMRole(r.Context(), ro); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a role named "+ro.Name+" already exists")
		return
	} else if err != nil {
		s.internalError(w, "save role", err)
		return
	}
	s.iamChanged()
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, action, "srn:syncloud:role/"+ro.ID, map[string]any{"name": ro.Name})
	x, _ := s.iamIndex(r.Context())
	writeJSON(w, status, roleView{ID: ro.ID, Name: ro.Name, Description: ro.Description, Trust: trustNonNil(ro.Trust), MaxSessionSeconds: ro.MaxSessionSeconds,
		Policies: x.attached("role", ro.ID), CreatedAt: ro.CreatedAt})
}

func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteIAMRole(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such role")
		return
	} else if err != nil {
		s.internalError(w, "delete role", err)
		return
	}
	s.iamChanged()
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "iam:DeleteRole", "srn:syncloud:role/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

type tempCredentials struct {
	AccessKeyID     string    `json:"accessKeyId"`
	SecretAccessKey string    `json:"secretAccessKey"`
	SessionToken    string    `json:"sessionToken"`
	Expiration      time.Time `json:"expiration"`
	Role            string    `json:"role,omitempty"`
}

// issueTemp creates temporary credentials for a user (optionally as a role).
func (s *Server) issueTemp(ctx context.Context, kind, userID, roleID string, mfa bool, ip string, ttl time.Duration) (tempCredentials, error) {
	id := auth.TempKeyPrefix + strings.ToUpper(auth.NewID(""))
	_, secret := auth.NewAccessKey()
	token := auth.NewToken("syn_sts_")
	now := s.now().Truncate(time.Second)
	c := store.TempCredential{ID: id, Kind: kind, UserID: userID, RoleID: roleID, SecretEnc: s.secrets.Seal([]byte(secret), []byte(id)),
		TokenHash: auth.HashToken(token), MFA: mfa, SourceIP: ip, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := s.store.CreateTempCredential(ctx, c); err != nil {
		return tempCredentials{}, err
	}
	return tempCredentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: token, Expiration: c.ExpiresAt}, nil
}

// handleAssumeRole issues role session credentials (§7.1, sts:AssumeRole).
// The caller must be trusted by the role, with MFA when the role asks.
func (s *Server) handleAssumeRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role            string `json:"role"` // name or ID
		DurationSeconds int    `json:"durationSeconds"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, _ := principal(r.Context())
	if p.RoleID != "" {
		writeError(w, http.StatusForbidden, CodeForbidden, "role sessions cannot assume roles")
		return
	}
	rs, err := s.store.ListIAMRoles(r.Context())
	if err != nil {
		s.internalError(w, "list roles", err)
		return
	}
	var role *store.IAMRole
	for i := range rs {
		if rs[i].ID == req.Role || rs[i].Name == req.Role {
			role = &rs[i]
		}
	}
	if role == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such role")
		return
	}
	u, err := s.store.IAMUser(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, "get user", err)
		return
	}
	trusted := p.isRoot() || slicesContainsFunc(role.Trust.Users, func(x string) bool { return x == u.ID })
	for _, g := range u.Groups {
		trusted = trusted || slicesContainsFunc(role.Trust.Groups, func(x string) bool { return x == g })
	}
	if !trusted {
		s.denied(w, r, "sts:AssumeRole", "srn:syncloud:role/"+role.ID, "the role does not trust you")
		return
	}
	if role.Trust.RequireMFA && !p.MFA {
		s.denied(w, r, "sts:AssumeRole", "srn:syncloud:role/"+role.ID, "the role requires MFA: sign in with an authenticator code")
		return
	}
	d := req.DurationSeconds
	if d == 0 {
		d = 3600
	}
	if d < 900 || d > role.MaxSessionSeconds {
		writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("durationSeconds must be between 900 and %d", role.MaxSessionSeconds))
		return
	}
	creds, err := s.issueTemp(r.Context(), store.TempRole, u.ID, role.ID, p.MFA, clientIP(r), time.Duration(d)*time.Second)
	if err != nil {
		s.internalError(w, "issue credentials", err)
		return
	}
	creds.Role = role.Name
	s.audit(r, u.ID, "sts:AssumeRole", "srn:syncloud:role/"+role.ID, map[string]any{"credential": creds.AccessKeyID, "seconds": d})
	writeJSON(w, http.StatusOK, creds)
}

// ── MFA ─────────────────────────────────────────────────────────────────────

// handleBeginMFA creates a new (not yet active) TOTP secret for the caller.
func (s *Server) handleBeginMFA(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	if p.CredType != CredSession {
		writeError(w, http.StatusForbidden, CodeForbidden, "set up MFA from the dashboard")
		return
	}
	u, err := s.store.IAMUser(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, "get user", err)
		return
	}
	if u.MFAEnabled {
		writeError(w, http.StatusConflict, CodeConflict, "MFA is already enabled; disable it first to enroll a new device")
		return
	}
	secret := auth.NewTOTPSecret()
	if err := s.store.SetMFA(r.Context(), u.ID, s.secrets.Seal([]byte(secret), []byte("mfa:"+u.ID)), false); err != nil {
		s.internalError(w, "store MFA secret", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "uri": auth.TOTPURI(secret, "SynCloud", u.Email)})
}

func (s *Server) checkOTP(r *http.Request, u store.IAMUser, code string) bool {
	if len(u.MFASecret) == 0 {
		return false
	}
	secret, err := s.secrets.Open(u.MFASecret, []byte("mfa:"+u.ID))
	return err == nil && auth.VerifyTOTP(string(secret), code, s.now())
}

// handleEnableMFA turns MFA on once a code from the new secret checks out.
func (s *Server) handleEnableMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, _ := principal(r.Context())
	u, err := s.store.IAMUser(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, "get user", err)
		return
	}
	if !s.checkOTP(r, u, req.Code) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the code does not match; check your device's clock and try the next one")
		return
	}
	if err := s.store.SetMFA(r.Context(), u.ID, u.MFASecret, true); err != nil {
		s.internalError(w, "enable MFA", err)
		return
	}
	s.audit(r, u.ID, "iam:EnableMFA", "srn:syncloud:user/"+u.ID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"mfaEnabled": true})
}

// handleDisableMFA needs a current code (or an administrator, for another user).
func (s *Server) handleDisableMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	target, ok := s.credentialOwner(w, r)
	if !ok {
		return
	}
	p, _ := principal(r.Context())
	u, err := s.store.IAMUser(r.Context(), target)
	if err != nil {
		s.internalError(w, "get user", err)
		return
	}
	if target == p.User.ID && u.MFAEnabled && !s.checkOTP(r, u, req.Code) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "enter a current code to turn MFA off")
		return
	}
	if err := s.store.SetMFA(r.Context(), u.ID, nil, false); err != nil {
		s.internalError(w, "disable MFA", err)
		return
	}
	s.audit(r, p.User.ID, "iam:DisableMFA", "srn:syncloud:user/"+u.ID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"mfaEnabled": false})
}

// handleChangePassword lets anyone change their own password.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, _ := principal(r.Context())
	u, err := s.store.UserByID(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, "get user", err)
		return
	}
	if ok, _ := auth.VerifyPassword(req.Current, u.PasswordHash); !ok {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the current password is incorrect")
		return
	}
	if len(req.New) < 12 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "the new password must be at least 12 characters")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		s.internalError(w, "hash password", err)
		return
	}
	if err := s.store.SetPassword(r.Context(), u.ID, hash); err != nil {
		s.internalError(w, "set password", err)
		return
	}
	s.audit(r, u.ID, "iam:ChangePassword", "srn:syncloud:user/"+u.ID, nil)
	_ = s.startSession(w, r, u, p.MFA) // other sessions ended; keep this one
	w.WriteHeader(http.StatusNoContent)
}

// ── settings, simulator, actions ────────────────────────────────────────────

func (s *Server) handleGetIAMSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"requireMfa": s.requireMFA.Load()})
}

func (s *Server) handlePutIAMSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequireMFA bool `json:"requireMfa"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, _ := principal(r.Context())
	if req.RequireMFA && !p.MFA && !p.isRoot() {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "sign in with MFA before making it mandatory")
		return
	}
	v := "0"
	if req.RequireMFA {
		v = "1"
	}
	if err := s.store.SetSetting(r.Context(), SettingRequireMFA, v); err != nil {
		s.internalError(w, "save setting", err)
		return
	}
	s.requireMFA.Store(req.RequireMFA)
	s.audit(r, p.User.ID, "iam:PutIAMSettings", "srn:syncloud:iam", map[string]any{"requireMfa": req.RequireMFA})
	writeJSON(w, http.StatusOK, req)
}

// handleSimulate answers "can principal X do action Y on resource Z?" (§7).
func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Principal string `json:"principal"` // user ID or email, or role:<name|id>
		Action    string `json:"action"`
		Resource  string `json:"resource"`
		MFA       bool   `json:"mfa"`
		SourceIP  string `json:"sourceIp"`
		CredType  string `json:"credentialType"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Action == "" || req.Resource == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "action and resource are required")
		return
	}
	var p Principal
	if name, ok := strings.CutPrefix(req.Principal, "role:"); ok {
		rs, _ := s.store.ListIAMRoles(r.Context())
		for _, ro := range rs {
			if ro.ID == name || ro.Name == name {
				p.RoleID = ro.ID
			}
		}
		if p.RoleID == "" {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such role")
			return
		}
	} else {
		u, err := s.store.UserByID(r.Context(), req.Principal)
		if errors.Is(err, store.ErrNotFound) {
			u, err = s.store.UserByEmail(r.Context(), req.Principal)
		}
		if err != nil {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such user")
			return
		}
		p.User = u
	}
	if req.CredType == "" {
		req.CredType = CredSession
	}
	var d iam.Decision
	if p.isRoot() {
		d = iam.Decision{Allowed: true, Reason: "root account"}
	} else {
		st, err := s.statementsFor(r.Context(), p)
		if err != nil {
			s.internalError(w, "load policies", err)
			return
		}
		d = iam.Evaluate(st, req.Action, req.Resource, iam.Context{MFA: req.MFA, SourceIP: req.SourceIP, CredType: req.CredType})
	}
	writeJSON(w, http.StatusOK, d)
}

type actionView struct {
	Action string `json:"action"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Self   bool   `json:"selfService"` // always allowed on oneself
}

// handleListActions is the action catalog, for policy editors.
func (s *Server) handleListActions(w http.ResponseWriter, r *http.Request) {
	out := []actionView{}
	for _, rt := range s.Routes() {
		if a := ActionFor(rt.Method, rt.Path); a != "" && !rt.Public {
			out = append(out, actionView{Action: a, Method: rt.Method, Path: rt.Path, Self: selfService[a]})
		}
	}
	out = append(out, actionView{Action: "registry:Pull", Path: "docker pull"}, actionView{Action: "registry:Push", Path: "docker push"},
		actionView{Action: "registry:Delete", Path: "registry token: delete"})
	sort.Slice(out, func(i, j int) bool { return out[i].Action < out[j].Action })
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleMyPermissions lists the caller's statements, for the dashboard to
// hide what it cannot do.
func (s *Server) handleMyPermissions(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	type stmt struct {
		Policy   string   `json:"policy"`
		Effect   string   `json:"effect"`
		Action   []string `json:"action"`
		Resource []string `json:"resource"`
	}
	out := []stmt{}
	if p.isRoot() {
		out = append(out, stmt{Policy: "root", Effect: "Allow", Action: []string{"*"}, Resource: []string{"*"}})
	} else {
		st, err := s.statementsFor(r.Context(), p)
		if err != nil {
			s.internalError(w, "load policies", err)
			return
		}
		for _, a := range st {
			if len(a.Statement.Condition) > 0 && !a.Statement.ConditionsHold(p.iamContext(r)) {
				continue
			}
			out = append(out, stmt{Policy: a.Policy, Effect: a.Statement.Effect, Action: a.Statement.Action, Resource: a.Statement.Resource})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"statements": out, "mfa": p.MFA, "mfaEnabled": p.MFAEnabled, "requireMfa": s.requireMFA.Load(), "credentialType": p.CredType})
}

// ── audit log ───────────────────────────────────────────────────────────────

func (s *Server) auditFilter(r *http.Request) (store.AuditFilter, int, error) {
	q := r.URL.Query()
	f := store.AuditFilter{ActorID: q.Get("actor"), Action: q.Get("action"), Resource: q.Get("resource"), Text: q.Get("q")}
	if v := q.Get("actor"); strings.Contains(v, "@") {
		if u, err := s.store.UserByEmail(r.Context(), v); err == nil {
			f.ActorID = u.ID
		}
	}
	for k, dst := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		if v := q.Get(k); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				if d, derr := time.ParseDuration(v); derr == nil && k == "since" {
					t = s.now().Add(-d)
				} else {
					return f, 0, fmt.Errorf("%s must be RFC 3339 (or a duration like 24h for since)", k)
				}
			}
			*dst = t
		}
	}
	if v := q.Get("before"); v != "" {
		f.BeforeID, _ = strconv.ParseInt(v, 10, 64)
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	return f, limit, nil
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	f, limit, err := s.auditFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	items, err := s.store.QueryAudit(r.Context(), f, limit)
	if err != nil {
		s.internalError(w, "query audit", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleExportAudit streams matching events as CSV (at most 100,000).
func (s *Server) handleExportAudit(w http.ResponseWriter, r *http.Request) {
	f, _, err := s.auditFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="syncloud-audit.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time", "actor", "actor_id", "action", "resource", "ip", "user_agent", "detail"})
	total := 0
	for total < 100000 {
		items, err := s.store.QueryAudit(r.Context(), f, 1000)
		if err != nil || len(items) == 0 {
			break
		}
		for _, e := range items {
			d, _ := json.Marshal(e.Detail)
			_ = cw.Write([]string{e.At.Format(time.RFC3339Nano), e.Actor, e.ActorID, e.Action, e.Resource, e.IP, e.UserAgent, string(d)})
		}
		total += len(items)
		f.BeforeID = items[len(items)-1].ID
	}
	cw.Flush()
}

// ── device login (synctl login) ─────────────────────────────────────────────

const deviceTTL = 10 * time.Minute

// handleStartDevice begins a device login: synctl shows the user code and
// polls while the person approves it in the dashboard.
func (s *Server) handleStartDevice(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.Allow(clientIP(r), s.now()) {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts, try again later")
		return
	}
	device := auth.NewToken("syn_dev_")
	code := auth.NewUserCode()
	now := s.now()
	if err := s.store.CreateDeviceCode(r.Context(), store.DeviceCode{DeviceHash: auth.HashToken(device), UserCode: code, CreatedAt: now, ExpiresAt: now.Add(deviceTTL)}); err != nil {
		s.internalError(w, "create device code", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deviceCode": device, "userCode": code, "verificationPath": "/device", "interval": 2, "expiresIn": int(deviceTTL.Seconds())})
}

// handleApproveDevice: the signed-in person confirms the code synctl shows.
func (s *Server) handleApproveDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserCode string `json:"userCode"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, _ := principal(r.Context())
	if p.CredType != CredSession {
		writeError(w, http.StatusForbidden, CodeForbidden, "approve device logins from the dashboard")
		return
	}
	d, err := s.store.DeviceCodeByUserCode(r.Context(), strings.ToUpper(strings.TrimSpace(req.UserCode)), s.now())
	if errors.Is(err, store.ErrNotFound) || (err == nil && d.ApprovedBy != "") {
		writeError(w, http.StatusNotFound, CodeNotFound, "unknown or expired code")
		return
	} else if err != nil {
		s.internalError(w, "get device code", err)
		return
	}
	creds, err := s.issueTemp(r.Context(), store.TempDevice, p.User.ID, "", p.MFA, clientIP(r), 12*time.Hour)
	if err != nil {
		s.internalError(w, "issue credentials", err)
		return
	}
	// The secret and token go to synctl, sealed until it polls.
	blob, _ := json.Marshal(creds)
	if err := s.store.ApproveDeviceCode(r.Context(), d.DeviceHash, p.User.ID, base64.StdEncoding.EncodeToString(s.secrets.Seal(blob, []byte(d.DeviceHash)))); err != nil {
		s.internalError(w, "approve device", err)
		return
	}
	s.audit(r, p.User.ID, "auth:ApproveDevice", "srn:syncloud:user/"+p.User.ID, map[string]any{"credential": creds.AccessKeyID})
	w.WriteHeader(http.StatusNoContent)
}

// handleDeviceToken is synctl's poll: pending until approved, then the
// credentials (once).
func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"deviceCode"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	hash := auth.HashToken(req.DeviceCode)
	d, err := s.store.DeviceCodeByHash(r.Context(), hash, s.now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "unknown or expired device code")
		return
	} else if err != nil {
		s.internalError(w, "get device code", err)
		return
	}
	if d.ApprovedBy == "" {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	sealed, _ := base64.StdEncoding.DecodeString(d.Credential)
	blob, err := s.secrets.Open(sealed, []byte(hash))
	if err != nil {
		s.internalError(w, "open device credentials", err)
		return
	}
	_ = s.store.DeleteDeviceCode(r.Context(), hash)
	var creds tempCredentials
	_ = json.Unmarshal(blob, &creds)
	writeJSON(w, http.StatusOK, creds)
}
