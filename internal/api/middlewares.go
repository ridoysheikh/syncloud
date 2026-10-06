package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/store"
	"syncloud/internal/system"
	"syncloud/internal/traefik"
)

// Descriptions of the middleware presets (§5.7), for the dashboard and CLI.
var presetDescriptions = map[string]string{
	"ip-allowlist":     "Only these client addresses may connect",
	"rate-limit":       "Requests per second per client IP, with a burst",
	"basic-auth":       "Username and password prompt",
	"redirect-www":     "Redirect www.example.com to example.com",
	"cors":             "Cross-origin requests from these origins",
	"security-headers": "HSTS, frame-deny, nosniff and a referrer policy",
	"circuit-breaker":  "Stop forwarding while the service fails",
	"compress":         "Gzip, Brotli and Zstandard responses",
	"retry":            "Retry attempts on another task (replaces the default 2)",
}

type middlewareView struct {
	ID        string          `json:"id"`
	Project   string          `json:"project"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Config    json.RawMessage `json:"config"`
	Services  []string        `json:"services"` // env/name
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type middlewareRequest struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Config   json.RawMessage `json:"config"`
	Services []string        `json:"services"`
}

func (s *Server) requireTraefik(w http.ResponseWriter) bool {
	if s.traefikExtras == nil || s.traefik == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "routing is not enabled")
		return false
	}
	return true
}

func (s *Server) middlewareViews(ctx context.Context, project string) ([]middlewareView, error) {
	list, err := s.store.ListMiddlewares(ctx)
	if err != nil {
		return nil, err
	}
	svcs, err := s.store.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, sv := range svcs {
		names[sv.ID] = sv.Environment + "/" + sv.Name
	}
	out := []middlewareView{}
	for _, m := range list {
		if project != "" && m.Project != project {
			continue
		}
		v := middlewareView{ID: m.ID, Project: m.Project, Name: m.Name, Type: m.Type, Config: traefik.PublicPreset(m.Type, json.RawMessage(m.Config)),
			Services: []string{}, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
		for _, id := range m.ServiceIDs {
			v.Services = append(v.Services, names[id])
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) handleListAllMiddlewares(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	items, err := s.middlewareViews(r.Context(), "")
	if err != nil {
		s.internalError(w, "list middlewares", err)
		return
	}
	types := []map[string]string{}
	for _, t := range traefik.PresetTypes {
		types = append(types, map[string]string{"type": t, "description": presetDescriptions[t]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.filterItems(r, items, itemMiddleware), "types": types})
}

func (s *Server) handleListMiddlewares(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	items, err := s.middlewareViews(r.Context(), p.Name)
	if err != nil {
		s.internalError(w, "list middlewares", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateMiddleware(w http.ResponseWriter, r *http.Request) {
	s.saveMiddleware(w, r, "")
}

func (s *Server) handleUpdateMiddleware(w http.ResponseWriter, r *http.Request) {
	s.saveMiddleware(w, r, r.PathValue("middleware"))
}

func (s *Server) findMiddleware(ctx context.Context, projectID, name string) (*store.Middleware, error) {
	list, err := s.store.ListMiddlewares(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		if m.ProjectID == projectID && m.Name == name {
			return &m, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Server) saveMiddleware(w http.ResponseWriter, r *http.Request, name string) {
	if !s.requireTraefik(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req middlewareRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var existing *store.Middleware
	status, action := http.StatusCreated, "traefik:CreateMiddleware"
	if name != "" {
		m, err := s.findMiddleware(r.Context(), p.ID, name)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "no such middleware")
			return
		} else if err != nil {
			s.internalError(w, "get middleware", err)
			return
		}
		existing, status, action = m, http.StatusOK, "traefik:EditMiddleware"
		if req.Name == "" {
			req.Name = m.Name
		}
		if req.Type == "" {
			req.Type = m.Type
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	if !policyNameRE.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name must be lowercase letters, digits and hyphens (at most 64)")
		return
	}
	var prev json.RawMessage
	if existing != nil {
		if existing.Type != req.Type {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "the type of a middleware cannot change; create another one")
			return
		}
		prev = json.RawMessage(existing.Config)
	}
	cfg, err := traefik.ValidatePreset(req.Type, req.Config, prev)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, req.Type+": "+err.Error())
		return
	}
	var ids []string
	for _, ref := range req.Services {
		env, svName, ok := strings.Cut(strings.TrimSpace(ref), "/")
		if !ok {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "services are written ENV/NAME, e.g. production/web")
			return
		}
		e, err := s.store.EnvironmentByName(r.Context(), p.ID, env)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "no environment "+env+" in "+p.Name)
			return
		}
		sv, err := s.store.ServiceByName(r.Context(), e.ID, svName)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "no service "+ref+" in "+p.Name)
			return
		}
		ids = append(ids, sv.ID)
	}
	now := s.now().UTC().Truncate(time.Second)
	m := store.Middleware{ID: auth.NewID("mw_"), ProjectID: p.ID, Name: req.Name, Type: req.Type, Config: string(cfg), ServiceIDs: ids, CreatedAt: now, UpdatedAt: now}
	if existing != nil {
		m.ID, m.CreatedAt = existing.ID, existing.CreatedAt
	}
	if err := s.store.PutMiddleware(r.Context(), m); errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, CodeConflict, "a middleware named "+m.Name+" already exists in "+p.Name)
		return
	} else if err != nil {
		s.internalError(w, "save middleware", err)
		return
	}
	if err := s.traefikExtras.Reload(r.Context()); err != nil {
		s.log.Error("reload middlewares", "err", err)
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, action, "srn:syncloud:project/"+p.Name+"/middleware/"+m.Name, map[string]any{"type": m.Type, "services": req.Services})
	items, _ := s.middlewareViews(r.Context(), p.Name)
	for _, v := range items {
		if v.ID == m.ID {
			writeJSON(w, status, v)
			return
		}
	}
	w.WriteHeader(status)
}

func (s *Server) handleDeleteMiddleware(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	m, err := s.findMiddleware(r.Context(), p.ID, r.PathValue("middleware"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such middleware")
		return
	} else if err != nil {
		s.internalError(w, "get middleware", err)
		return
	}
	// The custom configuration may refer to it by its Traefik name.
	if strings.Contains(s.traefikExtras.CustomText(), traefik.MiddlewareName(m.ID)) {
		writeError(w, http.StatusConflict, CodeConflict, "the custom Traefik configuration uses "+traefik.MiddlewareName(m.ID)+"; remove it there first")
		return
	}
	if err := s.store.DeleteMiddleware(r.Context(), m.ID); err != nil {
		s.internalError(w, "delete middleware", err)
		return
	}
	_ = s.traefikExtras.Reload(r.Context())
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "traefik:DeleteMiddleware", "srn:syncloud:project/"+p.Name+"/middleware/"+m.Name, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleTraefikConfig is the read-only "Raw config" view (§5.7): exactly
// what Traefik is served, with certificates and password hashes redacted.
func (s *Server) handleTraefikConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.traefik.Redacted())
}

func (s *Server) handleGetTraefikCustom(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"yaml": s.traefikExtras.CustomText()})
}

func (s *Server) validateCustom(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		YAML string `json:"yaml"`
	}
	if !decodeJSON(w, r, &req) {
		return "", false
	}
	if _, err := traefik.ValidateCustom(req.YAML, s.traefik.Generated()); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return "", false
	}
	return req.YAML, true
}

// handleValidateTraefikCustom checks custom configuration without saving.
func (s *Server) handleValidateTraefikCustom(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	if _, ok := s.validateCustom(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
	}
}

// handlePutTraefikCustom validates and stores custom configuration; Traefik
// picks it up on its next poll (2s).
func (s *Server) handlePutTraefikCustom(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	text, ok := s.validateCustom(w, r)
	if !ok {
		return
	}
	var err error
	if strings.TrimSpace(text) == "" {
		err = s.store.DeleteSetting(r.Context(), traefik.SettingCustom)
	} else {
		err = s.store.SetSetting(r.Context(), traefik.SettingCustom, text)
	}
	if err != nil {
		s.internalError(w, "save custom config", err)
		return
	}
	if err := s.traefikExtras.Reload(r.Context()); err != nil {
		s.internalError(w, "reload custom config", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "traefik:EditCustomConfig", "srn:syncloud:traefik/custom", map[string]any{"bytes": len(text)})
	writeJSON(w, http.StatusOK, map[string]string{"yaml": text})
}

// traefikReplica is one running Traefik: the controller's or an edge's.
type traefikReplica struct {
	Node  string `json:"node"`
	Role  string `json:"role"` // controller | edge
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type traefikSettingsView struct {
	Settings traefik.Settings `json:"settings"`
	Defaults traefik.Settings `json:"defaults"`
	// StaticArgs are the flags the settings add to every replica.
	StaticArgs []string         `json:"staticArgs"`
	Replicas   []traefikReplica `json:"replicas"`
	Version    string           `json:"version"`
	// Restarted is set when saving changed static settings, which restarts
	// every replica (a few seconds without public traffic each).
	Restarted bool `json:"restarted,omitempty"`
}

func (s *Server) traefikSettingsView() traefikSettingsView {
	set := s.traefikExtras.Settings()
	v := traefikSettingsView{Settings: set, Defaults: traefik.DefaultSettings(), StaticArgs: set.StaticArgs(), Replicas: []traefikReplica{},
		Version: strings.TrimPrefix(system.ImageTraefik, "traefik:")}
	if s.system != nil {
		if t, ok := s.system.Task("sys-traefik"); ok {
			v.Replicas = append(v.Replicas, traefikReplica{Node: "controller", Role: "controller", State: t.State, Error: t.Error})
		}
	}
	if s.edges != nil {
		for _, h := range s.edges.List() {
			v.Replicas = append(v.Replicas, traefikReplica{Node: h.Node, Role: "edge", State: h.State, Error: h.Error})
		}
	}
	return v
}

func (s *Server) handleGetTraefikSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.traefikSettingsView())
}

// handlePutTraefikSettings saves the global settings. Dynamic ones reach
// Traefik on its next poll; static ones restart every replica.
func (s *Server) handlePutTraefikSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireTraefik(w) {
		return
	}
	in := traefik.DefaultSettings()
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	before := s.traefikExtras.Settings().StaticArgs()
	b, _ := json.Marshal(in)
	if err := s.store.SetSetting(r.Context(), traefik.SettingGlobal, string(b)); err != nil {
		s.internalError(w, "save Traefik settings", err)
		return
	}
	if err := s.traefikExtras.Reload(r.Context()); err != nil {
		s.internalError(w, "reload Traefik settings", err)
		return
	}
	restart := !slices.Equal(before, in.StaticArgs())
	if restart && s.onTraefikSettings != nil {
		s.onTraefikSettings()
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "traefik:UpdateTraefikSettings", "srn:syncloud:traefik/settings", map[string]any{"restart": restart})
	v := s.traefikSettingsView()
	v.Restarted = restart
	writeJSON(w, http.StatusOK, v)
}
