package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// Migration 00047 rebuilds service_routing without UNIQUE (public_port):
// existing rows keep their values and become automatic ports.
func TestPublicPortsMigrationKeepsRouting(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	dsn := "file:" + path + "?" + url.Values{"_pragma": {"foreign_keys(ON)"}}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, mustSub(migrations, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 46); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, created_at) VALUES ('prj_1', 'shop', 0)`,
		`INSERT INTO environments (id, project_id, name, created_at) VALUES ('env_1', 'prj_1', 'production', 0)`,
		`INSERT INTO services (id, environment_id, name, revision, desired_count, held, created_at, updated_at) VALUES ('svc_1', 'env_1', 'mail', 1, 1, 0, 0, 0)`,
		`INSERT INTO service_routing (service_id, port_name, generated, label, public_port, allow) VALUES ('svc_1', 'smtp', 1, NULL, 20000, '["10.0.0.0/8"]')`,
		`INSERT INTO service_routing (service_id, port_name, generated, label, public_port, allow) VALUES ('svc_1', 'http', 1, 'mail', NULL, '[]')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rs, err := st.ListRouting(ctx, "svc_1")
	if err != nil || len(rs) != 2 {
		t.Fatalf("routing %+v %v", rs, err)
	}
	for _, r := range rs {
		switch r.PortName {
		case "smtp":
			if r.PublicPort != 20000 || r.PortMode != PortAuto || r.ProxyProtocol || r.TLS != "" || len(r.Allow) != 1 {
				t.Errorf("smtp %+v", r)
			}
		case "http":
			if r.Label != "mail" || r.PublicPort != 0 {
				t.Errorf("http %+v", r)
			}
		}
	}
	// The port table refuses a second holder on a node, and keeps when a
	// port was first claimed.
	first := time.Now().Add(-time.Hour).UTC()
	if err := st.ReplaceOwnerClaims(ctx, PortOwnerService, "svc_1", []PortClaim{{Node: "ctl-0", Protocol: "tcp", Port: 25, PortName: "smtp", CreatedAt: first}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceOwnerClaims(ctx, PortOwnerService, "svc_2", []PortClaim{{Node: "ctl-0", Protocol: "tcp", Port: 25, PortName: "smtp"}}); !errors.Is(err, ErrPortTaken) {
		t.Fatalf("second holder: %v", err)
	}
	if err := st.ReplaceOwnerClaims(ctx, PortOwnerService, "svc_2", []PortClaim{{Node: "n1", Protocol: "tcp", Port: 25, PortName: "smtp"}, {Node: "ctl-0", Protocol: "udp", Port: 25, PortName: "dns"}}); err != nil {
		t.Fatalf("another node and protocol: %v", err)
	}
	if err := st.ReplaceOwnerClaims(ctx, PortOwnerService, "svc_1", []PortClaim{{Node: "ctl-0", Protocol: "tcp", Port: 25, PortName: "smtp"}}); err != nil {
		t.Fatal(err)
	}
	claims, _ := st.ListPortClaims(ctx)
	for _, c := range claims {
		if c.OwnerID == "svc_1" && !c.CreatedAt.Equal(first) {
			t.Errorf("first claim time lost: %v, want %v", c.CreatedAt, first)
		}
	}
	skipped, err := st.ReplaceAllPortClaims(ctx, []PortClaim{
		{Node: "ctl-0", Protocol: "tcp", Port: 25, OwnerKind: PortOwnerService, OwnerID: "svc_1", PortName: "smtp"},
		{Node: "ctl-0", Protocol: "tcp", Port: 25, OwnerKind: PortOwnerService, OwnerID: "svc_2", PortName: "smtp"},
	})
	if err != nil || len(skipped) != 1 || skipped[0].OwnerID != "svc_2" {
		t.Fatalf("resync clash %+v %v", skipped, err)
	}
}
