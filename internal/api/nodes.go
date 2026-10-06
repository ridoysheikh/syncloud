package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/store"
)

// Node names are DNS labels, so they can appear in internal DNS names (§8.1).
var nodeNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type joinRequest struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	CSR   string `json:"csr"`
}

type joinResponse struct {
	NodeID         string `json:"nodeId"`
	Name           string `json:"name"`
	Certificate    string `json:"certificate"`
	CACertificate  string `json:"caCertificate"`
	GatewayAddress string `json:"gatewayAddress"`
}

// handleJoin registers a node (§6.1). It is authenticated by the join token in
// the body. The node's key stays on the node: we only sign its CSR.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	// Only invalid tokens count: many servers of a pool may join from one
	// NAT address at once.
	if s.joinLimiter.Blocked(clientIP(r), now) {
		writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many attempts, try again later")
		return
	}
	var req joinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	if !nodeNameRE.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "name must be a DNS label: lowercase letters, digits and hyphens, at most 63 characters")
		return
	}
	nodeID := auth.NewID("node_")
	rejoin, isRejoin, err := s.store.RejoinTarget(r.Context(), auth.HashToken(strings.TrimSpace(req.Token)), now)
	if err != nil {
		s.internalError(w, "join node", err)
		return
	}
	if isRejoin {
		nodeID = rejoin.ID
	}
	certPEM, serial, err := s.ca.SignNodeCSR([]byte(req.CSR), nodeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	n := store.Node{ID: nodeID, Name: req.Name, Status: store.NodePending, CertSerial: serial, CreatedAt: now.Truncate(time.Second),
		Schedulable: req.Name != controllerNode || s.controllerSchedulable}
	n.StatusAt = n.CreatedAt
	switch err := s.store.JoinNode(r.Context(), auth.HashToken(strings.TrimSpace(req.Token)), n, now); {
	case errors.Is(err, store.ErrNotFound):
		s.joinLimiter.Fail(clientIP(r), now)
		s.audit(r, "", "node:Join", "srn:syncloud:node/"+req.Name, map[string]any{"result": "invalid_token"})
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid, expired or already used join token")
		return
	case errors.Is(err, store.ErrNameTaken):
		writeError(w, http.StatusConflict, CodeConflict, "a node named "+req.Name+" already exists")
		return
	case err != nil:
		s.internalError(w, "join node", err)
		return
	}
	if stored, err := s.store.NodeByID(r.Context(), n.ID); err == nil {
		n = stored // the join token may have put it in a pool
	}
	s.nodes.Added(n)
	s.audit(r, "", "node:Join", "srn:syncloud:node/"+nodeID, map[string]any{"name": req.Name, "rejoin": isRejoin})
	s.log.Info("node joined", "node", req.Name, "id", nodeID)
	writeJSON(w, http.StatusCreated, joinResponse{
		NodeID: nodeID, Name: req.Name, Certificate: string(certPEM), CACertificate: string(s.ca.CertPEM), GatewayAddress: s.gatewayAddr,
	})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.nodes.List()})
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteNode(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node")
		return
	} else if err != nil {
		s.internalError(w, "delete node", err)
		return
	}
	s.nodes.Removed(id)
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "node:Delete", "srn:syncloud:node/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

type joinTokenResponse struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	SingleUse   bool      `json:"singleUse"`
	Uses        int       `json:"uses"`
	Token       string    `json:"token,omitempty"`
	// Command is the line to run on a new server (when the token is created).
	Command string `json:"command,omitempty"`
}

func toJoinTokenResponse(t store.JoinToken) joinTokenResponse {
	return joinTokenResponse{ID: t.ID, Description: t.Description, CreatedAt: t.CreatedAt.UTC(), ExpiresAt: t.ExpiresAt.UTC(), SingleUse: t.SingleUse, Uses: t.Uses}
}

const maxJoinTokenTTL = 7 * 24 * time.Hour

