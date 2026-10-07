package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStandaloneAndProjectDatabases(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.CreateProject(ctx, Project{ID: "prj_1", Name: "shop", CreatedAt: now}, Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mk := func(id, env, name string) error {
		return s.CreateDatabase(ctx, Database{ID: id, EnvironmentID: env, Name: name, Engine: "valkey", Version: "8.1", Spec: "{}", State: "{}", Secrets: []byte("x"), CreatedAt: now})
	}
	if err := mk("db_1", "", "sessions"); err != nil {
		t.Fatal(err)
	}
	if err := mk("db_2", "env_1", "cache"); err != nil {
		t.Fatal(err)
	}
	// Names are unique in the cluster, whatever the owner.
	if err := mk("db_3", "env_1", "sessions"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name in a project: %v", err)
	}
	if err := mk("db_4", "", "cache"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate standalone name: %v", err)
	}
	sa, err := s.DatabaseByName(ctx, "sessions")
	if err != nil || !sa.Standalone() || sa.Project != "" || sa.Network != "{}" {
		t.Fatalf("standalone: %+v %v", sa, err)
	}
	pd, err := s.DatabaseByName(ctx, "cache")
	if err != nil || pd.Standalone() || pd.Project != "shop" || pd.Environment != "production" {
		t.Fatalf("project database: %+v %v", pd, err)
	}
	if err := s.SetDatabaseNetwork(ctx, "db_1", `{"access":["project:shop"],"public":{"enabled":true,"allow":["0.0.0.0/0"]}}`, now); err != nil {
		t.Fatal(err)
	}
	sa, _ = s.DatabaseByID(ctx, "db_1")
	if n := sa.ParseNetwork(); !n.Public.Enabled || len(n.Access) != 1 {
		t.Errorf("network: %+v", n)
	}
	all, err := s.ListDatabases(ctx)
	if err != nil || len(all) != 2 {
		t.Errorf("list: %d %v", len(all), err)
	}
	// Deleting the project removes its databases, not standalone ones.
	if _, err := s.W.ExecContext(ctx, `DELETE FROM projects WHERE id = 'prj_1'`); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.ListDatabases(ctx); len(all) != 1 || all[0].Name != "sessions" {
		t.Errorf("after project delete: %+v", all)
	}
}
