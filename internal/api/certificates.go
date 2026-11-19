package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/certs"
	"github.com/ridoysheikh/syncloud/internal/dns"
)

// Certificates a person asks for, authorities and requests (Phase 28).

// certificateSRN names a certificate for IAM: srn:syncloud:certificate/<zone>/<id>
// when it belongs to a registered domain, else srn:syncloud:certificate/<id>.
func (s *Server) certificateSRN(id string) string {
	if s.certs != nil {
		if z := s.certs.ZoneOf(id); z != "" {
			return "srn:syncloud:certificate/" + z + "/" + id
		}
	}
	return "srn:syncloud:certificate/" + id
}

// certificateNewSRN is the resource of a certificate that does not exist yet.
func (s *Server) certificateNewSRN(ctx context.Context, name string) string {
	if s.dns != nil {
		if z, ok := s.dns.ZoneFor(ctx, strings.TrimPrefix(name, "*.")); ok {
			return "srn:syncloud:certificate/" + z.Name + "/new"
		}
	}
	return "srn:syncloud:certificate/new"
}

func (s *Server) requireCerts(w http.ResponseWriter) bool {
	if s.certs == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "certificates are not managed")
		return false
	}
	return true
}

// certError maps the certificate manager's errors to the API's.
func (s *Server) certError(w http.ResponseWriter, what string, err error) {
	var perr *dns.Error
	switch {
	case errors.Is(err, certs.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such certificate")
	case errors.Is(err, certs.ErrInvalid):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, certs.ErrConflict):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.As(err, &perr):
		writeError(w, http.StatusBadGateway, "provider_error", err.Error())
	default:
		s.internalError(w, what, err)
	}
}

func actorOf(r *http.Request) string {
	if u, ok := currentUser(r.Context()); ok {
		if u.Email != "" {
			return u.Email
		}
		return u.ID
	}
	return ""
}

// authorizeCertificateNames decides the exact resource of a new certificate.
func (s *Server) authorizeCertificateNames(w http.ResponseWriter, r *http.Request, action string, names []string) bool {
	if len(names) == 0 {
		return true // the manager says what is wrong
	}
	res := s.certificateNewSRN(r.Context(), names[0])
	if d := s.decide(r, action, res); !d.Allowed {
		s.denied(w, r, action, res, d.Reason)
		return false
	}
	return true
}