func (s *Server) handleCreateJoinToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description string `json:"description"`
		TTLMinutes  int    `json:"ttlMinutes"`
		SingleUse   *bool  `json:"singleUse"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.TTLMinutes == 0 {
		req.TTLMinutes = 60
	}
	ttl := time.Duration(req.TTLMinutes) * time.Minute
	if ttl < time.Minute || ttl > maxJoinTokenTTL {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "ttlMinutes must be between 1 and 10080 (7 days)")
		return
	}
	single := req.SingleUse == nil || *req.SingleUse
	u, _ := currentUser(r.Context())
	now := s.now().Truncate(time.Second)
	tok, t := newJoinToken(u.ID, strings.TrimSpace(req.Description), now, ttl, single)
	if err := s.store.CreateJoinToken(r.Context(), t); err != nil {
		s.internalError(w, "create join token", err)
		return
	}
	s.audit(r, u.ID, "node:CreateJoinToken", "srn:syncloud:join-token/"+t.ID, nil)
	resp := toJoinTokenResponse(t)
	resp.Token = tok
	resp.Command = s.joinCommand(r, "--token "+tok)
	writeJSON(w, http.StatusCreated, resp)
}

func newJoinToken(createdBy, desc string, now time.Time, ttl time.Duration, single bool) (string, store.JoinToken) {
	tok := auth.NewToken("SYN-JOIN-")
	return tok, store.JoinToken{
		ID: auth.NewID("jt_"), TokenHash: auth.HashToken(tok), Description: desc, CreatedBy: createdBy,
		CreatedAt: now, ExpiresAt: now.Add(ttl), SingleUse: single,
	}
}

func (s *Server) handleListJoinTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := s.store.ListJoinTokens(r.Context(), s.now())
	if err != nil {
		s.internalError(w, "list join tokens", err)
		return
	}
	out := make([]joinTokenResponse, 0, len(toks))
	for _, t := range toks {
		out = append(out, toJoinTokenResponse(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleDeleteJoinToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteJoinToken(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such join token")
		return
	} else if err != nil {
		s.internalError(w, "delete join token", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "node:DeleteJoinToken", "srn:syncloud:join-token/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// controllerNode is the controller's own node (§6.4).
const controllerNode = "ctl-0"

// handleSetSchedulable is cordon/uncordon (§6.4, Phase 3 adds draining).
func (s *Server) handleSetSchedulable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Schedulable bool `json:"schedulable"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.store.SetNodeSchedulable(r.Context(), id, req.Schedulable); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node")
		return
	} else if err != nil {
		s.internalError(w, "set schedulable", err)
		return
	}
	prev, _ := s.nodes.Get(id)
	s.nodes.SetSchedulable(r.Context(), id, req.Schedulable, prev.Draining && !req.Schedulable)
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "node:SetSchedulable", "srn:syncloud:node/"+id, map[string]any{"schedulable": req.Schedulable})
	if s.workloads != nil {
		s.workloads.EnqueueAll()
	}
	v, _ := s.nodes.Get(id)
	writeJSON(w, http.StatusOK, v)
}

// handleDrainNode moves a node's tasks elsewhere: replacements start first,
// then the node's tasks stop (§5.4). Undo with PUT …/schedulable true.
func (s *Server) handleDrainNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DrainNode(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such node")
		return
	} else if err != nil {
		s.internalError(w, "drain node", err)
		return
	}
	s.nodes.SetSchedulable(r.Context(), id, false, true)
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "node:Drain", "srn:syncloud:node/"+id, nil)
	if s.workloads != nil {
		s.workloads.EnqueueAll()
	}
	v, _ := s.nodes.Get(id)
	writeJSON(w, http.StatusOK, v)
}

// joinBase is the URL new servers download join.sh from.
func (s *Server) joinBase(r *http.Request) string {
	base := ""
	if s.domains != nil {
		base = s.domains.Endpoints().DashboardURL
	}
	if base == "" {
		scheme := "http"
		if isHTTPS(r) {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base
}

// joinPin is the controller certificate's key pin while it is self-signed
// (a private network, where ACME cannot issue), else "".
func (s *Server) joinPin() string {
	if s.certs == nil || s.domains == nil {
		return ""
	}
	base := s.domains.Endpoints().BaseDomain
	if base == "" {
		return ""
	}
	return s.certs.Pin(base)
}

// joinCommand is the one line that joins a server; with a self-signed
// certificate it pins the controller's key instead of skipping checks.
func (s *Server) joinCommand(r *http.Request, args string) string {
	base := s.joinBase(r)
	if pin := s.joinPin(); pin != "" && strings.HasPrefix(base, "https://") {
		return "curl -fsSLk --pinnedpubkey '" + pin + "' " + base + "/join.sh | sudo bash -s -- --pin '" + pin + "' " + args
	}
	return "curl -fsSL " + base + "/join.sh | sudo bash -s -- " + args
}
