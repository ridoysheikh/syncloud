package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Cursor pages (§14) walk a history list newest first without gaps or
// repeats, also when rows share a timestamp.
func TestCursorPages(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.CreateProject(ctx, Project{ID: "prj_1", Name: "shop", CreatedAt: now}, Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateService(ctx, Service{ID: "svc_1", EnvironmentID: "env_1", Name: "web", DesiredCount: 1, CreatedAt: now}, `{"v":1}`, "test"); err != nil {
		t.Fatal(err)
	}
	// 7 builds, three of them in the same second.
	for i := range 7 {
		at := now.Add(time.Duration(min(i, 4)) * time.Second)
		if err := s.CreateBuild(ctx, Build{ID: fmt.Sprintf("bld_%d", i), ServiceID: "svc_1", SHA: fmt.Sprint(i), Ref: "main", Trigger: "manual", Status: "queued", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	before := ""
	for range 10 {
		page, err := s.ServiceBuilds(ctx, "svc_1", 3, before)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range page {
			got = append(got, b.ID)
		}
		if len(page) < 3 {
			break
		}
		before = page[len(page)-1].ID
	}
	if want := "[bld_6 bld_5 bld_4 bld_3 bld_2 bld_1 bld_0]"; fmt.Sprint(got) != want {
		t.Fatalf("builds by page: %v, want %s", got, want)
	}

	if err := s.CreateDatabase(ctx, Database{ID: "db_1", EnvironmentID: "env_1", Name: "cache", Engine: "valkey", Version: "8.1", Spec: "{}", State: "{}", Secrets: []byte("x"), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if err := s.AddDatabaseEvent(ctx, DatabaseEvent{DatabaseID: "db_1", At: now, Kind: "k", To: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := s.DatabaseEvents(ctx, "db_1", 2, "")
	next, _ := s.DatabaseEvents(ctx, "db_1", 2, fmt.Sprint(first[1].ID))
	last, _ := s.DatabaseEvents(ctx, "db_1", 2, fmt.Sprint(next[1].ID))
	if first[0].To != "4" || first[1].To != "3" || next[0].To != "2" || next[1].To != "1" || len(last) != 1 || last[0].To != "0" {
		t.Fatalf("event pages: %v %v %v", first, next, last)
	}
}
