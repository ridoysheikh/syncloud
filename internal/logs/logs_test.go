package logs

import (
	"testing"
	"time"
)

func TestLogsQLQuotesUserInput(t *testing.T) {
	f := Filter{Project: "shop", Service: `web") OR (*`, Text: `a" | delete`}
	got := f.LogsQL(time.Hour)
	want := `_time:3600s project:="shop" service:="web\") OR (*" i("a\" | delete")`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
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
