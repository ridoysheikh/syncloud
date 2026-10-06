// Command fakeagent simulates many nodes for load tests (Phase 9): each joins
// with a real join token and keeps a real mTLS agent stream, sends
// heartbeats, and reports every task it is given as running, without Docker.
//
//	fakeagent -controller http://10.0.0.1:7070 -token SYN-JOIN-… -nodes 50 -dir /tmp/fake
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"

	"syncloud/internal/agent"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

func main() {
	controller := flag.String("controller", "", "controller URL (joins)")
	token := flag.String("token", "", "multi-use join token")
	n := flag.Int("nodes", 50, "nodes to simulate")
	prefix := flag.String("prefix", "load", "node name prefix")
	dir := flag.String("dir", "/tmp/fakeagent", "state directory")
	cores := flag.Int("cores", 8, "CPU cores each node reports")
	memGiB := flag.Int("memory-gib", 32, "memory each node reports")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var tasks, msgs atomic.Int64
	var wg sync.WaitGroup
	for i := range *n {
		name := fmt.Sprintf("%s-%03d", *prefix, i)
		d := filepath.Join(*dir, name)
		if _, err := os.Stat(filepath.Join(d, "agent.json")); err != nil {
			if _, err := agent.Join(ctx, d, *controller, *token, name, ""); err != nil {
				log.Fatalf("%s: join: %v", name, err)
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := &fake{dir: d, name: name, cores: *cores, mem: uint64(*memGiB) << 30, tasks: map[string]*agentv1.TaskStatus{}, nTasks: &tasks, nMsgs: &msgs}
			for ctx.Err() == nil {
				if err := f.session(ctx); err != nil && ctx.Err() == nil {
					log.Printf("%s: %v", name, err)
					time.Sleep(time.Second)
				}
			}
		}()
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-t.C:
			fmt.Printf("%s nodes=%d tasks=%d messages=%d\n", time.Now().Format("15:04:05"), *n, tasks.Load(), msgs.Load())
		}
	}
}

type fake struct {
	dir, name string
	cores     int
	mem       uint64
	nTasks    *atomic.Int64
	nMsgs     *atomic.Int64

	mu     sync.Mutex
	tasks  map[string]*agentv1.TaskStatus
	subnet netip.Prefix
	next   uint32
	gen    uint64
}

func (f *fake) creds() (credentials.TransportCredentials, string, error) {
	var st agent.State
	b, err := os.ReadFile(filepath.Join(f.dir, "agent.json"))
	if err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, "", err
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(f.dir, "node.crt"), filepath.Join(f.dir, "node.key"))
	if err != nil {
		return nil, "", err
	}
	ca, err := os.ReadFile(filepath.Join(f.dir, "ca.crt"))
	if err != nil {
		return nil, "", err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	host, _, _ := net.SplitHostPort(st.Gateway)
	return credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS13}), st.Gateway, nil
}

func (f *fake) session(ctx context.Context) error {
	creds, gw, err := f.creds()
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(gw, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := agentv1.NewAgentGatewayServiceClient(conn).Connect(ctx)
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	f.mu.Lock()
	snapshot := make([]*agentv1.TaskStatus, 0, len(f.tasks))
	for _, t := range f.tasks {
		snapshot = append(snapshot, t)
	}
	f.mu.Unlock()
	if err := stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		AgentVersion: "fake",
		Info: &agentv1.NodeInfo{Hostname: f.name, Os: "linux", Kernel: "fake", Arch: "amd64", CpuCores: int32(f.cores),
			MemoryBytes: f.mem, DiskBytes: 100 << 30, DockerVersion: "fake"},
		Tasks: snapshot,
	}}}); err != nil {
		return err
	}
	out := make(chan *agentv1.ConnectRequest, 1024)
	errc := make(chan error, 2)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			var m *agentv1.ConnectRequest
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case m = <-out:
			case <-t.C:
				f.mu.Lock()
				gen := f.gen
				f.mu.Unlock()
				m = &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{
					Metrics: &agentv1.NodeMetrics{CpuPercent: 10, MemoryUsedBytes: f.mem / 4, MemoryTotalBytes: f.mem, DiskUsedBytes: 10 << 30, DiskTotalBytes: 100 << 30},
					Network: &agentv1.NetworkStatus{Generation: gen, Mode: "fake"},
				}}}
			}
			if err := stream.Send(m); err != nil {
				errc <- err
				return
			}
			f.nMsgs.Add(1)
		}
	}()
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			switch m := msg.Msg.(type) {
			case *agentv1.ConnectResponse_RunTask:
				out <- &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_TaskStatus{TaskStatus: f.run(m.RunTask.GetSpec())}}
			case *agentv1.ConnectResponse_StopTask:
				f.mu.Lock()
				st, ok := f.tasks[m.StopTask.GetTaskId()]
				if ok {
					delete(f.tasks, m.StopTask.GetTaskId())
					f.nTasks.Add(-1)
				}
				f.mu.Unlock()
				if ok {
					s := proto.Clone(st).(*agentv1.TaskStatus)
					s.State = agentv1.TaskState_TASK_STATE_REMOVED
					out <- &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_TaskStatus{TaskStatus: s}}
				}
			case *agentv1.ConnectResponse_Network:
				f.mu.Lock()
				f.gen = m.Network.GetGeneration()
				if p, err := netip.ParsePrefix(m.Network.GetContainerSubnet()); err == nil {
					f.subnet = p
				}
				f.mu.Unlock()
			}
		}
	}()
	return <-errc
}

func (f *fake) run(spec *agentv1.TaskSpec) *agentv1.TaskStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.tasks[spec.TaskId]; ok && st.SpecHash == agent.SpecHash(spec) {
		return st
	}
	if _, ok := f.tasks[spec.TaskId]; !ok {
		f.nTasks.Add(1)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	ip := ""
	if f.subnet.IsValid() {
		f.next++
		a := f.subnet.Addr().As4()
		a[3] = byte(f.next%250 + 2)
		ip = netip.AddrFrom4(a).String()
	}
	st := &agentv1.TaskStatus{TaskId: spec.TaskId, State: agentv1.TaskState_TASK_STATE_RUNNING, ContainerId: hex.EncodeToString(id),
		Image: spec.Image, StartedAtUnix: time.Now().Unix(), SpecHash: agent.SpecHash(spec), Ip: ip}
	f.tasks[spec.TaskId] = st
	return st
}
