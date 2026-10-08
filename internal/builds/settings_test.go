package builds

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestBuildSettings(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	env := store.Environment{ID: "env_1", ProjectID: "prj_1", Name: "production", CreatedAt: now}
	if err := st.CreateProject(ctx, store.Project{ID: "prj_1", Name: "shop", CreatedAt: now}, env); err != nil {
		t.Fatal(err)
	}
	sv := store.Service{ID: "svc_1", EnvironmentID: env.ID, Name: "web", DesiredCount: 1, CreatedAt: now}
	if err := st.CreateService(ctx, sv, `{"image":"x"}`, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGitSource(ctx, store.GitSource{ID: "git_1", ServiceID: sv.ID, URL: "https://git.example.com/a/b.git", Branch: "main", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.New(make([]byte, 32))
	m := &Manager{st: st, box: box, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	str := func(s string) *string { return &s }

	out, err := m.SetSettings(ctx, sv, SettingsInput{BuildCommand: " npm run build ", PostBuild: []string{"npm test", " ", "./smoke.sh"},
		Variables: map[string]*string{"NPM_TOKEN": str("s3cret"), "NODE_ENV": str("production")}})
	if err != nil {
		t.Fatal(err)
	}
	if out.BuildCommand != "npm run build" || !slices.Equal(out.PostBuild, []string{"npm test", "./smoke.sh"}) ||
		!slices.Equal(out.Variables, []string{"NODE_ENV", "NPM_TOKEN"}) {
		t.Fatalf("settings %+v", out)
	}
	g, _ := st.GitSourceByService(ctx, sv.ID)
	if strings.Contains(g.BuildSettings, "s3cret") || strings.Contains(string(g.BuildVarsEnc), "s3cret") {
		t.Fatal("a build variable is stored in the clear")
	}
	envs := map[string]string{}
	m.settingsEnv(g, envs)
	if envs["NIXPACKS_BUILD_CMD"] != "npm run build" || envs["BUILD_VARS"] != "NODE_ENV=production\nNPM_TOKEN=s3cret" || envs["NIXPACKS_START_CMD"] != "" {
		t.Fatalf("build env %v", envs)
	}

	// null keeps a value, left out removes it.
	if out, err = m.SetSettings(ctx, sv, SettingsInput{Variables: map[string]*string{"NPM_TOKEN": nil}}); err != nil {
		t.Fatal(err)
	}
	g, _ = st.GitSourceByService(ctx, sv.ID)
	if vars := m.buildVars(g); len(vars) != 1 || vars["NPM_TOKEN"] != "s3cret" || len(out.PostBuild) != 0 {
		t.Fatalf("after keep: %v %+v", vars, out)
	}

	for _, bad := range []SettingsInput{
		{InstallCommand: "a\nb"},
		{Variables: map[string]*string{"1BAD": str("x")}},
		{Variables: map[string]*string{"MISSING": nil}},
		{Variables: map[string]*string{"A": str("line\nbreak")}},
	} {
		if _, err := m.SetSettings(ctx, sv, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	if s := checkScript([]string{"npm test", "./smoke.sh"}); !strings.HasPrefix(s, "set -e\n") || !strings.Contains(s, "npm test\n") ||
		strings.Index(s, "npm test") > strings.Index(s, "./smoke.sh") {
		t.Fatalf("check script %q", s)
	}
}
