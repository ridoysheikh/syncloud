package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"syncloud/internal/certs"
	"syncloud/internal/domain"
)

// ACMEInfo describes the certificate issuer for the dashboard.
type ACMEInfo struct {
	Enabled      bool   `json:"enabled"`
	DirectoryURL string `json:"directoryUrl"`
	Email        string `json:"email"`
}

type domainSettings struct {
	domain.Endpoints
	PublicIP      string   `json:"publicIp"`
	PublicIPError string   `json:"publicIpError,omitempty"`
	Suggestions   []string `json:"suggestions"`
	ACME          ACMEInfo `json:"acme"`
	Warnings      []string `json:"warnings"`
}

func (s *Server) domainSettings(ctx context.Context) domainSettings {
	out := domainSettings{Endpoints: s.domains.Endpoints(), Suggestions: []string{}, ACME: s.acme, Warnings: []string{}}
	if s.detector != nil {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		ip, err := s.detector.PublicIP(ctx)
		if err != nil {
			out.PublicIPError = err.Error()
		} else {
			out.PublicIP = ip
			for _, svc := range domain.WildcardServices {
				out.Suggestions = append(out.Suggestions, domain.Wildcard(ip, svc))
			}
		}
	}
	return out
}

func (s *Server) handleGetDomain(w http.ResponseWriter, r *http.Request) {
	if s.domains == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "domains are not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.domainSettings(r.Context()))
}

func (s *Server) handleSetDomain(w http.ResponseWriter, r *http.Request) {
	if s.domains == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "domains are not configured")
		return
	}
	var req struct {
		BaseDomain string `json:"baseDomain"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	before := s.domains.Base()
	ep, err := s.domains.Set(r.Context(), req.BaseDomain)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "settings:SetBaseDomain", "srn:syncloud:settings/domain", map[string]any{"from": before, "to": ep.BaseDomain})
	out := s.domainSettings(r.Context())
	if out.PublicIP != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		ok, addrs := domain.Resolves(ctx, ep.BaseDomain, out.PublicIP)
		cancel()
		if !ok {
			msg := ep.BaseDomain + " does not resolve to this server (" + out.PublicIP + ")"
			if len(addrs) > 0 {
				msg += "; it resolves to " + addrs[0]
			}
			out.Warnings = append(out.Warnings, msg+". Certificates cannot be issued until DNS points here, including registry."+ep.BaseDomain+".")
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListCertificates(w http.ResponseWriter, _ *http.Request) {
	items := []certs.View{}
	if s.certs != nil {
		items = s.certs.List()
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRenewCertificate(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if s.certs == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "certificates are not managed")
		return
	}
	if err := s.certs.Renew(r.Context(), host); errors.Is(err, certs.ErrUnknownHost) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	} else if err != nil {
		s.internalError(w, "renew certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificates:Renew", "srn:syncloud:certificate/"+host, nil)
	w.WriteHeader(http.StatusAccepted)
}
