package firewall

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

func lockedDown() (*agentv1.NetworkConfig, *agentv1.Firewall) {
	cfg := &agentv1.NetworkConfig{
		ListenPort: 51820, ContainerSubnet: "10.91.2.0/24", MeshCidr: "10.90.0.0/16", ContainerCidr: "10.91.0.0/16", ServiceCidr: "10.92.0.0/16",
	}
	fw := &agentv1.Firewall{
		ClusterSources: []string{"198.51.100.2", "2001:db8::2"},
		Rules:          []*agentv1.FirewallRule{{Id: "builtin:ssh", Protocol: "tcp", Ports: "22", Description: "SSH"}},
		Guards: []*agentv1.FirewallGuard{
			{Id: "builtin:ssh", Protocol: "tcp", Ports: "22,2222", Rate: 10, Unit: "minute", Burst: 5, Exempt: []string{"203.0.113.0/24", "2001:db8:1::/48"}},
			{Id: "lb:shop", Protocol: "tcp", Ports: "80,443", Rate: 50, Unit: "second", Burst: 100, MaxConnections: 200},
		},
		Blocks: []*agentv1.FirewallBlock{
			{Source: "192.0.2.7", TimeoutSeconds: 600}, {Source: "192.0.2.128/25"}, {Source: "2001:db8:dead::/48", TimeoutSeconds: 60},
		},
		SealForward: true,
	}
	return cfg, fw
}

func TestRenderLockedDown(t *testing.T) {
	cfg, fw := lockedDown()
	out, err := Render(cfg, fw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type filter hook prerouting priority raw",
		"192.0.2.7 timeout 600s", "192.0.2.128/25", "2001:db8:dead::/48 timeout 60s",
		"tcp dport { 22, 2222 } ct state new ip saddr != @g0_x4 add @g0_r4 { ip saddr limit rate over 10/minute burst 5 packets } update @limit_drops",
		"add @g1_c4 { ip saddr ct count over 200 }",
		"update @host_drops6 { ip6 saddr . meta l4proto . th dport }",
		`jump sealed`, `comment "builtin:forward-seal"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// The guards come before the accept rules.
	if strings.Index(out, "@g0_r4") > strings.Index(out, `accept comment "builtin:ssh"`) {
		t.Error("the SSH guard comes after the SSH accept rule")
	}
	fw.SealForward = false
	if out, _ := Render(cfg, fw, nil); strings.Contains(out, "jump sealed") {
		t.Error("forward sealed while off")
	}
}

func TestGuardAndBlockValidation(t *testing.T) {
	for _, g := range []*agentv1.FirewallGuard{
		{Id: "x", Protocol: "tcp", Ports: "22"},                               // no limit
		{Id: "x", Protocol: "icmp", Ports: "22", Rate: 1, Unit: "second"},     // protocol
		{Id: "x", Protocol: "tcp", Ports: "22;drop", Rate: 1, Unit: "second"}, // ports
		{Id: "x", Protocol: "tcp", Ports: "22", Rate: 1, Unit: "hour"},        // unit
		{Id: "x y", Protocol: "tcp", Ports: "22", Rate: 1, Unit: "second"},    // id
		{Id: "x", Protocol: "tcp", Ports: "22", MaxConnections: 5, Exempt: []string{"evil }"}},
	} {
		if ValidateGuard(g) == nil {
			t.Errorf("%+v accepted", g)
		}
	}
	for _, s := range []string{"0.0.0.0/0", "10.0.0.0/7", "::/0", "x"} {
		if _, err := ValidateBlock(s); err == nil {
			t.Errorf("block %s accepted", s)
		}
	}
}

// The rendered table loads in a real nftables (in a private network
// namespace; skipped where that is not possible).
func TestRenderLoadsInNftables(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	if err := exec.Command("unshare", "-rn", "nft", "list", "ruleset").Run(); err != nil {
		t.Skip("no unprivileged network namespaces")
	}
	cfg, fw := lockedDown()
	sec := &Security{Policy: &agentv1.SecurityPolicy{}}
	out, err := RenderWith(cfg, fw, []*agentv1.VirtualService{{Id: "abc", Vip: "10.92.0.5", Ports: []*agentv1.VirtualPort{{Protocol: "tcp", Port: 80, Backends: []string{"10.91.2.3:80"}}}}}, sec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rules.nft")
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("unshare", "-rn", "nft", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("nft rejects the table: %v\n%s\n%s", err, b, out)
	}
}

// Drift detection ignores what changes by itself: guard state, drop logs
// and block timeouts.
func TestStripDynamicGuardsAndBlocks(t *testing.T) {
	if exec.Command("unshare", "-rn", "nft", "list", "ruleset").Run() != nil {
		t.Skip("no unprivileged network namespaces")
	}
	cfg, fw := lockedDown()
	out, err := Render(cfg, fw, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rules.nft")
	_ = os.WriteFile(path, []byte(out), 0o600)
	script := "nft -f " + path + " && nft -s list table inet syncloud && echo ===== && sleep 1.1 && " +
		"nft add element inet syncloud g0_r4 { 203.0.113.99 } && nft add element inet syncloud limit_drops { 203.0.113.99 . tcp . 22 } && " +
		"nft -s list table inet syncloud"
	b, err := exec.Command("unshare", "-rn", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	before, after, _ := strings.Cut(string(b), "=====\n")
	if StripDynamic(before) != StripDynamic(after) {
		t.Errorf("drift after traffic:\n%s\n---\n%s", StripDynamic(before), StripDynamic(after))
	}
}
