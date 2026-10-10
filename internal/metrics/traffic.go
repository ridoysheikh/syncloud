package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// TrafficScope selects the routes to chart: one service, a project
// environment, or every service (all fields empty). The platform's own
// routes (the dashboard and its API, the registry) are never included: the
// dashboard's polling is not traffic.
type TrafficScope struct {
	ServiceID   string
	Project     string
	Environment string
}

func (sc TrafficScope) selector() string {
	switch {
	case sc.ServiceID != "":
		return fmt.Sprintf(`service_id=%q`, sc.ServiceID)
	case sc.Project != "":
		return fmt.Sprintf(`project=%q,environment=%q`, sc.Project, sc.Environment)
	}
	return `service_id!=""`
}

// Traffic charts requests per second by status class, latency percentiles
// (ms), bandwidth (bytes/s) and, beyond one service, requests per service,
// from Traefik's metrics (§5.7).
func (s *Store) Traffic(ctx context.Context, sc TrafficScope, d time.Duration, now time.Time) (Result, error) {
	if s.url == "" {
		return Result{}, ErrDisabled
	}
	step := max(int(d.Seconds()/120), 10)
	end := time.Unix((now.Unix()/int64(step)+1)*int64(step), 0)
	res := Result{Start: end.Add(-d), End: end, Step: step, Charts: map[string][]Series{}}
	sel := sc.selector()
	w := fmt.Sprintf("%ds", max(2*step, 60))
	label := func(name string) func(map[string]string) string {
		return func(map[string]string) string { return name }
	}
	type query struct {
		chart, q string
		key      func(map[string]string) string
	}
	quantile := func(p float64) string {
		return fmt.Sprintf(`histogram_quantile(%g, sum by (le) (rate(traefik_service_request_duration_seconds_bucket{%s}[%s]))) * 1000`, p, sel, w)
	}
	qs := []query{
		{"requests", fmt.Sprintf(`sum by (class) (label_replace(rate(traefik_service_requests_total{%s}[%s]), "class", "${1}xx", "code", "([0-9]).."))`, sel, w),
			func(m map[string]string) string { return m["class"] }},
		{"latency", quantile(0.5), label("p50")},
		{"latency", quantile(0.95), label("p95")},
		{"latency", quantile(0.99), label("p99")},
		{"bandwidth", fmt.Sprintf(`sum(rate(traefik_service_requests_bytes_total{%s}[%s]))`, sel, w), label("in")},
		{"bandwidth", fmt.Sprintf(`sum(rate(traefik_service_responses_bytes_total{%s}[%s]))`, sel, w), label("out")},
	}
	if sc.ServiceID == "" {
		qs = append(qs, query{"services", fmt.Sprintf(`sum by (project, environment, app) (rate(traefik_service_requests_total{%s}[%s]))`, sel, w),
			func(m map[string]string) string {
				if sc.Project != "" {
					return m["app"]
				}
				return m["project"] + "/" + m["environment"] + "/" + m["app"]
			}})
	}
	for _, q := range qs {
		series, err := s.queryRangeKey(ctx, q.q, res.Start, end, step, q.key)
		if err != nil {
			return Result{}, err
		}
		res.Charts[q.chart] = append(res.Charts[q.chart], series...)
	}
	for _, c := range []string{"requests", "latency", "bandwidth", "services"} {
		if res.Charts[c] == nil && (c != "services" || sc.ServiceID == "") {
			res.Charts[c] = []Series{}
		}
	}
	return res, nil
}

// RouteTraffic is one service's traffic over the summary window.
type RouteTraffic struct {
	ServiceID   string  `json:"serviceId"`
	Project     string  `json:"project"`
	Environment string  `json:"environment"`
	Service     string  `json:"service"`
	RPS         float64 `json:"rps"`
	Errors4xx   float64 `json:"errors4xx"` // per second
	Errors5xx   float64 `json:"errors5xx"` // per second
	P50Ms       float64 `json:"p50Ms"`
	P95Ms       float64 `json:"p95Ms"`
	BytesIn     float64 `json:"bytesIn"`  // per second
	BytesOut    float64 `json:"bytesOut"` // per second
}

// TrafficSummary returns every route's traffic over the last window,
// busiest first.
func (s *Store) TrafficSummary(ctx context.Context, sc TrafficScope, window time.Duration) ([]RouteTraffic, error) {
	if s.url == "" {
		return nil, ErrDisabled
	}
	sel, w := sc.selector(), fmt.Sprintf("%ds", int(window.Seconds()))
	by := "project, environment, app, service_id"
	rows := map[string]*RouteTraffic{}
	get := func(m map[string]string) *RouteTraffic {
		k := m["project"] + "/" + m["environment"] + "/" + m["app"]
		r, ok := rows[k]
		if !ok {
			r = &RouteTraffic{ServiceID: m["service_id"], Project: m["project"], Environment: m["environment"], Service: m["app"]}
			rows[k] = r
		}
		return r
	}
	for _, q := range []struct {
		q   string
		set func(*RouteTraffic, float64)
	}{
		{fmt.Sprintf(`sum by (%s) (rate(traefik_service_requests_total{%s}[%s]))`, by, sel, w), func(r *RouteTraffic, v float64) { r.RPS = v }},
		{fmt.Sprintf(`sum by (%s) (rate(traefik_service_requests_total{%s,code=~"4.."}[%s]))`, by, sel, w), func(r *RouteTraffic, v float64) { r.Errors4xx = v }},
		{fmt.Sprintf(`sum by (%s) (rate(traefik_service_requests_total{%s,code=~"5.."}[%s]))`, by, sel, w), func(r *RouteTraffic, v float64) { r.Errors5xx = v }},
		{fmt.Sprintf(`histogram_quantile(0.5, sum by (le, %s) (rate(traefik_service_request_duration_seconds_bucket{%s}[%s]))) * 1000`, by, sel, w),
			func(r *RouteTraffic, v float64) { r.P50Ms = v }},
		{fmt.Sprintf(`histogram_quantile(0.95, sum by (le, %s) (rate(traefik_service_request_duration_seconds_bucket{%s}[%s]))) * 1000`, by, sel, w),
			func(r *RouteTraffic, v float64) { r.P95Ms = v }},
		{fmt.Sprintf(`sum by (%s) (rate(traefik_service_requests_bytes_total{%s}[%s]))`, by, sel, w), func(r *RouteTraffic, v float64) { r.BytesIn = v }},
		{fmt.Sprintf(`sum by (%s) (rate(traefik_service_responses_bytes_total{%s}[%s]))`, by, sel, w), func(r *RouteTraffic, v float64) { r.BytesOut = v }},
	} {
		samples, err := s.Instant(ctx, q.q)
		if err != nil {
			return nil, err
		}
		for _, x := range samples {
			q.set(get(x.Labels), x.Value)
		}
	}
	out := make([]RouteTraffic, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RPS != out[j].RPS {
			return out[i].RPS > out[j].RPS
		}
		return out[i].Project+out[i].Environment+out[i].Service < out[j].Project+out[j].Environment+out[j].Service
	})
	return out, nil
}

// Sample is one series of an instant query.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Instant runs a MetricsQL instant query; NaN and infinite values are left out.
func (s *Store) Instant(ctx context.Context, q string) ([]Sample, error) {
	if s.url == "" {
		return nil, ErrDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/api/v1/query?"+url.Values{"query": {q}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metrics storage unreachable: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("metrics storage: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("metrics query: %s", body.Error)
	}
	out := make([]Sample, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		str, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(str, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		out = append(out, Sample{Labels: r.Metric, Value: v})
	}
	return out, nil
}
