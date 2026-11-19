package mesh

import (
	"slices"
	"strings"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/firewall"

	"github.com/ridoysheikh/syncloud/internal/ingress"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// A project's load balancers open HTTP, HTTPS and its public ports; the
// controller and edge pools no longer open them (D22).
func TestFirewallProjectIngress(t *testing.T) {
	own := &ingress.Target{Nodes: []string{"lb1"}}
	opts := Options{
		ControllerDNSPorts: []string{"53"},
		IsEdge:             func(id string) bool { return id == "n-edge" },
		IsIngress:          func(name string) bool { return name == "lb1" },
		PublicPorts: func() []PublicPort {
			return []PublicPort{
				{Port: "20000", Protocol: "tcp", Kind: "service"},
				{Port: "20001", Protocol: "udp", Kind: "service", Ingress: own},
			}
		},
	}
	ids := func(self store.NodeNetwork) []string {
		var out []string
		for _, r := range FirewallFor(self, nil, nil, opts).Rules {
			out = append(out, r.Id)
		}
		return out
	}
	ctl := ids(store.NodeNetwork{NodeID: "n-ctl", NodeName: ControllerNode})
	if !slices.Contains(ctl, "builtin:service-tcp-20000") || slices.Contains(ctl, "builtin:service-udp-20001") {
		t.Errorf("controller %v", ctl)
	}
	if !slices.Contains(ctl, "builtin:dns-udp-53") || !slices.Contains(ctl, "builtin:dns-tcp-53") {
		t.Errorf("port 53 is open on the controller, UDP and TCP, by default: %v", ctl)
	}
	edge := ids(store.NodeNetwork{NodeID: "n-edge", NodeName: "e1"})
	if !slices.Contains(edge, "builtin:edge-443") || !slices.Contains(edge, "builtin:edge-service-tcp-20000") || slices.Contains(edge, "builtin:edge-service-udp-20001") {
		t.Errorf("edge %v", edge)
	}
	lb := ids(store.NodeNetwork{NodeID: "n-lb", NodeName: "lb1"})
	if !slices.Equal(lb, []string{"builtin:edge-80", "builtin:edge-443", "builtin:edge-service-udp-20001"}) {
		t.Errorf("load balancer %v", lb)
	}
	if w := ids(store.NodeNetwork{NodeID: "n-w", NodeName: "w1"}); len(w) != 0 {
		t.Errorf("worker %v", w)
	}
}

// A worker that serves no public port still gets everything it needs to be a
// node: address assignment, the mesh, replies, pings and the agent's own DNS
// for its tasks. Only SSH is open to the world (added by the host firewall);
// 53, 80 and 443 are not, because nothing on a worker listens there.
func TestWorkerKeepsWhatItNeeds(t *testing.T) {
	opts := Options{ControllerDNSPorts: []string{"53"}, IsEdge: func(string) bool { return false }, IsIngress: func(string) bool { return false }}
	self := store.NodeNetwork{NodeID: "n-w", NodeName: "w1", MeshIndex: 2, SubnetIndex: 2}
	other := store.NodeNetwork{NodeID: "n-c", NodeName: ControllerNode, Endpoint: "198.51.100.7:51820"}
	cfg := ConfigFor(self, []store.NodeNetwork{self, other}, 1)
	fw := FirewallFor(self, []store.NodeNetwork{self, other}, nil, opts)
	if len(fw.Rules) != 0 {
		t.Fatalf("a worker opens no port of its own: %v", fw.Rules)
	}
	out, err := firewall.Render(cfg, fw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for what, want := range map[string]string{
		"replies to what the node started": "ct state established,related accept",
		"loopback (host checks)":           `iifname "lo" accept`,
		"the mesh and container bridges (the agent's DNS and discovery for tasks)": `iifname { "wg-syncloud", "syncloud0", "docker0" } accept`,
		"pings":                       "icmp type echo-request",
		"path MTU and neighbours":     "ipv6-icmp accept",
		"the mesh from cluster nodes": `udp dport 51820 ip saddr @cluster4 accept comment "builtin:wireguard"`,
		"address assignment (DHCP)":   `udp sport 67 udp dport 68 counter accept comment "builtin:dhcp"`,
		"address assignment (DHCPv6)": `udp sport 547 udp dport 546 counter accept comment "builtin:dhcpv6"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("a worker must open %s: missing %q", what, want)
		}
	}
	for _, not := range []string{"dport 53 ", "dport 80 ", "dport 443 "} {
		if strings.Contains(out, not) {
			t.Errorf("a worker must not open %q to the world", not)
		}
	}
}
