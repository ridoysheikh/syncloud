package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/ingress"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// While a project moves from the controller to its own nodes, its old
// generated address keeps answering on the controller as a twin route.
func TestIngressRoutesDuringAMove(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, store.Service{ID: "svc_1", EnvironmentID: "env_1", Name: "web", Revision: 1, CreatedAt: now, UpdatedAt: now}, `{"image":"nginx"}`, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDomain(ctx, store.Domain{ID: "dom_1", ServiceID: "svc_1", Host: "shop.example.com", PortName: "http", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ing := ingress.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ing.NodeAddrs = func(context.Context) (map[string]string, error) { return map[string]string{"lb1": "198.51.100.1"}, nil }
	ing.Resolve = func(context.Context, string) ([]string, error) { return []string{"203.0.113.1"}, nil }
	if _, err := ing.Set(ctx, "prj_1", []string{"lb1"}); err != nil {
		t.Fatal(err)
	}
	base := "203-0-113-1.sslip.io"
	routes := []workload.Route{
		{Name: "svc-svc_1-http", Host: "web-production-shop.198-51-100-1.sslip.io", Project: "shop", Generated: true, ServiceID: "svc_1"},
		{Name: "dom-1", Host: "shop.example.com", Project: "shop", ServiceID: "svc_1"},
	}
	got := map[string]string{}
	for _, r := range ingressRoutes(routes, ing, base, noChain) {
		got[r.Name] = r.Host
		switch r.Name {
		case "svc-svc_1-http":
			if r.Ingress.Default || !r.Ingress.Served("lb1", false) {
				t.Errorf("new address served by %+v", r.Ingress)
			}
		case "svc-svc_1-http_prev":
			if !r.Ingress.Default || r.Ingress.Served("lb1", false) {
				t.Errorf("old address served by %+v", r.Ingress)
			}
		case "dom-1":
			if !r.Ingress.Default || !r.Ingress.Served("lb1", false) {
				t.Errorf("domain served by %+v", r.Ingress)
			}
		}
	}
	if got["svc-svc_1-http_prev"] != "web-production-shop."+base {
		t.Errorf("routes %v", got)
	}
}
