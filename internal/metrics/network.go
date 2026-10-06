package metrics

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Talker is a task's or service's recent throughput.
type Talker struct {
	Key   string  `json:"key"` // task ID or project/env/service
	Node  string  `json:"node,omitempty"`
	Owner string  `json:"owner,omitempty"` // a task's service
	RxBps float64 `json:"rxBps"`
	TxBps float64 `json:"txBps"`
}

// NetworkResult is the Network › Traffic data (§8.4): per-node throughput on
// all interfaces and over the mesh, and the busiest services and tasks.
type NetworkResult struct {
	Result
	TopServices []Talker `json:"topServices"`
	TopTasks    []Talker `json:"topTasks"`
}

// Network charts node and mesh throughput over d and ranks the busiest
// services and tasks over the last 5 minutes.
func (s *Store) Network(ctx context.Context, d time.Duration, now time.Time) (NetworkResult, error) {
	if s.url == "" {
		return NetworkResult{}, ErrDisabled
	}
	step := max(int(d.Seconds()/120), 10)
	end := time.Unix((now.Unix()/int64(step)+1)*int64(step), 0)
	res := NetworkResult{Result: Result{Start: end.Add(-d), End: end, Step: step, Charts: map[string][]Series{}}}
	w := fmt.Sprintf("%ds", max(2*step, 60))
	byNode := func(m map[string]string) string { return m["node"] }
	for chart, metric := range map[string]string{
		"nodeRx": "syncloud_node_net_rx_bytes_total", "nodeTx": "syncloud_node_net_tx_bytes_total",
		"meshRx": "syncloud_node_mesh_rx_bytes_total", "meshTx": "syncloud_node_mesh_tx_bytes_total",
	} {
		series, err := s.queryRangeKey(ctx, fmt.Sprintf(`sum by (node) (rate(%s[%s]))`, metric, w), res.Start, end, step, byNode)
		if err != nil {
			return NetworkResult{}, err
		}
		res.Charts[chart] = series
	}
	talkers := func(by string, key func(map[string]string) string) ([]Talker, error) {
		out := map[string]*Talker{}
		for _, dir := range []string{"rx", "tx"} {
			samples, err := s.Instant(ctx, fmt.Sprintf(`sum by (%s) (rate(syncloud_task_net_%s_bytes_total[5m]))`, by, dir))
			if err != nil {
				return nil, err
			}
			for _, x := range samples {
				k := key(x.Labels)
				t := out[k]
				if t == nil {
					t = &Talker{Key: k, Node: x.Labels["node"]}
					if x.Labels["task"] != "" {
						t.Owner = x.Labels["project"] + "/" + x.Labels["environment"] + "/" + x.Labels["service"]
					}
					out[k] = t
				}
				if dir == "rx" {
					t.RxBps = x.Value
				} else {
					t.TxBps = x.Value
				}
			}
		}
		list := make([]Talker, 0, len(out))
		for _, t := range out {
			list = append(list, *t)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].RxBps+list[i].TxBps > list[j].RxBps+list[j].TxBps })
		if len(list) > 10 {
			list = list[:10]
		}
		return list, nil
	}
	var err error
	if res.TopServices, err = talkers("project, environment, service", func(m map[string]string) string {
		return m["project"] + "/" + m["environment"] + "/" + m["service"]
	}); err != nil {
		return NetworkResult{}, err
	}
	if res.TopTasks, err = talkers("task, node, project, environment, service", func(m map[string]string) string { return m["task"] }); err != nil {
		return NetworkResult{}, err
	}
	return res, nil
}
