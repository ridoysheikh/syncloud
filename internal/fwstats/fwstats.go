// Package fwstats collects firewall visibility from agent heartbeats (§8.3):
// per-rule hit counters, dropped connection attempts (the drop log) and
// nodes where security groups cannot be enforced.
package fwstats

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ridoysheikh/syncloud/internal/firewall"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/logs"
	"github.com/ridoysheikh/syncloud/internal/mesh"
	"github.com/ridoysheikh/syncloud/internal/metrics"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Drop is a dropped flow seen by one node, with names for its addresses.
type Drop struct {
	At        time.Time `json:"at"`
	Node      string    `json:"node"`
	Direction string    `json:"direction"` // host | in | out
	Src       string    `json:"src"`
	SrcName   string    `json:"srcName,omitempty"`
	Dst       string    `json:"dst,omitempty"`
	DstName   string    `json:"dstName,omitempty"`
	Protocol  string    `json:"protocol"`
	Port      uint32    `json:"port"`
	Packets   uint64    `json:"packets"`
}

type nodeState struct {
	at       time.Time
	counters map[string]firewall.Counts
	secErr   string
}

// Stats is fed by heartbeats and read by the API.
type Stats struct {
	st      *store.Store
	metrics *metrics.Store
	logs    *logs.Store
	log     *slog.Logger

	mu    sync.Mutex
	nodes map[string]*nodeState // by node name
	drops []Drop                // newest last, at most maxDrops
	names map[string]string     // IP -> name
	named time.Time
}

const maxDrops = 2000

func New(st *store.Store, m *metrics.Store, l *logs.Store, log *slog.Logger) *Stats {
	return &Stats{st: st, metrics: m, logs: l, log: log, nodes: map[string]*nodeState{}}
}

// OnHeartbeat records a node's counters and drops.
func (s *Stats) OnHeartbeat(node store.Node, hb *agentv1.Heartbeat) {
	ns := hb.GetNetwork()
	if ns == nil {
		return
	}
	now := time.Now().UTC()
	counters := map[string]firewall.Counts{}
	var prom strings.Builder
	for _, c := range ns.GetCounters() {
		counters[c.GetId()] = firewall.Counts{Packets: c.GetPackets(), Bytes: c.GetBytes()}
		fmt.Fprintf(&prom, "syncloud_firewall_rule_packets_total{node=%q,rule=%q} %d\n", node.Name, c.GetId(), c.GetPackets())
		fmt.Fprintf(&prom, "syncloud_firewall_rule_bytes_total{node=%q,rule=%q} %d\n", node.Name, c.GetId(), c.GetBytes())
	}
	if prom.Len() > 0 && s.metrics != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = s.metrics.Import(ctx, prom.String())
		}()
	}
	var drops []Drop
	if len(ns.GetDrops()) > 0 {
		names := s.nameIndex()
		for _, d := range ns.GetDrops() {
			drops = append(drops, Drop{At: now, Node: node.Name, Direction: d.GetDirection(), Src: d.GetSrc(), SrcName: names[d.GetSrc()],
				Dst: d.GetDst(), DstName: names[d.GetDst()], Protocol: d.GetProtocol(), Port: d.GetPort(), Packets: d.GetPackets()})
		}
	}
	s.mu.Lock()
	s.nodes[node.Name] = &nodeState{at: now, counters: counters, secErr: ns.GetSecurityError()}
	s.drops = append(s.drops, drops...)
	if over := len(s.drops) - maxDrops; over > 0 {
		s.drops = append([]Drop(nil), s.drops[over:]...)
	}
	s.mu.Unlock()
	if s.logs != nil {
		for _, d := range drops {
			s.logs.Emit(dropLine(d))
		}
	}
}

func label(ip, name string) string {
	if name == "" {
		return ip
	}
	return ip + " (" + name + ")"
}

func dropLine(d Drop) logs.Line {
	var msg string
	switch d.Direction {
	case "host":
		msg = fmt.Sprintf("dropped %s %s → node %s port %d ×%d", d.Protocol, label(d.Src, d.SrcName), d.Node, d.Port, d.Packets)
	default:
		msg = fmt.Sprintf("dropped %s %s → %s:%d ×%d (%s)", d.Protocol, label(d.Src, d.SrcName), label(d.Dst, d.DstName), d.Port, d.Packets,
			map[string]string{"in": "no inbound rule", "out": "no outbound rule"}[d.Direction])
	}
	return logs.Line{Time: d.At, Project: "syncloud", Environment: "system", Service: "firewall", TaskID: "firewall", Node: d.Node,
		Stream: logs.StreamFirewall, Level: "warn", Message: msg, Fields: map[string]string{
			"direction": d.Direction, "src": d.Src, "src_name": d.SrcName, "dst": d.Dst, "dst_name": d.DstName,
			"protocol": d.Protocol, "port": strconv.Itoa(int(d.Port)), "packets": strconv.FormatUint(d.Packets, 10),
		}}
}

