package firewall

import (
	"strings"
	"testing"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

func TestValidateRule(t *testing.T) {
	good := []struct {
		proto, ports string
		src          []string
	}{
		{"tcp", "22", nil}, {"tcp", "8000-8100", []string{"203.0.113.0/24", "2001:db8::1"}}, {"udp", "", []string{"cluster"}}, {"icmp", "", nil}, {"any", "", []string{"10.0.0.1"}},
	}
	for _, g := range good {
		if err := ValidateRule(g.proto, g.ports, g.src); err != nil {
			t.Errorf("%+v: %v", g, err)
		}
	}
	bad := []struct {
		proto, ports string
		src          []string
	}{
		{"sctp", "", nil}, {"tcp", "0", nil}, {"tcp", "70000", nil}, {"tcp", "90-80", nil}, {"tcp", "22; drop", nil},
		{"icmp", "22", nil}, {"tcp", "22", []string{"evil }"}}, {"tcp", "22", []string{"example.com"}},
	}
	for _, b := range bad {
		if err := ValidateRule(b.proto, b.ports, b.src); err == nil {
			t.Errorf("%+v accepted", b)
		}
	}
}

func TestRender(t *testing.T) {
	cfg := &agentv1.NetworkConfig{
		ListenPort: 51820, ContainerSubnet: "10.91.2.0/24", MeshCidr: "10.90.0.0/16", ContainerCidr: "10.91.0.0/16", ServiceCidr: "10.92.0.0/16",
	}
	fw := &agentv1.Firewall{
		ClusterSources: []string{"198.51.100.7", "2001:db8::7"},
		Rules: []*agentv1.FirewallRule{
			{Id: "fwp_default:0", Protocol: "tcp", Ports: "22", Sources: []string{"203.0.113.5", "2001:db8::/64"}},
			{Id: "builtin:https", Protocol: "tcp", Ports: "443"},
			{Id: "fwp_x:0", Protocol: "udp", Ports: "53", Sources: []string{"cluster"}},
		},
	}
	out, err := Render(cfg, fw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"elements = { 198.51.100.7 }",
		"elements = { 2001:db8::7 }",
		`ip saddr { 203.0.113.5/32 } tcp dport 22 counter accept comment "fwp_default:0"`,
		`ip6 saddr { 2001:db8::/64 } tcp dport 22 counter accept comment "fwp_default:0"`,
		`tcp dport 443 counter accept comment "builtin:https"`,
		`ip saddr @cluster4 udp dport 53 counter accept`,
		`counter drop comment "default deny"`,
		`ip saddr 10.91.2.0/24 ip daddr != { 10.90.0.0/16, 10.91.0.0/16, 10.92.0.0/16 }`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Without a firewall there is no input chain.
	out, _ = Render(cfg, nil, nil)
	if strings.Contains(out, "chain input") {
		t.Fatal("input chain without firewall")
	}
	// A bad rule never reaches nft.
	if _, err := Render(cfg, &agentv1.Firewall{Rules: []*agentv1.FirewallRule{{Id: "x", Protocol: "tcp", Ports: "1; flush ruleset"}}}, nil); err == nil {
		t.Fatal("bad rule rendered")
	}
}

func TestRenderServices(t *testing.T) {
	cfg := &agentv1.NetworkConfig{ContainerSubnet: "10.91.2.0/24", MeshCidr: "10.90.0.0/16", ContainerCidr: "10.91.0.0/16", ServiceCidr: "10.92.0.0/16"}
	out, err := Render(cfg, nil, []*agentv1.VirtualService{{Id: "abc123", Vip: "10.92.0.1", Ports: []*agentv1.VirtualPort{
		{Protocol: "tcp", Port: 8080, Backends: []string{"10.91.1.2:8080", "10.91.2.3:8080"}},
		{Protocol: "udp", Port: 53},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ip daddr 10.92.0.1 tcp dport 8080 goto s-abc123-tcp-8080",
		"numgen random mod 2 vmap { 0 : goto s-abc123-tcp-8080-0, 1 : goto s-abc123-tcp-8080-1 }",
		"ip saddr 10.91.2.3 meta mark set meta mark | 0x4000",
		"meta l4proto tcp dnat ip to 10.91.2.3:8080",
		"type nat hook prerouting priority dstnat - 10",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "udp dport 53 goto") {
		t.Error("a port without backends got a dispatch rule")
	}
	for _, bad := range []*agentv1.VirtualService{
		{Id: "x; flush", Vip: "10.92.0.1"},
		{Id: "x", Vip: "10.92.0.1", Ports: []*agentv1.VirtualPort{{Protocol: "tcp", Port: 80, Backends: []string{"1.2.3.4:80 }"}}}},
	} {
		if _, err := Render(cfg, nil, []*agentv1.VirtualService{bad}); err == nil {
			t.Errorf("%v rendered", bad)
		}
	}
}
