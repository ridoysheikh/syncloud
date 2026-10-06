package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TraefikTaskID is the edge proxy's system task; its JSON access log becomes
// request lines of the services it routes to (§5.7).
const TraefikTaskID = "sys-traefik"

var accessFields = []string{"method", "host", "path", "status", "duration_ms", "bytes", "client", "upstream", "service_id"}

// Traefik names a service's routers and load balancers
// "svc-<service ID>-<port>" (see workload.Routes).
var routerRE = regexp.MustCompile(`^svc-(svc_[a-z0-9]+)-`)

// traefikAccess is the part of Traefik's JSON access log we keep.
type traefikAccess struct {
	ClientHost            string `json:"ClientHost"`
	DownstreamStatus      int    `json:"DownstreamStatus"`
	DownstreamContentSize int64  `json:"DownstreamContentSize"`
	Duration              int64  `json:"Duration"` // nanoseconds
	RequestMethod         string `json:"RequestMethod"`
	RequestHost           string `json:"RequestHost"`
	RequestPath           string `json:"RequestPath"`
	RouterName            string `json:"RouterName"`
	ServiceURL            string `json:"ServiceURL"`
	StartUTC              string `json:"StartUTC"`
}

// ServiceIDOfRouter returns the SynCloud service ID in a Traefik router or
// service name ("svc-svc_ab12-http@http"), or "".
func ServiceIDOfRouter(name string) string {
	if m := routerRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// accessLine turns one Traefik access log line into a request line labelled
// with the service it reached. Other Traefik output returns ok=false.
func (s *Store) accessLine(node, raw string) (Line, bool) {
	if !strings.HasPrefix(raw, "{") || !strings.Contains(raw, `"DownstreamStatus"`) {
		return Line{}, false
	}
	var a traefikAccess
	if err := json.Unmarshal([]byte(raw), &a); err != nil || a.RequestMethod == "" {
		return Line{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, a.StartUTC)
	if err != nil {
		t = time.Now()
	}
	l := Line{Time: t.UTC(), TaskID: TraefikTaskID, Node: node, Stream: StreamAccess}
	id := ServiceIDOfRouter(a.RouterName)
	switch {
	case id != "":
		lb := s.labelsForService(id)
		l.Project, l.Environment, l.Service = lb.project, lb.environment, lb.service
	case a.RouterName != "":
		name := strings.TrimPrefix(strings.SplitN(a.RouterName, "@", 2)[0], "syncloud-")
		l.Project, l.Environment, l.Service = "syncloud", "system", name
	default:
		l.Project, l.Environment, l.Service = "syncloud", "system", "unrouted"
	}
	ms := float64(a.Duration) / 1e6
	upstream := strings.TrimPrefix(strings.TrimPrefix(a.ServiceURL, "http://"), "https://")
	l.Fields = map[string]string{
		"method": a.RequestMethod, "host": a.RequestHost, "path": a.RequestPath, "status": strconv.Itoa(a.DownstreamStatus),
		"duration_ms": strconv.FormatFloat(ms, 'f', 1, 64), "bytes": strconv.FormatInt(a.DownstreamContentSize, 10),
		"client": a.ClientHost, "upstream": upstream, "service_id": id,
	}
	for k, v := range l.Fields {
		if v == "" {
			delete(l.Fields, k)
		}
	}
	l.Message = fmt.Sprintf("%s %s%s %d %.0fms %dB %s", a.RequestMethod, a.RequestHost, a.RequestPath, a.DownstreamStatus, ms, a.DownstreamContentSize, a.ClientHost)
	if a.DownstreamStatus >= 500 {
		l.Level = "error"
	} else if a.DownstreamStatus >= 400 {
		l.Level = "warn"
	}
	return l, true
}

// labelsForService looks up (and caches) a service's names by ID.
func (s *Store) labelsForService(id string) labels {
	key := "service:" + id
	s.mu.Lock()
	lb, ok := s.cache[key]
	s.mu.Unlock()
	if ok {
		return lb
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sv, err := s.st.ServiceByID(ctx, id)
	if err != nil {
		return labels{project: "syncloud", environment: "system", service: "unknown"}
	}
	lb = labels{project: sv.Project, environment: sv.Environment, service: sv.Name}
	s.mu.Lock()
	s.cache[key] = lb
	s.mu.Unlock()
	return lb
}

// UpstreamCount is how many requests one task (upstream address) of a
// service answered.
type UpstreamCount struct {
	ServiceID string `json:"serviceId"`
	Upstream  string `json:"upstream"`
	Requests  int    `json:"requests"`
}

// UpstreamCounts counts request lines per service and task over the last
// window: Traefik's metrics stop at the service, the access log names the
// task that answered.
func (s *Store) UpstreamCounts(ctx context.Context, window time.Duration) ([]UpstreamCount, error) {
	q := fmt.Sprintf(`_time:%ds stream:=%s service_id:* | stats by (service_id, upstream) count() as n`, int(window.Seconds()), quote(StreamAccess))
	rows, err := s.queryRows(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]UpstreamCount, 0, len(rows))
	for _, r := range rows {
		n, _ := strconv.Atoi(r["n"])
		if r["service_id"] != "" {
			out = append(out, UpstreamCount{ServiceID: r["service_id"], Upstream: r["upstream"], Requests: n})
		}
	}
	return out, nil
}
