package metrics

import (
	"context"
	"fmt"
	"time"
)

// Database charts a managed database over the last d (Phase 12): operations,
// memory against maxmemory, clients, keys, hit rate, evictions and
// expirations, replication lag, CPU and network, a series per member where
// it helps.
func (s *Store) Database(ctx context.Context, databaseID string, d time.Duration, now time.Time) (Result, error) {
	if s.url == "" {
		return Result{}, ErrDisabled
	}
	step := max(int(d.Seconds()/120), 10)
	end := time.Unix((now.Unix()/int64(step)+1)*int64(step), 0)
	res := Result{Start: end.Add(-d), End: end, Step: step, Charts: map[string][]Series{}}
	g := fmt.Sprintf("%ds", max(step, 20))
	w := fmt.Sprintf("%ds", max(2*step, 60))
	sel := fmt.Sprintf(`database_id=%q`, databaseID)
	byMember := func(m map[string]string) string { return m["member"] }
	label := func(name string) func(map[string]string) string {
		return func(map[string]string) string { return name }
	}
	// Only the primary's series (by its is_primary gauge).
	primary := fmt.Sprintf(`and on (member) (last_over_time(syncloud_db_is_primary{%s}[%s]) == 1)`, sel, g)
	qs := []struct {
		chart, q string
		key      func(map[string]string) string
	}{
		{"ops", fmt.Sprintf(`max by (member) (avg_over_time(syncloud_db_ops_per_sec{%s}[%s]))`, sel, g), byMember},
		{"memory", fmt.Sprintf(`max by (member) (max_over_time(syncloud_db_used_memory_bytes{%s}[%s]))`, sel, g), byMember},
		{"maxmemory", fmt.Sprintf(`max(max_over_time(syncloud_db_maxmemory_bytes{%s}[%s]) %s)`, sel, g, primary), label("maxmemory")},
		{"clients", fmt.Sprintf(`max by (member) (max_over_time(syncloud_db_connected_clients{%s}[%s]))`, sel, g), byMember},
		{"keys", fmt.Sprintf(`max(last_over_time(syncloud_db_keys{%s}[%s]) %s)`, sel, g, primary), label("keys")},
		{"hitRate", fmt.Sprintf(`100 * sum(rate(syncloud_db_keyspace_hits_total{%[1]s}[%[2]s])) / (sum(rate(syncloud_db_keyspace_hits_total{%[1]s}[%[2]s])) + sum(rate(syncloud_db_keyspace_misses_total{%[1]s}[%[2]s])))`, sel, w), label("hit rate")},
		{"evictions", fmt.Sprintf(`sum(rate(syncloud_db_evicted_keys_total{%s}[%s]))`, sel, w), label("evicted")},
		{"evictions", fmt.Sprintf(`sum(rate(syncloud_db_expired_keys_total{%s}[%s]))`, sel, w), label("expired")},
		{"lag", fmt.Sprintf(`max by (member) (max_over_time(syncloud_db_replication_lag_bytes{%s}[%s]))`, sel, g), byMember},
		{"cpu", fmt.Sprintf(`max by (member) (avg_over_time(syncloud_db_cpu_percent{%s}[%s]))`, sel, g), byMember},
		{"network", fmt.Sprintf(`sum(rate(syncloud_db_net_input_bytes_total{%s}[%s]))`, sel, w), label("in")},
		{"network", fmt.Sprintf(`sum(rate(syncloud_db_net_output_bytes_total{%s}[%s]))`, sel, w), label("out")},
	}
	for _, q := range qs {
		series, err := s.queryRangeKey(ctx, q.q, res.Start, end, step, q.key)
		if err != nil {
			return Result{}, err
		}
		res.Charts[q.chart] = append(res.Charts[q.chart], series...)
	}
	for _, c := range []string{"ops", "memory", "maxmemory", "clients", "keys", "hitRate", "evictions", "lag", "cpu", "network"} {
		if res.Charts[c] == nil {
			res.Charts[c] = []Series{}
		}
	}
	return res, nil
}
