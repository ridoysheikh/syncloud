// Package agent is syncloud-agent: it joins a node to the controller and keeps
// one outbound mTLS gRPC stream open for heartbeats and (later) commands (§6).
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"syncloud/internal/agent/docker"
	"syncloud/internal/agent/netcfg"
	"syncloud/internal/agent/sysinfo"
	"syncloud/internal/client"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/nodes"
	"syncloud/internal/pki"
	"syncloud/internal/version"
)

// State files under the agent's data directory.
const (
	keyFile   = "node.key"
	certFile  = "node.crt"
	caFile    = "ca.crt"
	stateFile = "agent.json"
)

type State struct {
	NodeID     string `json:"nodeId"`
	Name       string `json:"name"`
	Controller string `json:"controller"`
	Gateway    string `json:"gateway"`
}

// Join registers this machine with the controller using a join token. The
// node key is generated here and never leaves the machine.
func Join(ctx context.Context, dataDir, controllerURL, token, name string) (State, error) {
	if _, err := os.Stat(filepath.Join(dataDir, stateFile)); err == nil {
		return State{}, fmt.Errorf("this machine has already joined (state in %s); remove it to join again", dataDir)
	}
	keyPEM, csrPEM, err := pki.NewNodeKey(name)
	if err != nil {
		return State{}, err
	}
	c, err := client.New(controllerURL, client.Credentials{})
	if err != nil {
		return State{}, err
	}
	resp, err := c.JoinNode(ctx, client.JoinRequest{Token: token, Name: name, CSR: string(csrPEM)})
	if err != nil {
		return State{}, fmt.Errorf("join: %w", err)
	}
	st := State{NodeID: resp.NodeID, Name: resp.Name, Controller: controllerURL, Gateway: resp.GatewayAddress}
	stateJSON, _ := json.MarshalIndent(st, "", "  ")

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return st, err
	}
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{keyFile, keyPEM, 0o600},
		{certFile, []byte(resp.Certificate), 0o644},
		{caFile, []byte(resp.CACertificate), 0o644},
		{stateFile, append(stateJSON, '\n'), 0o644}, // last: its presence means "joined"
	} {
		if err := os.WriteFile(filepath.Join(dataDir, f.name), f.data, f.mode); err != nil {
			return st, err
		}
	}
	return st, nil
}

