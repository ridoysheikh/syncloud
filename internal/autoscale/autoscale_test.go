package autoscale

import (
	"testing"

	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestDecide(t *testing.T) {
	p := store.ScalingPolicy{Min: 1, Max: 10, Metric: "cpu", Target: 60}
	for _, c := range []struct {
		name             string
		current, running int
		value            float64
		ok               bool
		want             int
	}{
		{"scale out in proportion", 3, 3, 82, true, 5},
		{"scale in in proportion", 6, 6, 20, true, 2},
		{"within tolerance", 4, 4, 64, true, 4},
		{"capped at max", 4, 4, 600, true, 10},
		{"floored at min", 4, 4, 1, true, 1},
		{"below min without data", 0, 0, 0, false, 1},
		{"above max", 12, 12, 60, true, 10},
		{"no data holds", 3, 3, 0, false, 3},
		{"starting tasks hold scale out", 5, 3, 90, true, 5},
		{"starting tasks: scale in from running", 5, 2, 15, true, 1},
	} {
		if got := Decide(p, c.current, c.running, c.value, c.ok); got.Desired != c.want {
			t.Errorf("%s: got %d (%s), want %d", c.name, got.Desired, got.Reason, c.want)
		}
	}
	if got := Decide(p, 3, 3, 82, true); got.Reason != "cpu 82 > target 60 % of reserved CPU" {
		t.Errorf("reason %q", got.Reason)
	}
}

func TestValidate(t *testing.T) {
	ok := store.ScalingPolicy{Min: 1, Max: 5, Metric: "rps", Target: 50}
	if err := Validate(&ok); err != nil || ok.ScaleOutCooldown != 60 || ok.ScaleInCooldown != 300 || ok.ScaleInChecks != 4 {
		t.Errorf("defaults: %+v %v", ok, err)
	}
	for _, p := range []store.ScalingPolicy{
		{Min: 1, Max: 5, Metric: "qps", Target: 1},
		{Min: 6, Max: 5, Metric: "cpu", Target: 50},
		{Min: 1, Max: 5, Metric: "cpu", Target: 150},
		{Min: 1, Max: 5, Metric: "latency", Target: 0},
		{Min: 1, Max: 500, Metric: "rps", Target: 10},
		{Min: 1, Max: 5, Metric: "rps", Target: 10, ScaleOutCooldown: 5},
	} {
		if err := Validate(&p); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}
