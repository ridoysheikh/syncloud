package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Labeled is a series of an ad-hoc query with all its labels.
type Labeled struct {
	Labels map[string]string `json:"labels"`
	Points [][2]float64      `json:"points"` // [unix ms, value]
}

// maxSeries bounds what the explorer returns.
const maxSeries = 200

// Explore runs a PromQL range query (Metrics explorer, §9.1). step <= 0
// picks one that gives about 240 points.
func (s *Store) Explore(ctx context.Context, q string, start, end time.Time, step int) ([]Labeled, bool, error) {
	if s.url == "" {
		return nil, false, ErrDisabled
	}
	if strings.TrimSpace(q) == "" {
		return nil, false, fmt.Errorf("query is empty")
	}
	if !end.After(start) {
		return nil, false, fmt.Errorf("end must be after start")
	}
	if step <= 0 {
		step = max(int(end.Sub(start).Seconds())/240, 15)
	}
	out, err := s.queryRangeRaw(ctx, q, start, end, step)
	if err != nil {
		return nil, false, err
	}
	sort.Slice(out, func(i, j int) bool { return labelKey(out[i].Labels) < labelKey(out[j].Labels) })
	truncated := len(out) > maxSeries
	if truncated {
		out = out[:maxSeries]
	}
	return out, truncated, nil
}

func labelKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + ",")
	}
	return b.String()
}

// MetricNames lists the stored metric names (for the explorer's suggestions).
func (s *Store) MetricNames(ctx context.Context) ([]string, error) {
	if s.url == "" {
		return nil, ErrDisabled
	}
	v := url.Values{"start": {fmt.Sprint(time.Now().Add(-24 * time.Hour).Unix())}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/api/v1/label/__name__/values?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metrics storage unreachable: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Data, nil
}
