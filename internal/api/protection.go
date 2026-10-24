package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/dbs"
	"github.com/ridoysheikh/syncloud/internal/lbprotect"
	"github.com/ridoysheikh/syncloud/internal/store"
)

type protectionValkey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Health      string `json:"health"`
}

type protectionView struct {
	Project string               `json:"project"`
	Spec    lbprotect.Spec       `json:"spec"`
	Redis   lbprotect.RedisState `json:"redis"`
	// Valkeys of the project that can keep the shared counters.
	Valkeys []protectionValkey `json:"valkeys"`
	// OwnNodes: the project has its own load balancers (the network guard
	// applies only there).
	OwnNodes bool `json:"ownNodes"`
	// Stats are the last hour's limited (429) and blocked (403) requests.
	Stats *protectionStats `json:"stats,omitempty"`
	// Secret is returned once, right after it was generated.
	Secret string `json:"secret,omitempty"`
}

type protectionStats struct {
	Limited float64 `json:"limited"`
	Blocked float64 `json:"blocked"`
	Total   float64 `json:"total"`
}

func (s *Server) requireProtection(w http.ResponseWriter) bool {
	if s.protection == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "load-balancer protection is not enabled")
		return false
	}
	return true
}

func (s *Server) protectionView(ctx context.Context, p store.Project) protectionView {
	v := protectionView{Project: p.Name, Spec: s.protection.Get(p.Name), Redis: s.protection.Redis(p.Name), Valkeys: []protectionValkey{}}
	if s.ingress != nil {
		v.OwnNodes = len(s.ingress.Nodes(p.Name)) > 0
	}
	if s.databases != nil {
		if all, err := s.databases.List(ctx); err == nil {
			for _, d := range all {
				if d.Project == p.Name && d.Engine == dbs.EngineValkey && !d.Deleting {
					v.Valkeys = append(v.Valkeys, protectionValkey{ID: d.ID, Name: d.Name, Environment: d.Environment, Health: d.Health})
				}
			}
		}
	}
	if s.metrics != nil {
		mctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		q := fmt.Sprintf(`sum by (code) (increase(traefik_service_requests_total{project=%q}[1h]))`, p.Name)
		if samples, err := s.metrics.Instant(mctx, q); err == nil {
			st := &protectionStats{}
			for _, x := range samples {
				st.Total += x.Value
				switch x.Labels["code"] {
				case "429":
					st.Limited += x.Value
				case "403":
					st.Blocked += x.Value
				}
			}
			v.Stats = st
		}
	}
	return v
}

func (s *Server) handleGetProjectProtection(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok || !s.requireProtection(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.protectionView(r.Context(), p))
}

// handleSetProjectProtection saves a project's traffic protection. The
// bypass secret is write-only: set, generate or clear it.
func (s *Server) handleSetProjectProtection(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok || !s.requireProtection(w) {
		return
	}
	var req struct {
		lbprotect.Spec
		Secret         string `json:"secret"`
		GenerateSecret bool   `json:"generateSecret"`
		ClearSecret    bool   `json:"clearSecret"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	upd := lbprotect.SecretUpdate{Set: req.Secret, Clear: req.ClearSecret}
	if req.GenerateSecret {
		upd.Set = lbprotect.NewSecret()
	}
	spec, err := s.protection.Put(r.Context(), p, req.Spec, upd)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "project:SetProjectProtection", projectSRN(p.Name), map[string]any{"enabled": spec.Enabled, "rateLimit": spec.RateLimit,
		"block": len(spec.Block), "bypassIps": len(spec.Bypass.IPs), "secret": map[bool]string{true: "changed"}[upd.Set != "" || upd.Clear]})
	v := s.protectionView(r.Context(), p)
	if req.GenerateSecret {
		v.Secret = upd.Set
	}
	writeJSON(w, http.StatusOK, v)
}

// handleCreateProtectionValkey creates a small managed Valkey in one of the
// project's environments for shared rate-limit counters and points the
// protection at it. It is an ordinary database, listed with the others.
func (s *Server) handleCreateProtectionValkey(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok || !s.requireProtection(w) {
		return
	}
	if s.databases == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "managed databases are not enabled")
		return
	}
	var req struct {
		Environment string `json:"environment"`
		Name        string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	e, err := s.store.EnvironmentByName(r.Context(), p.ID, req.Environment)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no environment "+req.Environment+" in "+p.Name)
		return
	} else if err != nil {
		s.internalError(w, "get environment", err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = p.Name + "-ratelimit"
		if len(name) > 29 {
			name = strings.TrimRight(name[:19], "-") + "-ratelimit"
		}
	}
	res := databaseSRN(p.Name, e.Name, name)
	if d := s.decide(r, "database:CreateDatabase", res); !d.Allowed {
		s.denied(w, r, "database:CreateDatabase", res, d.Reason)
		return
	}
	u, _ := currentUser(r.Context())
	spec := dbs.Spec{Memory: dbs.Range{Min: 64, Max: 64}, Persistence: "none", EvictionPolicy: "allkeys-lru"}
	db, err := s.databases.Create(r.Context(), dbs.CreateRequest{Name: name, Engine: dbs.EngineValkey, Env: &e, Spec: spec, Actor: u.Email,
		Exists: s.dbPeerExists(r)})
	if err != nil {
		s.databaseErr(w, "create database", err)
		return
	}
	s.audit(r, u.ID, "database:Create", res, map[string]any{"engine": db.Engine, "memory": db.Spec.Memory, "for": "rate limits"})
	cur := s.protection.Get(p.Name)
	cur.Redis.DatabaseID = db.ID
	if _, err := s.protection.Put(r.Context(), p, cur, lbprotect.SecretUpdate{}); err != nil {
		s.internalError(w, "save protection", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.protectionView(r.Context(), p))
}
