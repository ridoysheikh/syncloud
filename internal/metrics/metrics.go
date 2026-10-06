// Package metrics stores task resource samples from the agents in
// VictoriaMetrics and reads them back for the dashboard (§9.1).
package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

// ErrDisabled means no VictoriaMetrics is configured.
var ErrDisabled = errors.New("metrics storage is not configured")

type sample struct {
	node string
	m    *agentv1.TaskMetrics
}

type Store struct {
	url  string
	http *http.Client
	log  *slog.Logger
	in   chan sample
}

// New returns a store writing to VictoriaMetrics at baseURL ("" disables it).
func New(baseURL string, log *slog.Logger) *Store {
	return &Store{url: strings.TrimSuffix(baseURL, "/"), http: &http.Client{Timeout: 15 * time.Second}, log: log, in: make(chan sample, 20000)}
}

// Add queues samples from a node's heartbeat; it never blocks.
func (s *Store) Add(node string, ms []*agentv1.TaskMetrics) {
	if s.url == "" {
		return
	}
	for _, m := range ms {
		select {
		case s.in <- sample{node, m}:
		default: // storage is behind: drop rather than stall heartbeats
		}
	}
}

// Run writes queued samples in batches until ctx ends.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var buf bytes.Buffer
	n := 0
	flush := func() {
		if n == 0 {
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/api/v1/import/prometheus", bytes.NewReader(buf.Bytes()))
		if err == nil {
			var resp *http.Response
			if resp, err = s.http.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 300 {
					err = fmt.Errorf("HTTP %d", resp.StatusCode)
				}
			}
		}
		if err != nil {
			s.log.Warn("write task metrics", "samples", n, "err", err)
		}
		buf.Reset()
		n = 0
	}
	for {
		select {
		case <-ctx.Done():
			return
		case x := <-s.in:
			write(&buf, x)
			if n++; n >= 2000 {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

func write(b *bytes.Buffer, x sample) {
	m := x.m
	labels := fmt.Sprintf(`{task=%q,service_id=%q,project=%q,environment=%q,service=%q,node=%q}`,
		m.GetTaskId(), m.GetServiceId(), m.GetProject(), m.GetEnvironment(), m.GetService(), x.node)
	ts := m.GetAtUnixMs()
	for _, v := range []struct {
		name string
		val  float64
	}{
		{"syncloud_task_cpu_percent", m.GetCpuPercent()},
		{"syncloud_task_memory_bytes", float64(m.GetMemoryBytes())},
		{"syncloud_task_memory_limit_bytes", float64(m.GetMemoryLimitBytes())},
		{"syncloud_task_net_rx_bytes_total", float64(m.GetNetRxBytes())},
		{"syncloud_task_net_tx_bytes_total", float64(m.GetNetTxBytes())},
		{"syncloud_task_block_read_bytes_total", float64(m.GetBlockReadBytes())},
		{"syncloud_task_block_write_bytes_total", float64(m.GetBlockWriteBytes())},
	} {
		fmt.Fprintf(b, "%s%s %s %d\n", v.name, labels, strconv.FormatFloat(v.val, 'f', -1, 64), ts)
	}
}

// Series is one line on a chart.
type Series struct {
	Key    string       `json:"key"`    // task ID or service name
	Node   string       `json:"node"`   // per-task series only
	Points [][2]float64 `json:"points"` // [unix ms, value]
}

// Result holds the charts of one query.
type Result struct {
	Start  time.Time           `json:"start"`
	End    time.Time           `json:"end"`
	Step   int                 `json:"stepSeconds"`
	Charts map[string][]Series `json:"charts"` // cpu, memory, netRx, netTx, diskRead, diskWrite
}

// Ranges the dashboard offers, with their query step.
var Ranges = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

// Scope selects the tasks to chart and how to group them.
type Scope struct {
	ServiceID   string // one service, a series per task
	Project     string // or a whole environment, a series per service
	Environment string
}

// Query returns CPU (percent of one core), memory (bytes) and network and
// disk throughput (bytes/s) over the last d.
func (s *Store) Query(ctx context.Context, sc Scope, d time.Duration, now time.Time) (Result, error) {
	if s.url == "" {
		return Result{}, ErrDisabled
	}
	step := max(int(d.Seconds()/120), 10)
	// Points are aligned to the step; end on the next boundary so the newest
	// samples are always included, whatever the range.
	end := time.Unix((now.Unix()/int64(step)+1)*int64(step), 0)
	res := Result{Start: end.Add(-d), End: end, Step: step, Charts: map[string][]Series{}}
	var sel, by string
	if sc.ServiceID != "" {
		sel, by = fmt.Sprintf(`service_id=%q`, sc.ServiceID), "task, node"
	} else {
		sel, by = fmt.Sprintf(`project=%q,environment=%q`, sc.Project, sc.Environment), "service"
	}
	window := fmt.Sprintf("%ds", max(2*step, 60))
	// Each point summarizes its step: average CPU, peak memory.
	gauge := fmt.Sprintf("%ds", max(step, 20))
	queries := map[string]string{
		"cpu":       fmt.Sprintf(`sum by (%s) (avg_over_time(syncloud_task_cpu_percent{%s}[%s]))`, by, sel, gauge),
		"memory":    fmt.Sprintf(`sum by (%s) (max_over_time(syncloud_task_memory_bytes{%s}[%s]))`, by, sel, gauge),
		"netRx":     fmt.Sprintf(`sum by (%s) (rate(syncloud_task_net_rx_bytes_total{%s}[%s]))`, by, sel, window),
		"netTx":     fmt.Sprintf(`sum by (%s) (rate(syncloud_task_net_tx_bytes_total{%s}[%s]))`, by, sel, window),
		"diskRead":  fmt.Sprintf(`sum by (%s) (rate(syncloud_task_block_read_bytes_total{%s}[%s]))`, by, sel, window),
		"diskWrite": fmt.Sprintf(`sum by (%s) (rate(syncloud_task_block_write_bytes_total{%s}[%s]))`, by, sel, window),
	}
	for name, q := range queries {
		series, err := s.queryRange(ctx, q, res.Start, end, step)
		if err != nil {
			return Result{}, err
		}
		res.Charts[name] = series
	}
	return res, nil
}

func (s *Store) queryRange(ctx context.Context, q string, start, end time.Time, step int) ([]Series, error) {
	return s.queryRangeKey(ctx, q, start, end, step, func(m map[string]string) string {
		if k := m["task"]; k != "" {
			return k
		}
		return m["service"]
	})
}

// queryRangeKey runs a range query, naming each series with key(labels).
func (s *Store) queryRangeKey(ctx context.Context, q string, start, end time.Time, step int, key func(map[string]string) string) ([]Series, error) {
	v := url.Values{"query": {q}, "start": {strconv.FormatInt(start.Unix(), 10)}, "end": {strconv.FormatInt(end.Unix(), 10)}, "step": {strconv.Itoa(step) + "s"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/api/v1/query_range?"+v.Encode(), nil)
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
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("metrics storage: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("metrics query: %s", body.Error)
	}
	out := make([]Series, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		sr := Series{Key: key(r.Metric), Node: r.Metric["node"], Points: make([][2]float64, 0, len(r.Values))}
		for _, p := range r.Values {
			ts, _ := p[0].(float64)
			str, _ := p[1].(string)
			val, err := strconv.ParseFloat(str, 64)
			if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
				continue
			}
			sr.Points = append(sr.Points, [2]float64{ts * 1000, val})
		}
		out = append(out, sr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