func loadState(dataDir string) (State, *tls.Config, *certHolder, error) {
	var st State
	b, err := os.ReadFile(filepath.Join(dataDir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil, nil, fmt.Errorf("not joined yet: run `syncloud-agent join` first")
	} else if err != nil {
		return st, nil, nil, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, nil, nil, err
	}
	cert, err := loadNodeCert(dataDir)
	if err != nil {
		return st, nil, nil, err
	}
	holder := &certHolder{}
	holder.set(cert)
	caPEM, err := os.ReadFile(filepath.Join(dataDir, caFile))
	if err != nil {
		return st, nil, nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return st, nil, nil, errors.New("invalid CA certificate")
	}
	host, _, err := net.SplitHostPort(st.Gateway)
	if err != nil {
		return st, nil, nil, fmt.Errorf("gateway address %q: %w", st.Gateway, err)
	}
	return st, &tls.Config{
		// Read on every handshake, so a renewed certificate is used on the next reconnect.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return holder.get(), nil },
		RootCAs:              pool, // only the cluster CA is trusted for the gateway
		ServerName:           host,
		MinVersion:           tls.VersionTLS13,
	}, holder, nil
}

// Options tune the agent.
type Options struct {
	// Network joins the WireGuard mesh (§8). It needs root.
	Network bool
	// AdvertiseAddress is the address peers dial for WireGuard ("" = the
	// address the controller sees).
	AdvertiseAddress string
	// RenewBefore renews the node certificate when less than this is left
	// (default 30 days of the 90-day validity).
	RenewBefore time.Duration
}

// Run keeps the agent connected until ctx ends, reconnecting with backoff.
func Run(ctx context.Context, dataDir string, log *slog.Logger, opts Options) error {
	return RunWith(ctx, dataDir, docker.New(docker.DefaultSocket), log, opts)
}

// RunWith is Run with an explicit Docker client (tests).
func RunWith(ctx context.Context, dataDir string, d *docker.Client, log *slog.Logger, opts Options) error {
	st, tlsCfg, certs, err := loadState(dataDir)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(st.Gateway,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
	)
	if err != nil {
		return err
	}
	defer conn.Close()
	gw := agentv1.NewAgentGatewayServiceClient(conn)
	info := sysinfo.StaticInfo(ctx, "/")

	runner := NewRunner(d, log)
	go runner.Watch(ctx)
	net, err := netcfg.New(dataDir, d, log, opts.Network)
	if err != nil {
		return fmt.Errorf("network: %w", err)
	}
	go net.Run(ctx)
	runner.NetworkReady = net.Ready
	logs := NewLogShipper(d, dataDir, log)
	go logs.Run(ctx)
	renewBefore := opts.RenewBefore
	if renewBefore == 0 {
		renewBefore = 30 * 24 * time.Hour
	}
	a := &agentLink{info: info, runner: runner, net: net, logs: logs, advertise: opts.AdvertiseAddress, log: log,
		dataDir: dataDir, certs: certs, renewBefore: renewBefore}

	log.Info("agent starting", "node", st.Name, "gateway", st.Gateway, "version", version.Version)
	for attempt := 0; ; attempt++ {
		connected, err := a.session(ctx, gw)
		if ctx.Err() != nil {
			return nil
		}
		if status.Code(err) == codes.PermissionDenied {
			// The node was removed or its certificate replaced: retrying cannot help.
			return fmt.Errorf("controller rejected this node: %w", err)
		}
		if connected {
			attempt = 0
		}
		delay := backoff(attempt)
		log.Warn("disconnected from controller", "err", err, "retry_in", delay.Round(100*time.Millisecond))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// agentLink holds what every stream session needs.
type agentLink struct {
	info        nodes.Info
	runner      *Runner
	net         *netcfg.Manager
	logs        *LogShipper
	advertise   string
	log         *slog.Logger
	dataDir     string
	certs       *certHolder
	renewBefore time.Duration

	mu         sync.Mutex
	pendingKey []byte // key for an in-flight renewal
}

// session runs one stream. connected reports whether the controller accepted it.
func (a *agentLink) session(ctx context.Context, gw agentv1.AgentGatewayServiceClient) (connected bool, err error) {
	info, runner, log := a.info, a.runner, a.log
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := gw.Connect(ctx)
	if err != nil {
		return false, err
	}
	err = stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		AgentVersion: version.Version,
		Info: &agentv1.NodeInfo{
			Hostname: info.Hostname, Os: info.OS, Kernel: info.Kernel, Arch: info.Arch,
			CpuCores: int32(info.CPUCores), MemoryBytes: info.MemoryBytes, DiskBytes: info.DiskBytes,
			DockerVersion:      info.DockerVersion,
			WireguardPublicKey: a.net.PublicKey(), AdvertiseAddress: a.advertise,
		},
		Tasks: runner.Snapshot(ctx),
	}}})
	if errors.Is(err, io.EOF) {
		// The server ended the stream (e.g. rejected this node); Recv returns the real status.
		_, err = stream.Recv()
	}
	if err != nil {
		return false, err
	}
	first, err := stream.Recv()
	if err != nil {
		return false, err
	}
	welcome := first.GetWelcome()
	if welcome == nil {
		return false, errors.New("expected Welcome from controller")
	}
	log.Info("connected to controller", "node_id", welcome.GetNodeId())

	interval := time.Duration(welcome.GetHeartbeatIntervalSeconds()) * time.Second
	if interval <= 0 {
		interval = nodes.HeartbeatInterval
	}
	errc := make(chan error, 2)
	renew := make(chan *agentv1.ConnectRequest, 1)
	if req := a.renewalRequest(); req != nil {
		renew <- req
	}
	go func() { errc <- writer(ctx, stream, interval, runner, a.net, a.logs, renew) }()
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			switch m := msg.Msg.(type) {
			case *agentv1.ConnectResponse_RunTask:
				go runner.Run(ctx, m.RunTask.GetSpec())
			case *agentv1.ConnectResponse_StopTask:
				st := m.StopTask
				go runner.Stop(ctx, st.GetTaskId(), time.Duration(st.GetTimeoutSeconds())*time.Second, st.GetRemove())
			case *agentv1.ConnectResponse_Network:
				a.net.Submit(m.Network)
			case *agentv1.ConnectResponse_ConfirmNetwork:
				a.net.Confirm(m.ConfirmNetwork.GetGeneration())
			case *agentv1.ConnectResponse_Certificate:
				a.installCertificate(m.Certificate.GetCertificate())
			}
		}
	}()
	return true, <-errc
}

// writer is the only goroutine that sends on the stream (gRPC streams are not
// safe for concurrent sends): heartbeats on a ticker, task statuses as they come.
func writer(ctx context.Context, stream grpc.BidiStreamingClient[agentv1.ConnectRequest, agentv1.ConnectResponse], every time.Duration, runner *Runner, net *netcfg.Manager, logs *LogShipper, extra <-chan *agentv1.ConnectRequest) error {
	sampler := sysinfo.NewSampler("/")
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		var msg *agentv1.ConnectRequest
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s := <-runner.out:
			msg = &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_TaskStatus{TaskStatus: s}}
		case msg = <-extra:
		case l := <-logs.out:
			msg = logs.batch(l)
		case <-t.C:
			m := sampler.Sample()
			msg = &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{
				Metrics: &agentv1.NodeMetrics{
					CpuPercent: m.CPUPercent, MemoryUsedBytes: m.MemoryUsedBytes, MemoryTotalBytes: m.MemoryTotalBytes,
					DiskUsedBytes: m.DiskUsedBytes, DiskTotalBytes: m.DiskTotalBytes,
					Load1: m.Load1, Load5: m.Load5, Load15: m.Load15,
					NetRxBytes: m.NetRxBytes, NetTxBytes: m.NetTxBytes, UptimeSeconds: m.UptimeSeconds,
				},
				Network: net.Status(),
			}}}
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
}

// backoff: 1s, 2s, 4s … capped at 30s, with ±25% jitter so nodes don't reconnect in lockstep.
func backoff(attempt int) time.Duration {
	d := time.Second << min(attempt, 5)
	d = min(d, 30*time.Second)
	return time.Duration(float64(d) * (0.75 + rand.Float64()*0.5))
}
