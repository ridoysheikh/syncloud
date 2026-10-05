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

type Gateway struct {
	agentv1.UnimplementedAgentGatewayServiceServer
	st       *store.Store
	registry *nodes.Registry
	log      *slog.Logger
}

func New(st *store.Store, registry *nodes.Registry, log *slog.Logger) *Gateway {
	return &Gateway{st: st, registry: registry, log: log}
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

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
				return nil
			}
			g.log.Info("agent disconnected", "node", node.Name, "err", err)
			return nil
		}
		switch m := msg.Msg.(type) {
		case *agentv1.ConnectRequest_Heartbeat:
			g.registry.Heartbeat(ctx, node.ID, metricsFrom(m.Heartbeat.GetMetrics()))
		case *agentv1.ConnectRequest_Result:
			// Command results arrive with the Docker runner.
		case *agentv1.ConnectRequest_Hello:
			return status.Error(codes.InvalidArgument, "Hello sent twice")
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
	if n.CertSerial != leaf.SerialNumber.Text(16) {
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