// nameIndex maps task, job run and node addresses to names (refreshed at
// most every 10s).
func (s *Stats) nameIndex() map[string]string {
	s.mu.Lock()
	if time.Since(s.named) < 10*time.Second && s.names != nil {
		n := s.names
		s.mu.Unlock()
		return n
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := map[string]string{}
	svcs, _ := s.st.ListServices(ctx)
	byID := map[string]store.Service{}
	for _, sv := range svcs {
		byID[sv.ID] = sv
	}
	tasks, _ := s.st.ActiveTasks(ctx)
	for _, t := range tasks {
		if sv, ok := byID[t.ServiceID]; ok && t.IP != "" {
			out[t.IP] = sv.Project + "/" + sv.Environment + "/" + sv.Name
		}
	}
	runs, _ := s.st.ActiveRunAddresses(ctx)
	for _, r := range runs {
		out[r.IP] = "job run " + r.ID
	}
	nets, _ := s.st.ListNodeNetworks(ctx)
	for _, n := range nets {
		out[mesh.MeshAddr(n.MeshIndex).String()] = "node " + n.NodeName
	}
	s.mu.Lock()
	s.names, s.named = out, time.Now()
	s.mu.Unlock()
	return out
}

// Name returns what an address belongs to ("" when unknown).
func (s *Stats) Name(ip string) string { return s.nameIndex()[ip] }

// Counters sums each rule's counters over every node.
func (s *Stats) Counters() map[string]firewall.Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]firewall.Counts{}
	for _, n := range s.nodes {
		for k, c := range n.counters {
			t := out[k]
			t.Packets += c.Packets
			t.Bytes += c.Bytes
			out[k] = t
		}
	}
	return out
}

// NodeCounters returns one node's counters.
func (s *Stats) NodeCounters(node string) map[string]firewall.Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.nodes[node]; n != nil {
		return n.counters
	}
	return nil
}

// SecurityErrors lists nodes that cannot enforce security groups.
func (s *Stats) SecurityErrors() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for name, n := range s.nodes {
		if n.secErr != "" && time.Since(n.at) < 2*time.Minute {
			out[name] = n.secErr
		}
	}
	return out
}

// DropFilter selects drops; empty fields match everything.
type DropFilter struct {
	Node, Direction, IP string
	Since               time.Time
}

func (f DropFilter) match(d Drop) bool {
	return (f.Node == "" || d.Node == f.Node) && (f.Direction == "" || d.Direction == f.Direction) &&
		(f.IP == "" || d.Src == f.IP || d.Dst == f.IP) && !d.At.Before(f.Since)
}

// Drops returns recent drops kept in memory, newest first.
func (s *Stats) Drops(f DropFilter, limit int) []Drop {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Drop{}
	for i := len(s.drops) - 1; i >= 0 && len(out) < limit; i-- {
		if f.match(s.drops[i]) {
			out = append(out, s.drops[i])
		}
	}
	return out
}

// DropsFromLogs reads the drop log from VictoriaLogs (history beyond the
// controller's memory), newest first.
func (s *Stats) DropsFromLogs(ctx context.Context, f DropFilter, since time.Duration, limit int) ([]Drop, error) {
	lines, err := s.logs.Query(ctx, logs.Filter{Stream: logs.StreamFirewall, Node: f.Node, Text: f.IP}, since, limit)
	if err != nil {
		return nil, err
	}
	out := []Drop{}
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		port, _ := strconv.Atoi(l.Fields["port"])
		pk, _ := strconv.ParseUint(l.Fields["packets"], 10, 64)
		d := Drop{At: l.Time, Node: l.Node, Direction: l.Fields["direction"], Src: l.Fields["src"], SrcName: l.Fields["src_name"],
			Dst: l.Fields["dst"], DstName: l.Fields["dst_name"], Protocol: l.Fields["protocol"], Port: uint32(port), Packets: pk}
		if f.Direction != "" && d.Direction != f.Direction {
			continue
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}
