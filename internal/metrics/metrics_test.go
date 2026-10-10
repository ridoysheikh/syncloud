package metrics

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// A fake VictoriaMetrics: records imports and answers range queries.
func TestWriteAndQuery(t *testing.T) {
	var mu sync.Mutex
	var imported string
	var queries []string
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/import/prometheus":
			b, _ := io.ReadAll(r.Body)
			imported += string(b)
		case "/api/v1/query_range":
			queries = append(queries, r.URL.Query().Get("query"))
			json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{
				map[string]any{"metric": map[string]string{"task": "task_b", "node": "w1"}, "values": [][2]any{{1700000000.0, "12.5"}, {1700000010.0, "NaN"}}},
				map[string]any{"metric": map[string]string{"task": "task_a", "node": "w2"}, "values": [][2]any{{1700000000.0, "3"}}},
			}}})
		}
	}))
	defer vm.Close()

	s := New(vm.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	s.Add("w1", []*agentv1.TaskMetrics{{TaskId: "task_a", ServiceId: "svc_1", Project: "shop", Environment: "production", Service: "web",
		AtUnixMs: 1700000000000, CpuPercent: 12.5, MemoryBytes: 1 << 20, NetRxBytes: 42}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		got := imported
		mu.Unlock()
		if got != "" {
			want := `syncloud_task_cpu_percent{task="task_a",service_id="svc_1",project="shop",environment="production",service="web",node="w1"} 12.5 1700000000000`
			if !strings.Contains(got, want) || !strings.Contains(got, "syncloud_task_net_rx_bytes_total{") {
				t.Fatalf("import body:\n%s", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("samples were not written")
		}
		time.Sleep(50 * time.Millisecond)
	}

	res, err := s.Query(ctx, Scope{ServiceID: "svc_1"}, time.Hour, time.Unix(1700000100, 0))
	if err != nil {
		t.Fatal(err)
	}
	cpu := res.Charts["cpu"]
	if len(res.Charts) != 6 || len(cpu) != 2 || cpu[0].Key != "task_a" || cpu[1].Node != "w1" {
		t.Fatalf("charts: %+v", res.Charts)
	}
	if len(cpu[1].Points) != 1 || cpu[1].Points[0] != [2]float64{1700000000000, 12.5} {
		t.Fatalf("points (NaN dropped): %+v", cpu[1].Points)
	}
	if res.Step != 30 {
		t.Fatalf("step %d", res.Step)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, q := range queries {
		if !strings.Contains(q, `service_id="svc_1"`) || !strings.Contains(q, "by (task, node)") {
			t.Fatalf("query %q", q)
		}
	}

	if _, err := New("", nil).Query(ctx, Scope{}, time.Hour, time.Now()); err != ErrDisabled {
		t.Fatalf("disabled store: %v", err)
	}
}

func TestRelabelTraefik(t *testing.T) {
	in := `# HELP traefik_service_requests_total How many HTTP requests processed on a service.
traefik_service_requests_total{code="200",method="GET",protocol="http",service="svc-svc_ab12-http@http"} 7
traefik_service_requests_total{code="200",method="GET",protocol="http",service="svc-svc_gone-http@http"} 3
traefik_service_requests_total{code="200",method="GET",protocol="http",service="syncloud-controller@http"} 2
traefik_router_requests_total{code="404",method="GET",protocol="http",router="svc-svc_ab12-http@http",service="svc-svc_ab12-http@http"} 1
traefik_service_requests_total{code="200",method="GET",protocol="http",service="dom-xy34@http"} 5
traefik_service_requests_total{code="200",method="GET",protocol="http",service="svc-svc_ab12-http_probe@http"} 240
traefik_router_requests_total{code="200",method="GET",protocol="http",router="svc-svc_ab12-http_probe@http",service="svc-svc_ab12-http_probe@http"} 240
traefik_config_reloads_total 4
go_goroutines 40
`
	web := ServiceName{ID: "svc_ab12", Project: "shop", Environment: "production", Name: "web"}
	out, err := Relabel(strings.NewReader(in), map[string]ServiceName{"svc_ab12": web, "dom-xy34": web})
	if err != nil {
		t.Fatal(err)
	}
	want := `traefik_service_requests_total{service_id="svc_ab12",project="shop",environment="production",app="web",code="200",method="GET",protocol="http",service="svc-svc_ab12-http@http"} 7
traefik_service_requests_total{project="syncloud",environment="system",app="controller",code="200",method="GET",protocol="http",service="syncloud-controller@http"} 2
traefik_router_requests_total{service_id="svc_ab12",project="shop",environment="production",app="web",code="404",method="GET",protocol="http",router="svc-svc_ab12-http@http",service="svc-svc_ab12-http@http"} 1
traefik_service_requests_total{service_id="svc_ab12",project="shop",environment="production",app="web",code="200",method="GET",protocol="http",service="dom-xy34@http"} 5
`
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}
