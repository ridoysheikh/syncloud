// Package logs collects container output from agents into VictoriaLogs and
// serves queries and live tails (§9.2).
package logs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/store"
)

// Line is one log line with its labels.
type Line struct {
	Time        time.Time `json:"time"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Service     string    `json:"service"`
	TaskID      string    `json:"taskId"`
	Revision    string    `json:"revision,omitempty"`
	Node        string    `json:"node"`
	Stream      string    `json:"stream"`
	Level       string    `json:"level,omitempty"`
	Message     string    `json:"message"`
	// Fields of an access log line (stream "access"): method, host, path,
	// status, duration_ms, bytes, client, upstream, service_id.
	Fields map[string]string `json:"fields,omitempty"`
}

// StreamAccess holds Traefik's access log, one line per request (§5.7).
const StreamAccess = "access"

// StreamFirewall holds dropped connection attempts (§8.3).
const StreamFirewall = "firewall"

// FirewallFields are the fields of a drop line.
var FirewallFields = []string{"direction", "src", "src_name", "dst", "dst_name", "protocol", "port", "packets"}

// streamFields are the extra fields kept per stream.
var streamFields = map[string][]string{StreamAccess: accessFields, StreamFirewall: FirewallFields}

// Emit stores a line produced by the controller itself (not an agent).
func (s *Store) Emit(l Line) {
	s.fanout(l)
	select {
	case s.queue <- l:
	default:
		s.failed.Add(1)
	}
}

// Filter selects lines; empty fields match everything.
type Filter struct {
	Project, Environment, Service, TaskID, Node string
	// Text is a plain substring search (never interpreted as a query).
	Text string
	// Stream "access" selects request lines and "firewall" drop lines; ""
	// means application logs (both are left out).
	Stream string
	// Status is a status class of request lines: "2", "3", "4" or "5".
	Status string
	// Client selects request lines from one client IP.
	Client string
}

func (f Filter) match(l Line) bool {
	if f.Stream == "" && (l.Stream == StreamAccess || l.Stream == StreamFirewall) || f.Stream != "" && l.Stream != f.Stream {
		return false
	}
	if f.Status != "" && !strings.HasPrefix(l.Fields["status"], f.Status) || f.Client != "" && l.Fields["client"] != f.Client {
		return false
	}
	return (f.Project == "" || f.Project == l.Project) && (f.Environment == "" || f.Environment == l.Environment) &&
		(f.Service == "" || f.Service == l.Service) && (f.TaskID == "" || f.TaskID == l.TaskID) &&
		(f.Node == "" || f.Node == l.Node) && (f.Text == "" || strings.Contains(strings.ToLower(l.Message), strings.ToLower(f.Text)))
}

type labels struct{ project, environment, service, revision string }

type Store struct {
	st     *store.Store
	vlURL  string // VictoriaLogs base URL, e.g. http://127.0.0.1:9428
	http   *http.Client
	log    *slog.Logger
	queue  chan Line
	failed atomic.Uint64

	// OnIngest counts log volume per project and environment (metering).
	OnIngest func(project, environment string, bytes int)

	mu    sync.Mutex
	cache map[string]labels // task ID -> labels
	subs  map[*sub]struct{}
}

type sub struct {
	f Filter
	c chan Line
}

func New(st *store.Store, vlURL string, log *slog.Logger) *Store {
	return &Store{st: st, vlURL: strings.TrimRight(vlURL, "/"), http: &http.Client{Timeout: 30 * time.Second}, log: log,
		queue: make(chan Line, 50000), cache: map[string]labels{}, subs: map[*sub]struct{}{}}
}

// OnLogs handles a batch from an agent: label, fan out to tails, queue for storage.
func (s *Store) OnLogs(node store.Node, b *agentv1.LogBatch) {
	if n := b.GetDropped(); n > 0 {
		s.log.Warn("node dropped log lines (buffer full)", "node", node.Name, "lines", n)
	}
	for _, l := range b.GetLines() {
		if l.GetTaskId() == TraefikTaskID {
			if line, ok := s.accessLine(node.Name, l.GetLine()); ok {
				s.fanout(line)
				select {
				case s.queue <- line:
				default:
					s.failed.Add(1)
				}
				continue
			}
		}
		lb := s.labelsFor(l.GetTaskId())
		line := Line{
			Time: time.Unix(0, l.GetTimeUnixNano()).UTC(), Project: lb.project, Environment: lb.environment, Service: lb.service,
			TaskID: l.GetTaskId(), Revision: lb.revision, Node: node.Name, Stream: l.GetStream(), Message: l.GetLine(),
		}
		line.Level = detectLevel(line.Message)
		if s.OnIngest != nil {
			s.OnIngest(line.Project, line.Environment, len(line.Message))
		}
		s.fanout(line)
		select {
		case s.queue <- line:
		default:
			s.failed.Add(1)
		}
	}
}

func (s *Store) labelsFor(taskID string) labels {
	if c, ok := strings.CutPrefix(taskID, "sys-"); ok { // platform components (§5.0)
		return labels{project: "syncloud", environment: "system", service: c}
	}
	s.mu.Lock()
	lb, ok := s.cache[taskID]
	s.mu.Unlock()
	if ok {
		return lb
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if strings.HasPrefix(taskID, "run_") { // job runs (§5.11): service "job-<name>" or "run-<service>"
		var project, env, job, svc string
		err := s.st.R.QueryRowContext(ctx, `SELECT p.name, e.name, coalesce(j.name, ''), coalesce(sv.name, '') FROM job_runs r
			JOIN environments e ON e.id = r.environment_id JOIN projects p ON p.id = e.project_id
			LEFT JOIN jobs j ON j.id = r.job_id LEFT JOIN services sv ON sv.id = r.service_id WHERE r.id = ?`, taskID).Scan(&project, &env, &job, &svc)
		if err != nil {
			return labels{service: "unknown"}
		}
		name := "run-" + svc
		if job != "" {
			name = "job-" + job
		}
		lb = labels{project: project, environment: env, service: name}
		s.mu.Lock()
		s.cache[taskID] = lb
		s.mu.Unlock()
		return lb
	}
	t, err := s.st.TaskByID(ctx, taskID)
	if err != nil {
		return labels{service: "unknown"}
	}
	sv, err := s.st.ServiceByID(ctx, t.ServiceID)
	if err != nil {
		return labels{service: "unknown"}
	}
	lb = labels{project: sv.Project, environment: sv.Environment, service: sv.Name, revision: fmt.Sprint(t.Revision)}
	s.mu.Lock()
	if len(s.cache) > 20000 {
		s.cache = map[string]labels{}
	}
	s.cache[taskID] = lb
	s.mu.Unlock()
	return lb
}

var levelRE = regexp.MustCompile(`(?i)\b(fatal|panic|error|err|warn|warning|info|debug|trace)\b`)

// detectLevel reads a level from JSON logs ("level", "lvl", "severity") or
// the first level-like word.
func detectLevel(msg string) string {
	if strings.HasPrefix(msg, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(msg), &m) == nil {
			for _, k := range []string{"level", "lvl", "severity", "log.level"} {
				if v, ok := m[k].(string); ok {
					return normLevel(v)
				}
			}
		}
	}
	if m := levelRE.FindString(msg[:min(len(msg), 200)]); m != "" {
		return normLevel(m)
	}
	return ""
}

func normLevel(v string) string {
	switch strings.ToLower(v) {
	case "fatal", "panic", "critical", "crit":
		return "fatal"
	case "error", "err":
		return "error"
	case "warn", "warning":
		return "warn"
	case "info", "notice":
		return "info"
	case "debug", "trace":
		return "debug"
	}
	return strings.ToLower(v)
}

// ── live tail ───────────────────────────────────────────────────────────────

// Tail returns new lines matching f until cancel is called.
func (s *Store) Tail(f Filter) (<-chan Line, func()) {
	sb := &sub{f: f, c: make(chan Line, 1000)}
	s.mu.Lock()
	s.subs[sb] = struct{}{}
	s.mu.Unlock()
	return sb.c, func() {
		s.mu.Lock()
		if _, ok := s.subs[sb]; ok {
			delete(s.subs, sb)
			close(sb.c)
		}
		s.mu.Unlock()
	}
}

func (s *Store) fanout(l Line) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sb := range s.subs {
		if sb.f.match(l) {
			select {
			case sb.c <- l:
			default: // slow reader: drop
			}
		}
	}
}

// ── storage ─────────────────────────────────────────────────────────────────

// Run writes queued lines to VictoriaLogs in batches until ctx ends.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	var batch []Line
	lastWarn := time.Time{}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.write(ctx, batch); err != nil {
			s.failed.Add(uint64(len(batch)))
			if time.Since(lastWarn) > time.Minute {
				s.log.Warn("cannot write logs to VictoriaLogs; lines are dropped", "err", err, "dropped_total", s.failed.Load())
				lastWarn = time.Now()
			}
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-s.queue:
			batch = append(batch, l)
			if len(batch) >= 2000 {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

func (s *Store) write(ctx context.Context, lines []Line) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range lines {
		msg := l.Message
		if msg == "" {
			msg = " " // VictoriaLogs drops records with an empty message
		}
		r := map[string]string{"_time": l.Time.Format(time.RFC3339Nano), "_msg": msg, "project": l.Project, "environment": l.Environment,
			"service": l.Service, "task_id": l.TaskID, "node": l.Node, "stream": l.Stream}
		if l.Revision != "" {
			r["revision"] = l.Revision
		}
		if l.Level != "" {
			r["level"] = l.Level
		}
		for k, v := range l.Fields {
			if _, fixed := r[k]; !fixed && v != "" {
				r[k] = v
			}
		}
		_ = enc.Encode(r)
	}
	q := url.Values{"_stream_fields": {"project,environment,service,task_id,node,stream"}, "_time_field": {"_time"}, "_msg_field": {"_msg"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.vlURL+"/insert/jsonline?"+q.Encode(), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/stream+json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("VictoriaLogs: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	return nil
}

// quote makes a LogsQL string literal; user input is only ever used this way.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// LogsQL builds the query for f over the last `since`.
func (f Filter) LogsQL(since time.Duration) string {
	parts := []string{fmt.Sprintf("_time:%ds", int(since.Seconds()))}
	for _, kv := range [][2]string{{"project", f.Project}, {"environment", f.Environment}, {"service", f.Service}, {"task_id", f.TaskID}, {"node", f.Node}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+":="+quote(kv[1]))
		}
	}
	if f.Text != "" {
		parts = append(parts, "i("+quote(f.Text)+")")
	}
	if f.Stream == "" {
		parts = append(parts, "-stream:in("+quote(StreamAccess)+", "+quote(StreamFirewall)+")")
	} else {
		parts = append(parts, "stream:="+quote(f.Stream))
	}
	if f.Status != "" {
		parts = append(parts, "status:~"+quote("^"+regexp.QuoteMeta(f.Status)))
	}
	if f.Client != "" {
		parts = append(parts, "client:="+quote(f.Client))
	}
	return strings.Join(parts, " ")
}

// ErrUnavailable means VictoriaLogs could not be queried.
var ErrUnavailable = errors.New("log store unavailable")

// queryRows runs a LogsQL query and returns its rows as field maps.
func (s *Store) queryRows(ctx context.Context, q string) ([]map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.vlURL+"/select/logsql/query", strings.NewReader(url.Values{"query": {q}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrUnavailable, resp.StatusCode, bytes.TrimSpace(b))
	}
	var rows []map[string]string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 4<<20)
	for sc.Scan() {
		var r map[string]string
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			rows = append(rows, r)
		}
	}
	return rows, sc.Err()
}

// Query returns up to limit lines (newest last) matching f within since.
func (s *Store) Query(ctx context.Context, f Filter, since time.Duration, limit int) ([]Line, error) {
	rows, err := s.queryRows(ctx, f.LogsQL(since)+fmt.Sprintf(" | sort by (_time desc) | limit %d", limit))
	if err != nil {
		return nil, err
	}
	out := make([]Line, 0, len(rows))
	for _, r := range rows {
		t, _ := time.Parse(time.RFC3339Nano, r["_time"])
		l := Line{Time: t, Project: r["project"], Environment: r["environment"], Service: r["service"], TaskID: r["task_id"],
			Revision: r["revision"], Node: r["node"], Stream: r["stream"], Level: r["level"], Message: r["_msg"]}
		if fields := streamFields[l.Stream]; fields != nil {
			l.Fields = map[string]string{}
			for _, k := range fields {
				if v := r[k]; v != "" {
					l.Fields[k] = v
				}
			}
		}
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b Line) int { return a.Time.Compare(b.Time) })
	return out, nil
}
