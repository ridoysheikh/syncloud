package workload

import (
	"reflect"
	"testing"
)

func TestDiff(t *testing.T) {
	a := Spec{Image: "app:1", Env: map[string]string{"A": "1", "B": "2", "C": "3"}, Resources: Resources{CPU: 0.1, Memory: 128},
		Ports: []Port{{Name: "http", Container: 80, Protocol: "http"}}}
	b := Spec{Image: "app:2", Env: map[string]string{"A": "1", "B": "x", "D": "4"}, Resources: Resources{CPU: 0.5, Memory: 128},
		Ports: []Port{{Name: "http", Container: 8080, Protocol: "http"}}, RedeployedAt: "2026-10-08T00:00:00Z"}
	got := Diff(a, b)
	want := []Change{
		{Field: "image", From: "app:1", To: "app:2"},
		{Field: "variable B", From: "changed", To: "changed"},
		{Field: "variable C", From: "removed"},
		{Field: "variable D", To: "added"},
		{Field: "ports", From: "http:80/http", To: "http:8080/http"},
		{Field: "cpu", From: "0.1", To: "0.5"},
		{Field: "redeploy", To: "2026-10-08T00:00:00Z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Diff:\n got %+v\nwant %+v", got, want)
	}
	for _, c := range got {
		if c.From == "2" || c.To == "x" || c.To == "4" {
			t.Fatalf("a variable value leaked: %+v", c)
		}
	}
	h := Spec{Health: &HealthCheck{Type: "http", Path: "/ok", Interval: 2}}
	h2 := Spec{Health: &HealthCheck{Type: "http", Path: "/missing", Interval: 2}}
	if got := Diff(h, h2); !reflect.DeepEqual(got, []Change{{Field: "health check path", From: "/ok", To: "/missing"}}) {
		t.Fatalf("health check diff %+v", got)
	}
	if got := Diff(Spec{}, h); len(got) != 1 || got[0].Field != "health check" || got[0].From != "" {
		t.Fatalf("added health check %+v", got)
	}
	if d := Diff(a, a); len(d) != 0 {
		t.Fatalf("Diff of equal specs: %+v", d)
	}
}
