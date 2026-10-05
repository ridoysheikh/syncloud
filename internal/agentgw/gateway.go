// Package agentgw is the controller's gRPC endpoint for agents (§12). Agents
// authenticate with client certificates issued by the internal CA; the node
// identity is the certificate's CommonName, checked against the nodes table
// (so deleting a node revokes its certificate).
package agentgw

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	"syncloud/internal/store"
)

// Hooks let other controller components react to agents. Hooks run on the
// stream's goroutine and must not block.
type Hooks struct {
	// OnConnect runs after the Welcome, with the node's Hello (including its
	// container snapshot) and the IP address it connected from.
	OnConnect func(c Conn)
	// OnTaskStatus runs for every task status the agent reports.
	OnTaskStatus func(node store.Node, s *agentv1.TaskStatus)
	// OnHeartbeat runs for every heartbeat.
	OnHeartbeat func(node store.Node, hb *agentv1.Heartbeat)
	// OnDisconnect runs when the stream ends.
	OnDisconnect func(node store.Node)
	// OnLogs runs for every batch of container output.
	OnLogs func(node store.Node, b *agentv1.LogBatch)
	// OnExecOutput runs for output of interactive exec sessions.
	OnExecOutput func(node store.Node, o *agentv1.ExecOutput)
}

type Gateway struct {
	agentv1.UnimplementedAgentGatewayServiceServer
	st       *store.Store
	registry *nodes.Registry
	log      *slog.Logger
	hooks    []Hooks
	ca       *pki.CA

	mu       sync.Mutex
	sessions map[string]*session // by node ID
}

type session struct {
	send chan *agentv1.ConnectResponse
}

var ErrNotConnected = errors.New("node is not connected")

func New(st *store.Store, registry *nodes.Registry, log *slog.Logger) *Gateway {
	return &Gateway{st: st, registry: registry, log: log, sessions: map[string]*session{}}
}

// Conn describes a newly connected agent.
type Conn struct {
	Node   store.Node
	Hello  *agentv1.Hello
	Remote string // IP address the agent connected from
}

// AddHooks registers h. It must be called before Serve.
func (g *Gateway) AddHooks(h Hooks) { g.hooks = append(g.hooks, h) }

// Send queues a command for a connected node.
func (g *Gateway) Send(nodeID string, msg *agentv1.ConnectResponse) error {
	g.mu.Lock()
	sess := g.sessions[nodeID]
	g.mu.Unlock()
	if sess == nil {
		return ErrNotConnected
	}
	select {
	case sess.send <- msg:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("node %s: command queue full", nodeID)
	}
}

// Serve listens on addr with mTLS until ctx ends. hosts are the names and IPs
// agents use to reach the gateway (put in the server certificate).
func (g *Gateway) Serve(ctx context.Context, ca *pki.CA, addr string, hosts []string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return g.ServeListener(ctx, ca, ln, hosts)
}

// ServeListener is Serve on an existing listener.
func (g *Gateway) ServeListener(ctx context.Context, ca *pki.CA, ln net.Listener, hosts []string) error {
	g.ca = ca
	cert, err := ca.ServerCert(hosts, 365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("gateway certificate: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    ca.Pool(),
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
	)
	agentv1.RegisterAgentGatewayServiceServer(srv, g)
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	g.log.Info("agent gateway listening", "addr", ln.Addr().String())
	return srv.Serve(ln)
}

func (g *Gateway) Connect(stream grpc.BidiStreamingServer[agentv1.ConnectRequest, agentv1.ConnectResponse]) error {
	ctx := stream.Context()
	node, err := g.authenticate(ctx)
	if err != nil {
		return err
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first message must be Hello")
	}
	g.registry.Connected(ctx, node.ID, infoFrom(hello))
	defer g.registry.Disconnected(context.WithoutCancel(ctx), node.ID)
	g.log.Info("agent connected", "node", node.Name, "version", hello.GetAgentVersion())

	if err := stream.Send(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Welcome{Welcome: &agentv1.Welcome{
		NodeId: node.ID, HeartbeatIntervalSeconds: int32(nodes.HeartbeatInterval / time.Second),
	}}}); err != nil {
		return err
	}

	// One writer per stream: Send() from other goroutines goes through the queue.
	sess := &session{send: make(chan *agentv1.ConnectResponse, 64)}
	g.mu.Lock()
	g.sessions[node.ID] = sess // a reconnect replaces the old session
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		if g.sessions[node.ID] == sess {
			delete(g.sessions, node.ID)
		}
		g.mu.Unlock()
	}()
	writeErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-sess.send:
				if err := stream.Send(msg); err != nil {
					writeErr <- err
					return
				}
			}
		}
	}()

	remote := ""
	if p, ok := peer.FromContext(ctx); ok {
		if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			remote = host
		}
	}
	for _, h := range g.hooks {
		if h.OnConnect != nil {
			h.OnConnect(Conn{Node: node, Hello: hello, Remote: remote})
		}
	}
	defer func() {
		for _, h := range g.hooks {
			if h.OnDisconnect != nil {
				h.OnDisconnect(node)
			}
		}
	}()

	recv := make(chan *agentv1.ConnectRequest)
	recvErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case recv <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case err := <-writeErr:
			g.log.Info("agent disconnected", "node", node.Name, "err", err)
			return nil
		case err := <-recvErr:
			if !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
				g.log.Info("agent disconnected", "node", node.Name, "err", err)
			}
			return nil
		case msg := <-recv:
			switch m := msg.Msg.(type) {
			case *agentv1.ConnectRequest_Heartbeat:
				g.registry.Heartbeat(ctx, node.ID, metricsFrom(m.Heartbeat.GetMetrics()))
				for _, h := range g.hooks {
					if h.OnHeartbeat != nil {
						h.OnHeartbeat(node, m.Heartbeat)
					}
				}
			case *agentv1.ConnectRequest_TaskStatus:
				for _, h := range g.hooks {
					if h.OnTaskStatus != nil {
						h.OnTaskStatus(node, m.TaskStatus)
					}
				}
			case *agentv1.ConnectRequest_Logs:
				for _, h := range g.hooks {
					if h.OnLogs != nil {
						h.OnLogs(node, m.Logs)
					}
				}
			case *agentv1.ConnectRequest_ExecOutput:
				for _, h := range g.hooks {
					if h.OnExecOutput != nil {
						h.OnExecOutput(node, m.ExecOutput)
					}
				}
			case *agentv1.ConnectRequest_RenewCertificate:
				g.renew(ctx, node, sess, m.RenewCertificate.GetCsr())
			case *agentv1.ConnectRequest_Result:
			case *agentv1.ConnectRequest_Hello:
				return status.Error(codes.InvalidArgument, "Hello sent twice")
			}
		}
	}
}

