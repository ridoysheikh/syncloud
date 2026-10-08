package firewall

import (
	"strings"
	"testing"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
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

func TestRenderSecurity(t *testing.T) {
	cfg := &agentv1.NetworkConfig{
		ListenPort: 51820, ContainerSubnet: "10.91.2.0/24", MeshCidr: "10.90.0.0/16", ContainerCidr: "10.91.0.0/16", ServiceCidr: "10.92.0.0/16",
	}
	sec := &Security{
		Policy: &agentv1.SecurityPolicy{
			PlatformIps: []string{"10.90.0.1"},
			Sets: []*agentv1.SecuritySet{
				{Name: "s_web", Ips: []string{"10.91.3.4"}, Match: []string{"svc:svc_web"}},
				{Name: "s_env", Ips: []string{"10.91.3.4", "10.91.3.9"}, Match: []string{"env:shop/production"}},
			},
			Rules: []*agentv1.SecurityRule{
				{Id: "sg_a:in:0", Direction: "in", LocalSet: "s_web", PeerSets: []string{"s_env"}, Protocol: "tcp", Ports: "8080"},
				{Id: "sg_a:in:1", Direction: "in", LocalSet: "s_web", PeerCidrs: []string{"192.168.1.0/24", "10.0.0.1"}, Protocol: "any"},
				{Id: "sg_a:out:0", Direction: "out", LocalSet: "s_web", PeerCidrs: []string{"0.0.0.0/0"}, Protocol: "any"},
			},
		},
		Local: []LocalContainer{
			{IP: "10.91.2.7", Labels: map[string]string{"syncloud.service_id": "svc_web", "syncloud.project": "shop", "syncloud.environment": "production"}},
			{IP: "10.91.2.8", Labels: map[string]string{"syncloud.project": "other", "syncloud.environment": "production"}},
		},
	}
	out, err := RenderWith(cfg, nil, nil, sec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"set s_web {\n\t\ttype ipv4_addr\n\t\telements = { 10.91.2.7, 10.91.3.4 }",
		"elements = { 10.91.2.7, 10.91.3.4, 10.91.3.9 }",
		`ip saddr @sg_platform accept comment "builtin:platform"`,
		`ip daddr @s_web ip saddr @s_env tcp dport 8080 counter accept comment "sg_a:in:0"`,
		`ip daddr @s_web ip saddr { 192.168.1.0/24, 10.0.0.1/32 } counter accept comment "sg_a:in:1"`,
		`ip saddr @s_web counter return comment "sg_a:out:0"`,
		`counter drop comment "builtin:sg-in-deny"`,
		`iifname "syncloud0" jump sg_out`,
		`oifname "syncloud0" ip daddr 10.91.2.0/24 jump sg_in`,
		"update @sg_out_drops { ip saddr . ip daddr . meta l4proto . th dport }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "10.91.2.8") {
		t.Error("unrelated local container joined a set")
	}
	bad := []*agentv1.SecurityRule{
		{Id: "x", Direction: "in", LocalSet: "nope", Protocol: "any"},
		{Id: "x", Direction: "in", LocalSet: "s_web", PeerCidrs: []string{"1.2.3.4 } accept"}, Protocol: "any"},
		{Id: "x", Direction: "sideways", LocalSet: "s_web", Protocol: "any"},
		{Id: "x", Direction: "in", LocalSet: "s_web", PeerCidrs: []string{"2001:db8::/64"}, Protocol: "any"},
	}
	for _, r := range bad {
		sec.Policy.Rules = []*agentv1.SecurityRule{r}
		if _, err := RenderWith(cfg, nil, nil, sec); err == nil {
			t.Errorf("rule %+v accepted", r)
		}
	}
}

func TestStripDynamicAndCounters(t *testing.T) {
	listing := "table inet syncloud {\n\tset host_drops {\n\t\ttype ipv4_addr . inet_proto . inet_service\n\t\tsize 4096\t# count 2\n\t\tflags dynamic,timeout\n\t\tcounter\n\t\ttimeout 10m\n\t\telements = { 127.0.0.2 . tcp . 81 counter,\n\t\t\t     127.0.0.2 . tcp . 82 counter }\n\t}\n\tchain input {\n\t}\n}"
	got := StripDynamic(listing)
	if strings.Contains(got, "elements") || strings.Contains(got, "count 2") || !strings.Contains(got, "chain input") {
		t.Errorf("StripDynamic:\n%s", got)
	}
	data := `{"nftables": [{"set": {"name": "host_drops", "elem": [{"elem": {"val": {"concat": ["127.0.0.2", "tcp", 81]}, "counter": {"packets": 2, "bytes": 120}}}]}},
	{"set": {"name": "sg_in_drops", "elem": [{"elem": {"val": {"concat": ["10.91.1.2", "10.91.2.3", "tcp", 5432]}, "counter": {"packets": 3, "bytes": 180}}}]}},
	{"rule": {"comment": "sg_a:in:0", "expr": [{"counter": {"packets": 5, "bytes": 300}}, {"accept": null}]}},
	{"rule": {"comment": "sg_a:in:0", "expr": [{"counter": {"packets": 1, "bytes": 60}}, {"accept": null}]}}]}`
	rules, drops, err := ParseCounters([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if rules["sg_a:in:0"] != (Counts{6, 360}) {
		t.Errorf("rules = %v", rules)
	}
	if drops[DropKey{"host", "127.0.0.2", "", "tcp", 81}] != 2 || drops[DropKey{"in", "10.91.1.2", "10.91.2.3", "tcp", 5432}] != 3 {
		t.Errorf("drops = %v", drops)
	}
}
