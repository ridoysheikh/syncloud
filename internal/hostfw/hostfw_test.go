package hostfw

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"testing"
	"time"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func newManager(t *testing.T) (*Manager, *store.Store, *int) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	changes := 0
	m.OnChange = func() { changes++ }
	m.ProtectedIn = func(p netip.Prefix) bool { return p.Contains(netip.MustParseAddr("198.51.100.1")) }
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return m, st, &changes
}

// The original default policy (SSH from anywhere) becomes the built-in
// SSH rule; SSH is open on 22 with a guard.
func TestDefaultPolicyMovesToSSH(t *testing.T) {
	m, st, _ := newManager(t)
	ps, _ := st.ListFirewallPolicies(context.Background())
	if len(ps) != 0 {
		t.Errorf("the default policy is still there: %+v", ps)
	}
	rules, guards, _ := m.For("w1", []string{"198.51.100.1"})
	if len(rules) != 1 || rules[0].GetPorts() != "22" || len(rules[0].GetSources()) != 0 {
		t.Errorf("rules %v", rules)
	}
	if len(guards) != 1 || guards[0].GetRate() != 10 || !slices.Contains(guards[0].GetExempt(), "198.51.100.1") {
		t.Errorf("guards %v", guards)
	}
}

func TestBlocksExpireAndProtect(t *testing.T) {
	ctx := context.Background()
	m, _, changes := newManager(t)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	if _, err := m.Block(ctx, BlockInput{Source: "198.51.100.0/24"}); err == nil {
		t.Error("blocked a range with a cluster node in it")
	}
	if _, err := m.Block(ctx, BlockInput{Source: "203.0.113.9", For: 10 * time.Minute, Reason: "scanner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Block(ctx, BlockInput{Source: "192.0.2.0/24", Node: "w2"}); err != nil {
		t.Fatal(err)
	}
	if *changes != 2 {
		t.Errorf("%d changes", *changes)
	}
	_, _, b1 := m.For("w1", nil)
	_, _, b2 := m.For("w2", nil)
	if len(b1) != 1 || b1[0].GetSource() != "203.0.113.9" || b1[0].GetTimeoutSeconds() != 601 {
		t.Errorf("w1 blocks %v", b1)
	}
	if len(b2) != 2 {
		t.Errorf("w2 blocks %v", b2)
	}
	now = now.Add(11 * time.Minute)
	if _, _, b := m.For("w1", nil); len(b) != 0 {
		t.Errorf("an expired block is still sent: %v", b)
	}
}

func TestAutoBanAndSSHPorts(t *testing.T) {
	m, _, _ := newManager(t)
	hb := &agentv1.Heartbeat{Network: &agentv1.NetworkStatus{ListenersSent: true, Listeners: []*agentv1.Listener{
		{Protocol: "tcp", Address: "0.0.0.0", Port: 2222, Process: "sshd"},
		{Protocol: "tcp", Address: "127.0.0.1", Port: 2223, Process: "sshd"},
		{Protocol: "tcp", Address: "0.0.0.0", Port: 8080, Process: "python3"},
	}}}
	m.OnHeartbeat(store.Node{Name: "w1"}, hb)
	if p := m.SSHPorts("w1"); !slices.Equal(p, []int{22, 2222}) {
		t.Errorf("ssh ports %v", p)
	}
	m.OnHeartbeat(store.Node{Name: "w1"}, &agentv1.Heartbeat{Network: &agentv1.NetworkStatus{Drops: []*agentv1.DropFlow{
		{Direction: "limit", Src: "203.0.113.50", Protocol: "tcp", Port: 2222, Packets: 40},
		{Direction: "limit", Src: "203.0.113.51", Protocol: "tcp", Port: 8080, Packets: 40}, // no guard of ours
		{Direction: "host", Src: "203.0.113.52", Protocol: "tcp", Port: 22, Packets: 1},
	}}})
	deadline := time.Now().Add(2 * time.Second)
	for len(m.Blocks()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	bs := m.Blocks()
	if len(bs) != 1 || bs[0].Source != "203.0.113.50" || bs[0].Node != "w1" || !bs[0].Auto || bs[0].ExpiresAt == nil {
		t.Errorf("blocks %+v", bs)
	}
	fw := &agentv1.Firewall{Rules: []*agentv1.FirewallRule{
		{Id: RuleSSH, Protocol: "tcp", Ports: "2222"},
		{Id: "fwp_x:0", Protocol: "tcp", Ports: "8000-8100", Sources: []string{"203.0.113.0/24"}},
	}}
	ls, _ := m.Listeners("w1", fw, 51820)
	got := map[uint32]string{}
	for _, l := range ls {
		got[l.Port] = l.Verdict
	}
	if got[2222] != "open" || got[2223] != "local" || got[8080] != "restricted" {
		t.Errorf("verdicts %v", got)
	}
	if v, _ := Verdict(&agentv1.Listener{Protocol: "tcp", Address: "0.0.0.0", Port: 5432}, fw, 51820); v != "blocked" {
		t.Errorf("5432: %s", v)
	}
	if v, _ := Verdict(&agentv1.Listener{Protocol: "tcp", Address: "10.90.0.3", Port: 7070}, fw, 51820); v != "private" {
		t.Errorf("mesh address: %s", v)
	}
}

func TestSSHValidation(t *testing.T) {
	for _, s := range []SSH{
		{Rate: 0, AutoBan: true},
		{Sources: []string{"cluster"}},
		{Ports: []int{70000}},
		{Rate: 5, Exempt: []string{"nope"}},
	} {
		if s.Validate() == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}