// authenticate maps the verified client certificate to a node.
func (g *Gateway) authenticate(ctx context.Context) (store.Node, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return store.Node{}, status.Error(codes.Unauthenticated, "no peer")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
		return store.Node{}, status.Error(codes.Unauthenticated, "client certificate required")
	}
	leaf := ti.State.VerifiedChains[0][0]
	n, err := g.st.NodeByID(ctx, leaf.Subject.CommonName)
	if errors.Is(err, store.ErrNotFound) {
		return n, status.Error(codes.PermissionDenied, "node has been removed")
	} else if err != nil {
		return n, status.Error(codes.Internal, "lookup node")
	}
	cur, prev, err := g.st.NodeCertSerials(ctx, n.ID)
	if err != nil {
		return n, status.Error(codes.Internal, "lookup node certificate")
	}
	switch serial := leaf.SerialNumber.Text(16); {
	case serial == cur:
		if prev != "" { // renewal completed: the old certificate is no longer accepted
			if err := g.st.ConfirmNodeCert(ctx, n.ID); err != nil {
				g.log.Warn("confirm node certificate", "node", n.Name, "err", err)
			}
		}
	case prev != "" && serial == prev:
		// Renewed, but the agent has not switched to the new certificate yet.
	default:
		return n, status.Error(codes.PermissionDenied, "certificate has been replaced")
	}
	return n, nil
}

func infoFrom(h *agentv1.Hello) nodes.Info {
	i := h.GetInfo()
	return nodes.Info{
		Hostname: i.GetHostname(), OS: i.GetOs(), Kernel: i.GetKernel(), Arch: i.GetArch(),
		CPUCores: int(i.GetCpuCores()), MemoryBytes: i.GetMemoryBytes(), DiskBytes: i.GetDiskBytes(),
		DockerVersion: i.GetDockerVersion(), AgentVersion: h.GetAgentVersion(),
	}
}

func metricsFrom(m *agentv1.NodeMetrics) nodes.Metrics {
	return nodes.Metrics{
		CPUPercent: m.GetCpuPercent(), MemoryUsedBytes: m.GetMemoryUsedBytes(), MemoryTotalBytes: m.GetMemoryTotalBytes(),
		DiskUsedBytes: m.GetDiskUsedBytes(), DiskTotalBytes: m.GetDiskTotalBytes(),
		Load1: m.GetLoad1(), Load5: m.GetLoad5(), Load15: m.GetLoad15(),
		NetRxBytes: m.GetNetRxBytes(), NetTxBytes: m.GetNetTxBytes(), UptimeSeconds: m.GetUptimeSeconds(),
	}
}

// renew signs a new node certificate (§6.1, 90-day validity).
func (g *Gateway) renew(ctx context.Context, node store.Node, sess *session, csr string) {
	certPEM, serial, err := g.ca.SignNodeCSR([]byte(csr), node.ID)
	if err != nil {
		g.log.Warn("certificate renewal rejected", "node", node.Name, "err", err)
		return
	}
	if err := g.st.RotateNodeCert(ctx, node.ID, serial); err != nil {
		g.log.Error("store renewed certificate", "node", node.Name, "err", err)
		return
	}
	g.log.Info("node certificate renewed", "node", node.Name, "serial", serial)
	select {
	case sess.send <- &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Certificate{Certificate: &agentv1.CertificateIssued{Certificate: string(certPEM)}}}:
	case <-ctx.Done():
	}
}
