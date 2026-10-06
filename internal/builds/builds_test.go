package builds

import (
	"context"
	"strings"
	"testing"
	"time"

	"syncloud/internal/registry"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

func TestBuildSpec(t *testing.T) {
	issuer, err := registry.LoadOrCreateIssuer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.New(make([]byte, 32))
	m := &Manager{box: box, issuer: issuer, now: time.Now, cfg: Config{
		RegistryHost: func() string { return "registry.example.com" }, Node: "ctl-0",
	}}
	sv := store.Service{ID: "svc_1", Project: "shop", Name: "web"}
	g := store.GitSource{ServiceID: "svc_1", URL: "https://git.example.com/acme/web.git", Branch: "feature/x", Dockerfile: "Dockerfile", Context: "app",
		TokenEnc: box.Seal([]byte("tok"), []byte("git:svc_1"))}
	g.Paths = []string{"app/**", "!app/docs/**"}
	sha := strings.Repeat("ab", 20)
	spec, err := m.buildSpec(context.Background(), sv, g, store.Build{SHA: sha, Ref: "refs/heads/feature/x", BaseSHA: "base"})
	if err != nil {
		t.Fatal(err)
	}
	env := spec.Env
	for k, want := range map[string]string{
		"GIT_URL": g.URL, "GIT_SHA": sha, "GIT_REF": "refs/heads/feature/x", "BASE_SHA": "base", "CONTEXT_DIR": "app",
		"DOCKERFILE": "Dockerfile", "BUILDER": "auto", "WATCH_PATHS": "app/**\n!app/docs/**", "GIT_TOKEN": "tok",
		"OUTPUT": `type=image,"name=registry.example.com/shop/web:` + sha[:12] + `,registry.example.com/shop/web:latest-feature-x",push=true`,
		"CACHE":  "type=registry,ref=registry.example.com/shop/web:buildcache",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if !strings.Contains(env["BUILD_REGISTRY_AUTH"], `"registry.example.com"`) {
		t.Errorf("registry auth: %s", env["BUILD_REGISTRY_AUTH"])
	}
	if spec.Placement.Node != "ctl-0" || spec.Image != BuildKitImage || !strings.Contains(spec.Entrypoint[2], "buildctl-daemonless.sh") {
		t.Errorf("spec: %+v", spec)
	}
	tagged, err := m.buildSpec(context.Background(), sv, g, store.Build{SHA: sha, Ref: "refs/tags/v1.2.0"})
	if err != nil || !strings.Contains(tagged.Env["OUTPUT"], "registry.example.com/shop/web:v1.2.0\"") {
		t.Errorf("tag build output: %s %v", tagged.Env["OUTPUT"], err)
	}

	m.cfg.RegistryHost = func() string { return "" }
	if _, err := m.buildSpec(context.Background(), sv, g, store.Build{SHA: sha}); err == nil {
		t.Error("built without a registry host")
	}
}

func TestSetSourceValidation(t *testing.T) {
	m := &Manager{now: time.Now}
	for _, in := range []Source{
		{URL: "git@github.com:acme/web.git"},
		{URL: "https://user:pw@github.com/acme/web.git"},
		{URL: "https://github.com/acme/web.git", Branch: "main; rm -rf /"},
		{URL: "https://github.com/acme/web.git", Context: "../etc"},
		{URL: "https://github.com/acme/web.git", PollSeconds: 5},
		{URL: "https://github.com/acme/web.git", Tags: "v* x"},
		{URL: "https://github.com/acme/web.git", Paths: []string{"a b"}},
		{URL: "https://github.com/acme/web.git", Paths: []string{"../x/**"}},
		{URL: "https://github.com/acme/web.git", Builder: "buildpacks"},
	} {
		if _, err := m.SetSource(t.Context(), store.Service{ID: "svc_1"}, in); err == nil {
			t.Errorf("accepted %+v", in)
		} else if _, ok := err.(ErrInvalid); !ok {
			t.Errorf("%+v: want ErrInvalid, got %v", in, err)
		}
	}
}

func TestDecide(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	type cand = candidate
	for _, tc := range []struct {
		name    string
		g       store.GitSource
		matched map[string]string
		forced  bool
		want    []cand
	}{
		{"first check builds the branch", store.GitSource{Branch: "main"},
			map[string]string{"refs/heads/main": a, "refs/tags/v1": b}, false, []cand{{"refs/heads/main", a, ""}}},
		{"first check of a pattern builds nothing", store.GitSource{Branch: "release/*"},
			map[string]string{"refs/heads/release/1": a}, false, nil},
		{"unchanged", store.GitSource{Branch: "main", RefSHAs: map[string]string{"refs/heads/main": a}},
			map[string]string{"refs/heads/main": a}, false, nil},
		{"changed ref compares with the old commit", store.GitSource{Branch: "main", RefSHAs: map[string]string{"refs/heads/main": a}},
			map[string]string{"refs/heads/main": b}, false, []cand{{"refs/heads/main", b, a}}},
		{"new branch and tag build in full", store.GitSource{Branch: "release/*", Tags: "v*", RefSHAs: map[string]string{"refs/heads/release/1": a}},
			map[string]string{"refs/heads/release/1": a, "refs/heads/release/2": b, "refs/tags/v2": c}, false,
			[]cand{{"refs/heads/release/2", b, ""}, {"refs/tags/v2", c, ""}}},
		{"source from before watch rules", store.GitSource{Branch: "main", LastSHA: a},
			map[string]string{"refs/heads/main": b}, false, []cand{{"refs/heads/main", b, a}}},
		{"webhook rebuild candidate", store.GitSource{Branch: "main", RefSHAs: map[string]string{"refs/heads/main": a}},
			map[string]string{"refs/heads/main": a}, true, []cand{{"refs/heads/main", a, ""}}},
	} {
		got := decide(tc.g, tc.matched, tc.forced)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

func TestDockerTag(t *testing.T) {
	for in, want := range map[string]string{"feature/x": "feature-x", "v1.2.0": "v1.2.0", ".hidden": "hidden", "": "ref"} {
		if got := dockerTag(in); got != want {
			t.Errorf("dockerTag(%q) = %q, want %q", in, got, want)
		}
	}
}