func (s *Server) handleListCertificates(w http.ResponseWriter, r *http.Request) {
	items := []certs.View{}
	if s.certs != nil {
		q := r.URL.Query()
		text := strings.ToLower(q.Get("q"))
		// Everyone who may list certificates sees the ones their policy reaches
		// (an editor of one domain sees that domain's certificates).
		// A policy written for the old resource name keeps working.
		all := s.can(r, "settings:ListCertificates", "srn:syncloud:*") || s.can(r, "settings:ListCertificates", "srn:syncloud:certificates")
		for _, c := range s.certs.List() {
			if !all && !s.can(r, "settings:ListCertificates", s.certificateSRN(c.ID)) {
				continue
			}
			if v := q.Get("status"); v != "" && c.Status != v {
				continue
			}
			if v := q.Get("zone"); v != "" && c.Zone != v {
				continue
			}
			if v := q.Get("source"); v != "" && c.Source != v {
				continue
			}
			if v := q.Get("managed"); v != "" && (v == "true") != c.Managed {
				continue
			}
			if text != "" && !strings.Contains(strings.ToLower(strings.Join(c.Names, " ")+" "+c.Authority+" "+c.IssuerName), text) {
				continue
			}
			items = append(items, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	v, ok := s.certs.Get(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such certificate")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type requestCertificateBody struct {
	Names     []string `json:"names"`
	Authority string   `json:"authority"`
	Method    string   `json:"method"`
	KeyType   string   `json:"keyType"`
	Fallback  string   `json:"fallback"`
	AutoRenew *bool    `json:"autoRenew"`
	Days      int      `json:"days"`
}

func (s *Server) handleRequestCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	var req requestCertificateBody
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.authorizeCertificateNames(w, r, "certificate:RequestCertificate", req.Names) {
		return
	}
	v, err := s.certs.Request(r.Context(), certs.RequestInput{Names: req.Names, Authority: req.Authority, Method: req.Method, KeyType: req.KeyType,
		Fallback: req.Fallback, AutoRenew: req.AutoRenew, Days: req.Days, Actor: actorOf(r)})
	if err != nil {
		s.certError(w, "request certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:RequestCertificate", s.certificateSRN(v.ID), map[string]any{"names": v.Names, "method": v.Method, "authority": v.AuthorityID})
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleUpdateCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	var req struct {
		AutoRenew *bool     `json:"autoRenew"`
		Authority *string   `json:"authority"`
		KeyType   *string   `json:"keyType"`
		Fallback  *string   `json:"fallback"`
		AlertDays *[]int    `json:"alertDays"`
		Pinned    *[]string `json:"pinnedHosts"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	v, err := s.certs.Update(r.Context(), id, certs.UpdateInput{AutoRenew: req.AutoRenew, Authority: req.Authority, KeyType: req.KeyType, Fallback: req.Fallback, AlertDays: req.AlertDays, Pinned: req.Pinned}, actorOf(r))
	if err != nil {
		s.certError(w, "update certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:UpdateCertificate", s.certificateSRN(id), nil)
	s.kickAfterChange()
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) kickAfterChange() {
	if s.certs != nil {
		s.certs.Kick()
	}
}

func (s *Server) handleDeleteCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	id := r.PathValue("id")
	res := s.certificateSRN(id)
	if err := s.certs.Delete(r.Context(), id, r.URL.Query().Get("force") == "true"); err != nil {
		s.certError(w, "delete certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:DeleteCertificate", res, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleVerifyCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	id := r.PathValue("id")
	v, err := s.certs.Verify(r.Context(), id, actorOf(r))
	if err != nil {
		s.certError(w, "verify certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:VerifyCertificate", s.certificateSRN(id), map[string]any{"status": v.Status})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleReissueCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	var req struct {
		Names []string `json:"names"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	v, err := s.certs.Reissue(r.Context(), id, req.Names, actorOf(r))
	if err != nil {
		s.certError(w, "reissue certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:ReissueCertificate", s.certificateSRN(id), map[string]any{"names": req.Names})
	writeJSON(w, http.StatusAccepted, v)
}

func (s *Server) handleRevokeCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	id := r.PathValue("id")
	v, err := s.certs.Revoke(r.Context(), id, actorOf(r))
	if err != nil {
		s.certError(w, "revoke certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:RevokeCertificate", s.certificateSRN(id), nil)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleGetCertificateChain(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	pemText, err := s.certs.Chain(r.PathValue("id"))
	if err != nil {
		s.certError(w, "certificate chain", err)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="chain.pem"`)
	_, _ = w.Write([]byte(pemText))
}

func (s *Server) handleListCertificateEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	evs, err := s.certs.Events(r.Context(), r.PathValue("id"))
	if err != nil {
		s.certError(w, "certificate history", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": evs})
}

// exportLimiter keeps private-key exports rare: 10 an hour per caller.
type exportLimiter struct {
	mu sync.Mutex
	at map[string][]time.Time
}

func (l *exportLimiter) allow(who string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at == nil {
		l.at = map[string][]time.Time{}
	}
	keep := l.at[who][:0]
	for _, t := range l.at[who] {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 10 {
		l.at[who] = keep
		return false
	}
	l.at[who] = append(keep, now)
	return true
}

func (s *Server) handleExportCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	id := r.PathValue("id")
	who := actorOf(r)
	if !s.exports.allow(who, time.Now()) {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "private keys can be exported 10 times an hour: try again later")
		return
	}
	chain, key, err := s.certs.Bundle(r.Context(), id, who)
	if err != nil {
		s.certError(w, "export certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:ExportCertificate", s.certificateSRN(id), map[string]any{"privateKey": true})
	writeJSON(w, http.StatusOK, map[string]string{"certificate": chain, "privateKey": key})
}

func (s *Server) handleImportCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	var req struct {
		Chain      string `json:"chain"`
		Key        string `json:"key"`
		Passphrase string `json:"passphrase"`
		PKCS12     string `json:"pkcs12"`
		Password   string `json:"password"`
		Replace    string `json:"replace"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	in := certs.ImportInput{Chain: req.Chain, Key: req.Key, Passphrase: req.Passphrase, Password: req.Password, Replace: req.Replace, Actor: actorOf(r)}
	if req.PKCS12 != "" {
		raw, err := base64.StdEncoding.DecodeString(req.PKCS12)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "pkcs12: base64 of the .p12/.pfx file")
			return
		}
		in.PKCS12 = raw
	}
	// What it covers decides who may import it: its names are in the certificate.
	if req.Replace != "" {
		if d := s.decide(r, "certificate:ImportCertificate", s.certificateSRN(req.Replace)); !d.Allowed {
			s.denied(w, r, "certificate:ImportCertificate", s.certificateSRN(req.Replace), d.Reason)
			return
		}
	} else if names := s.peekNames(in); len(names) > 0 {
		if !s.authorizeCertificateNames(w, r, "certificate:ImportCertificate", names) {
			return
		}
	} else if d := s.decide(r, "certificate:ImportCertificate", "srn:syncloud:certificate/new"); !d.Allowed {
		s.denied(w, r, "certificate:ImportCertificate", "srn:syncloud:certificate/new", d.Reason)
		return
	}
	res, err := s.certs.Import(r.Context(), in)
	if err != nil {
		s.certError(w, "import certificate", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:ImportCertificate", s.certificateSRN(res.View.ID), map[string]any{"names": res.View.Names, "replace": req.Replace})
	writeJSON(w, http.StatusCreated, res)
}

// peekNames reads the names of a certificate before it is accepted (for the permission check).
func (s *Server) peekNames(in certs.ImportInput) []string {
	if len(in.PKCS12) > 0 {
		return nil
	}
	return certs.NamesOfPEM(in.Chain)
}

func (s *Server) handleCreateCertificateRequest(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	var req struct {
		Names   []string `json:"names"`
		KeyType string   `json:"keyType"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.authorizeCertificateNames(w, r, "certificate:CreateCertificateRequest", req.Names) {
		return
	}
	v, err := s.certs.CreateRequest(r.Context(), req.Names, req.KeyType)
	if err != nil {
		s.certError(w, "create certificate request", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:CreateCertificateRequest", "srn:syncloud:certificate-request/"+v.ID, map[string]any{"names": v.Names})
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleListCertificateRequests(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	rs, err := s.certs.ListRequests(r.Context())
	if err != nil {
		s.certError(w, "list certificate requests", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rs})
}

func (s *Server) handleCompleteCertificateRequest(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	var req struct {
		Chain string `json:"chain"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.certs.CompleteRequest(r.Context(), r.PathValue("id"), req.Chain, actorOf(r))
	if err != nil {
		s.certError(w, "complete certificate request", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:CompleteCertificateRequest", s.certificateSRN(res.View.ID), map[string]any{"names": res.View.Names})
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleDeleteCertificateRequest(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	if err := s.certs.DeleteRequest(r.Context(), r.PathValue("id")); err != nil {
		s.certError(w, "delete certificate request", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:DeleteCertificateRequest", "srn:syncloud:certificate-request/"+r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── authorities ─────────────────────────────────────────────────────────────

func (s *Server) handleListCertificateAuthorities(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	as, err := s.certs.Authorities(r.Context())
	if err != nil {
		s.certError(w, "list authorities", err)
		return
	}
	out := as[:0]
	for _, a := range as {
		if s.can(r, "certificate:ListCertificateAuthorities", "srn:syncloud:certificate-authority/"+a.ID) {
			out = append(out, a)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "presets": certs.Presets})
}

func (s *Server) handleGetCertificateAuthority(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	a, err := s.certs.Authority(r.Context(), r.PathValue("id"))
	if err != nil {
		s.certError(w, "get authority", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type authorityBody struct {
	Name         string `json:"name"`
	Preset       string `json:"preset"`
	DirectoryURL string `json:"directoryUrl"`
	Email        string `json:"email"`
	EABKid       string `json:"eabKid"`
	EABHMAC      string `json:"eabHmac"`
	KeyType      string `json:"keyType"`
	Kind         string `json:"kind"`
	Provider     string `json:"provider"`
}

func (b authorityBody) input() certs.AuthorityInput {
	return certs.AuthorityInput{Name: b.Name, Preset: b.Preset, DirectoryURL: b.DirectoryURL, Email: b.Email, EABKid: b.EABKid, EABHMAC: b.EABHMAC, KeyType: b.KeyType, Kind: b.Kind, ProviderID: b.Provider}
}

func (s *Server) handleCreateCertificateAuthority(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	var req authorityBody
	if !decodeJSON(w, r, &req) {
		return
	}
	a, err := s.certs.AddAuthority(r.Context(), req.input())
	if err != nil {
		s.certError(w, "add authority", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:CreateCertificateAuthority", "srn:syncloud:certificate-authority/"+a.ID, map[string]any{"name": a.Name, "directory": a.DirectoryURL, "eab": a.HasEAB})
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleUpdateCertificateAuthority(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	var req struct {
		authorityBody
		Default  *bool `json:"default"`
		Fallback *bool `json:"fallback"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	if req.Default != nil && *req.Default {
		if err := s.certs.SetDefaultAuthority(ctx, id); err != nil {
			s.certError(w, "set default authority", err)
			return
		}
	}
	if req.Fallback != nil {
		target := id
		if !*req.Fallback {
			if s.certs.FallbackAuthorityID(ctx) != id {
				target = s.certs.FallbackAuthorityID(ctx)
			} else {
				target = ""
			}
		}
		if err := s.certs.SetFallbackAuthority(ctx, target); err != nil {
			s.certError(w, "set fallback authority", err)
			return
		}
	}
	var a certs.Authority
	var err error
	if (req.authorityBody != authorityBody{}) {
		a, err = s.certs.UpdateAuthority(ctx, id, req.input())
	} else {
		a, err = s.certs.Authority(ctx, id)
	}
	if err != nil {
		s.certError(w, "update authority", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:UpdateCertificateAuthority", "srn:syncloud:certificate-authority/"+id, nil)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleDeleteCertificateAuthority(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.certs.DeleteAuthority(r.Context(), id); err != nil {
		s.certError(w, "delete authority", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "certificate:DeleteCertificateAuthority", "srn:syncloud:certificate-authority/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestCertificateAuthority(w http.ResponseWriter, r *http.Request) {
	if !s.requireCerts(w) {
		return
	}
	a, err := s.certs.TestAuthority(r.Context(), r.PathValue("id"))
	if err != nil {
		s.certError(w, "test authority", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}
