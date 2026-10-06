package nodepool

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	good := Spec{Region: "fsn1", Type: "cx22", Image: "ubuntu-24.04", NodeCPU: 2, NodeMemoryMiB: 4096, Autoscale: true}
	if err := Validate("workers", "worker", true, &good, 1, 5); err != nil || good.Headroom != 80 || good.MaxStep != 2 {
		t.Fatalf("%v %+v", err, good)
	}
	for name, c := range map[string]struct {
		name, role string
		provider   bool
		spec       Spec
		min, max   int
	}{
		"default name":     {"default", "worker", false, Spec{}, 0, 0},
		"bad role":         {"x", "gpu", false, Spec{}, 0, 0},
		"max below min":    {"x", "worker", false, Spec{}, 3, 1},
		"manual autoscale": {"x", "worker", false, Spec{Autoscale: true}, 0, 1},
		"no capacity":      {"x", "worker", true, Spec{Region: "r", Type: "t", Image: "i"}, 0, 1},
		"bad thresholds":   {"x", "worker", true, Spec{Region: "r", Type: "t", Image: "i", NodeCPU: 1, NodeMemoryMiB: 1024, Headroom: 30, ScaleInBelow: 50}, 0, 1},
	} {
		if err := Validate(c.name, c.role, c.provider, &c.spec, c.min, c.max); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if ud := UserData("https://ctl.example", "SYN-JOIN-x", "w-abc", ""); !strings.Contains(ud, "curl -fsSL https://ctl.example/join.sh | bash -s -- --token SYN-JOIN-x --name w-abc") {
		t.Error(ud)
	}
}
