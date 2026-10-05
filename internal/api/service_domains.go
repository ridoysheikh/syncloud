package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"syncloud/internal/domain"
	"syncloud/internal/store"
)

type domainCheck struct {
	Host      string   `json:"host"`
	Expected  string   `json:"expected"`  // the public IP the A record must point to
	Addresses []string `json:"addresses"` // what the host resolves to now
	Ready     bool     `json:"ready"`
	Record    string   `json:"record"` // the DNS record to create
}

func (s *Server) checkDomain(ctx context.Context, host string) domainCheck {
	c := domainCheck{Host: host, Addresses: []string{}}
	if s.detector != nil {
		ip, err := s.detector.PublicIP(ctx)
		if err == nil {
			c.Expected = ip
			c.Record = host + ". A " + ip
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ok, addrs := domain.Resolves(rctx, host, c.Expected)
	if addrs != nil {
		c.Addresses = addrs
	}
	c.Ready = ok && c.Expected != ""
	return c
}

func (s *Server) handleListServiceDomains(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	ds, err := s.store.ListDomains(r.Context(), sv.ID)
	if err != nil {
		s.internalError(w, "list domains", err)
		return
	}
	type item struct {
		store.Domain
		DNS domainCheck `json:"dns"`
	}
	out := make([]item, 0, len(ds))
	for _, d := range ds {
		out = append(out, item{Domain: d, DNS: s.checkDomain(r.Context(), d.Host)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleAddServiceDomain(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Host string `json:"host"`
		Port string `json:"port"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	host, err := domain.Normalize(req.Host)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	if s.domains != nil {
		if base := s.domains.Base(); base != "" && (host == base || host == domain.RegistryHost(base)) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, host+" is used by the platform")
			return
		}
	}
	d, err := s.workloads.AddDomain(r.Context(), sv.ID, host, req.Port)
	if err != nil {
		s.workloadError(w, "add domain", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "service:AddDomain", "srn:syncloud:service/"+sv.Project+"/"+sv.Environment+"/"+sv.Name, map[string]any{"host": host})
	writeJSON(w, http.StatusCreated, map[string]any{"domain": d, "dns": s.checkDomain(r.Context(), host)})
}

func (s *Server) handleRemoveServiceDomain(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	host := r.PathValue("host")
	if err := s.workloads.RemoveDomain(r.Context(), sv.ID, host); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such domain on this service")
		return
	} else if err != nil {
		s.internalError(w, "remove domain", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "service:RemoveDomain", "srn:syncloud:service/"+sv.Project+"/"+sv.Environment+"/"+sv.Name, map[string]any{"host": host})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCheckDomain(w http.ResponseWriter, r *http.Request) {
	host, err := domain.Normalize(r.URL.Query().Get("host"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.checkDomain(r.Context(), host))
}
