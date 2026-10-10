package agent

import (
	"testing"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// Resource weights are applied in place: adding or changing them must not
// change the hash, or every container would be recreated (on upgrade too).
func TestSpecHashIgnoresWeights(t *testing.T) {
	base := &agentv1.TaskSpec{TaskId: "t1", Image: "nginx:1.27", MemoryLimitBytes: 256 << 20}
	h := SpecHash(base)
	w := &agentv1.TaskSpec{TaskId: "t1", Image: "nginx:1.27", MemoryLimitBytes: 256 << 20, CpuShares: 8192, MemoryReservationBytes: 128 << 20}
	if SpecHash(w) != h {
		t.Error("CPU shares or a memory reservation changed the spec hash")
	}
	if w.CpuShares != 8192 {
		t.Error("SpecHash changed its argument")
	}
	if SpecHash(&agentv1.TaskSpec{TaskId: "t1", Image: "nginx:1.28", MemoryLimitBytes: 256 << 20}) == h {
		t.Error("an image change kept the hash")
	}
}
