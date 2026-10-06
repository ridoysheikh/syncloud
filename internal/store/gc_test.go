package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestGC(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-500 * 24 * time.Hour)
	if err := s.CreateProject(ctx, Project{ID: "prj_1", Name: "shop", CreatedAt: now}, Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateService(ctx, Service{ID: "svc_1", EnvironmentID: "env_1", Name: "web", DesiredCount: 1, CreatedAt: now}, `{"v":1}`, "test"); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 60; i++ {
		if _, err := s.UpdateService(ctx, "svc_1", fmt.Sprintf(`{"v":%d}`, i), 1, "test", now); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A task still on revision 3 keeps it.
	exec(`INSERT INTO tasks (id, service_id, revision, desired, state, created_at, updated_at) VALUES ('t1', 'svc_1', 3, 'running', 'running', ?, ?)`, now.Unix(), now.Unix())
	exec(`INSERT INTO audit_events (at, action, resource, ip, user_agent) VALUES (?, 'a', 'r', '', ''), (?, 'b', 'r', '', '')`, old.Unix(), now.Unix())
	exec(`INSERT INTO incidents (id, service_id, state, cause, opened_at, closed_at) VALUES ('i1', 'svc_1', 'down', 'x', ?, ?), ('i2', 'svc_1', 'down', 'x', ?, NULL)`, old.Unix(), old.Unix(), old.Unix())
	exec(`INSERT INTO usage_daily (project, environment, day) VALUES ('shop', 'production', ?), ('shop', 'production', ?)`, old.Format("2006-01-02"), now.Format("2006-01-02"))

	res, err := s.GC(ctx, DefaultRetention, now)
	if err != nil {
		t.Fatal(err)
	}
	if res["audit_events"] != 1 || res["incidents"] != 1 || res["usage_daily"] != 1 {
		t.Fatalf("GC = %v", res)
	}
	var revs []int
	rows, err := s.W.QueryContext(ctx, `SELECT revision FROM task_definitions WHERE service_id = 'svc_1' ORDER BY revision`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r int
		_ = rows.Scan(&r)
		revs = append(revs, r)
	}
	// Revisions 11–60 (the newest 50), plus 3 (a task runs it).
	if len(revs) != 51 || revs[0] != 3 || revs[1] != 11 || revs[len(revs)-1] != 60 {
		t.Fatalf("revisions kept: %v", revs)
	}
}
