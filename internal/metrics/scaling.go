package metrics

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Import writes Prometheus text-format samples (current time).
func (s *Store) Import(ctx context.Context, text string) error {
	if s.url == "" {
		return ErrDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/api/v1/import/prometheus", strings.NewReader(text))
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("import: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Scaling charts an autoscaled service: desired and running tasks, and the
// policy's metric against its target (§5.5).
func (s *Store) Scaling(ctx context.Context, serviceID string, d time.Duration, now time.Time) (Result, error) {
	if s.url == "" {
		return Result{}, ErrDisabled
	}
	step := max(int(d.Seconds()/120), 15)
	end := time.Unix((now.Unix()/int64(step)+1)*int64(step), 0)
	res := Result{Start: end.Add(-d), End: end, Step: step, Charts: map[string][]Series{}}
	sel := fmt.Sprintf(`service_id=%q`, serviceID)
	w := fmt.Sprintf("%ds", max(step, 30))
	for _, q := range []struct{ chart, q, key string }{
		{"tasks", fmt.Sprintf(`max(max_over_time(syncloud_service_tasks{%s,kind="desired"}[%s]))`, sel, w), "desired"},
		{"tasks", fmt.Sprintf(`max(max_over_time(syncloud_service_tasks{%s,kind="running"}[%s]))`, sel, w), "running"},
		{"metric", fmt.Sprintf(`avg(avg_over_time(syncloud_autoscale_value{%s}[%s]))`, sel, w), "value"},
		{"metric", fmt.Sprintf(`max(last_over_time(syncloud_autoscale_target{%s}[%s]))`, sel, w), "target"},
	} {
		key := q.key
		series, err := s.queryRangeKey(ctx, q.q, res.Start, end, step, func(map[string]string) string { return key })
		if err != nil {
			return Result{}, err
		}
		res.Charts[q.chart] = append(res.Charts[q.chart], series...)
	}
	return res, nil
}
