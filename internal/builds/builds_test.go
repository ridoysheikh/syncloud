package builds

import (
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
	sha := strings.Repeat("ab", 20)
	spec, err := m.buildSpec(sv, g, store.Build{SHA: sha})
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(spec.Command, " ")
	for _, want := range []string{
		"context=https://git.example.com/acme/web.git#" + sha + ":app",
		`type=image,"name=registry.example.com/shop/web:` + sha[:12] + `,registry.example.com/shop/web:latest-feature-x",push=true`,
		"--secret id=GIT_AUTH_TOKEN.git.example.com,env=GIT_TOKEN",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command lacks %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "registry.insecure") {
		t.Error("insecure push without RegistryInsecure")
	}
	if spec.Env["GIT_TOKEN"] != "tok" || !strings.Contains(spec.Env["BUILD_REGISTRY_AUTH"], `"registry.example.com"`) {
		t.Errorf("env: %v", spec.Env)
	}
	if spec.Placement.Node != "ctl-0" || spec.Image != BuildKitImage {
		t.Errorf("spec: %+v", spec)
	}

	m.cfg.RegistryHost = func() string { return "" }
	if _, err := m.buildSpec(sv, g, store.Build{SHA: sha}); err == nil {
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
	} {
		if _, err := m.SetSource(t.Context(), store.Service{ID: "svc_1"}, in); err == nil {
			t.Errorf("accepted %+v", in)
		} else if _, ok := err.(ErrInvalid); !ok {
			t.Errorf("%+v: want ErrInvalid, got %v", in, err)
		}
	}
}
