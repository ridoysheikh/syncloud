package logs

import (
	"testing"
	"time"
)

func TestLogsQLQuotesUserInput(t *testing.T) {
	f := Filter{Project: "shop", Service: `web") OR (*`, Text: `a" | delete`}
	got := f.LogsQL(time.Hour)
	want := `_time:3600s project:="shop" service:="web\") OR (*" i("a\" | delete") -stream:in("access", "firewall")`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	f = Filter{Service: "web", Stream: StreamAccess, Status: "5", Client: "1.2.3.4"}
	want = `_time:3600s service:="web" stream:="access" status:~"^5" client:="1.2.3.4"`
	if got := f.LogsQL(time.Hour); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestAccessLine(t *testing.T) {
	s := New(nil, "http://x", nil)
	s.cache["service:svc_ab12"] = labels{project: "shop", environment: "production", service: "web"}
	raw := `{"ClientHost":"203.0.113.9","DownstreamContentSize":512,"DownstreamStatus":502,"Duration":12500000,"RequestHost":"web.example.com",` +
		`"RequestMethod":"GET","RequestPath":"/api?x=1","RouterName":"svc-svc_ab12-http@http","ServiceURL":"http://10.92.0.5:8080","StartUTC":"2026-10-06T10:00:00.5Z"}`
	l, ok := s.accessLine("ctl-0", raw)
	if !ok {
		t.Fatal("not parsed")
	}
	if l.Project != "shop" || l.Service != "web" || l.Stream != StreamAccess || l.Level != "error" || l.Node != "ctl-0" {
		t.Errorf("labels: %+v", l)
	}
	for k, want := range map[string]string{"status": "502", "duration_ms": "12.5", "upstream": "10.92.0.5:8080", "client": "203.0.113.9", "service_id": "svc_ab12", "path": "/api?x=1"} {
		if l.Fields[k] != want {
			t.Errorf("%s = %q, want %q", k, l.Fields[k], want)
		}
	}
	if !l.Time.Equal(time.Date(2026, 10, 6, 10, 0, 0, 5e8, time.UTC)) {
		t.Errorf("time %v", l.Time)
	}
	if l, _ := s.accessLine("ctl-0", `{"RequestMethod":"GET","DownstreamStatus":404,"RouterName":"syncloud-dashboard@http"}`); l.Service != "dashboard" || l.Project != "syncloud" {
		t.Errorf("system router: %+v", l)
	}
	if _, ok := s.accessLine("ctl-0", `time="..." level=info msg="Configuration loaded"`); ok {
		t.Error("a Traefik log line was taken for a request")
	}
	// Request lines stay out of application logs unless asked for.
	if (Filter{}).match(l) || !(Filter{Stream: StreamAccess, Status: "5"}).match(l) || (Filter{Stream: StreamAccess, Status: "4"}).match(l) {
		t.Error("stream and status filters")
	}
}

func TestDetectLevel(t *testing.T) {
	cases := map[string]string{
		`{"level":"WARN","msg":"x"}`:               "warn",
		`{"severity":"error"}`:                     "error",
		"2026/10/06 12:00:00 [error] boom":         "error",
		"INFO server started":                      "info",
		"GET / 200":                                "",
		"panic: runtime error: index out of range": "fatal",
	}
	for in, want := range cases {
		if got := detectLevel(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestTailFilter(t *testing.T) {
	s := New(nil, "http://x", nil)
	c, cancel := s.Tail(Filter{Service: "web", Text: "HELLO"})
	defer cancel()
	s.fanout(Line{Service: "api", Message: "hello"})
	s.fanout(Line{Service: "web", Message: "say hello"})
	s.fanout(Line{Service: "web", Message: "bye"})
	select {
	case l := <-c:
		if l.Message != "say hello" {
			t.Fatal(l)
		}
	default:
		t.Fatal("no line")
	}
	select {
	case l := <-c:
		t.Fatalf("unexpected %v", l)
	default:
	}
}
