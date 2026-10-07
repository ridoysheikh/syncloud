package metrics

import (
	"context"
	"fmt"
	"time"
)

// Overview charts the whole cluster over the last d for the dashboard's
// Overview (§10.2): per-node CPU, memory, disk, load and task counts, node
// network throughput, requests and latency over every route, and the
// controller process. Every chart is a fixed window, so old points fall off.
func (s *Store) Overview(ctx context.Context, d time.Duration, now time.Time) (Result, error) {
	if s.url == "" {
		return Result{}, ErrDisabled
	}
	traffic, err := s.Traffic(ctx, TrafficScope{}, d, now)
	if err != nil {
		return Result{}, err
	}
	res := Result{Start: traffic.Start, End: traffic.End, Step: traffic.Step, Charts: map[string][]Series{
		"requests": traffic.Charts["requests"], "latency": traffic.Charts["latency"],
	}}
	step := res.Step
	// Gauges: each point summarizes its step. Rates: a window of two steps.
	g := fmt.Sprintf("%ds", max(step, 20))
	w := fmt.Sprintf("%ds", max(2*step, 60))
	byNode := func(m map[string]string) string { return m["node"] }
	label := func(name string) func(map[string]string) string {
		return func(map[string]string) string { return name }
	}
	qs := []struct {
		chart, q string
		key      func(map[string]string) string
	}{
		{"cpu", fmt.Sprintf(`avg by (node) (avg_over_time(syncloud_node_cpu_percent[%s]))`, g), byNode},
		{"memory", fmt.Sprintf(`100 * max by (node) (max_over_time(syncloud_node_memory_used_bytes[%[1]s])) / max by (node) (max_over_time(syncloud_node_memory_total_bytes[%[1]s]))`, g), byNode},
		{"disk", fmt.Sprintf(`100 * max by (node) (last_over_time(syncloud_node_disk_used_bytes[%[1]s])) / max by (node) (last_over_time(syncloud_node_disk_total_bytes[%[1]s]))`, g), byNode},
		{"load", fmt.Sprintf(`avg by (node) (avg_over_time(syncloud_node_load1[%s]))`, g), byNode},
		{"tasks", fmt.Sprintf(`count by (node) (last_over_time(syncloud_task_cpu_percent[%s]))`, g), byNode},
		{"network", fmt.Sprintf(`sum(rate(syncloud_node_net_rx_bytes_total[%s]))`, w), label("in")},
		{"network", fmt.Sprintf(`sum(rate(syncloud_node_net_tx_bytes_total[%s]))`, w), label("out")},
		{"controllerHeap", fmt.Sprintf(`max(max_over_time(syncloud_controller_heap_bytes[%s]))`, g), label("heap")},
		{"controllerGoroutines", fmt.Sprintf(`max(max_over_time(syncloud_controller_goroutines[%s]))`, g), label("goroutines")},
	}
	for _, q := range qs {
		series, err := s.queryRangeKey(ctx, q.q, res.Start, res.End, step, q.key)
		if err != nil {
			return Result{}, err
		}
		res.Charts[q.chart] = append(res.Charts[q.chart], series...)
	}
	for c, v := range res.Charts {
		if v == nil {
			res.Charts[c] = []Series{}
		}
	}
	return res, nil
}

// RecordController writes the controller process's own gauges.
func (s *Store) RecordController(ctx context.Context, heapBytes uint64, goroutines int) error {
	return s.Import(ctx, fmt.Sprintf("syncloud_controller_heap_bytes %d\nsyncloud_controller_goroutines %d\n", heapBytes, goroutines))
}
