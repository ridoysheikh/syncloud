package mesh

import (
	"slices"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/ingress"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// A project's load balancers open HTTP, HTTPS and its public ports; the
// controller and edge pools no longer open them (D22).
func TestFirewallProjectIngress(t *testing.T) {
	own := &ingress.Target{Nodes: []string{"lb1"}}
	opts := Options{
		IsEdge:    func(id string) bool { return id == "n-edge" },
		IsIngress: func(name string) bool { return name == "lb1" },
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
