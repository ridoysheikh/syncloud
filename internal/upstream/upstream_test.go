package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestImageHost(t *testing.T) {
	for in, want := range map[string]string{
		"nginx":                         "docker.io",
		"nginx:1.27":                    "docker.io",
		"acme/app:v1":                   "docker.io",
		"docker.io/library/nginx":       "docker.io",
		"index.docker.io/acme/app":      "docker.io",
		"ghcr.io/acme/app:1@sha256:abc": "ghcr.io",
		"localhost/app":                 "localhost",
		"127.0.0.1:5000/shop/hello:v1":  "127.0.0.1:5000",
		"Registry.Example.com:5000/a/b": "registry.example.com:5000",
	} {
		if got := ImageHost(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestCredentials(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secrets.New(make([]byte, 32))
	m := New(st, box)

	if _, err := m.Put(ctx, "https://index.docker.io/", "alice", "s3cret"); err != nil {
		t.Fatal(err)
	}
	c, err := m.Put(ctx, "ghcr.io", "bob", "ghp_x")
	if err != nil {
		t.Fatal(err)
	}
	// Replacing keeps the ID.
	c2, err := m.Put(ctx, "GHCR.io", "bob", "ghp_y")
	if err != nil || c2.ID != c.ID {
		t.Fatalf("replace: %v %s vs %s", err, c2.ID, c.ID)
	}
	for _, bad := range [][3]string{{"", "u", "p"}, {"bad host/x", "u", "p"}, {"ghcr.io", "", "p"}, {"ghcr.io", "u", ""}} {
		if _, err := m.Put(ctx, bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}

	decode := func(h string) map[string]string {
		b, err := base64.URLEncoding.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]string
		_ = json.Unmarshal(b, &v)
		return v
	}
	if a := decode(m.RegistryAuth(ctx, "nginx:1.27")); a["username"] != "alice" || a["password"] != "s3cret" || a["serveraddress"] != "https://index.docker.io/v1/" {
		t.Errorf("docker hub auth: %v", a)
	}
	if a := decode(m.RegistryAuth(ctx, "ghcr.io/acme/app:1")); a["password"] != "ghp_y" {
		t.Errorf("ghcr auth: %v", a)
	}
	if h := m.RegistryAuth(ctx, "quay.io/x/y"); h != "" {
		t.Errorf("no credential for quay.io, got %q", h)
	}
	auths := m.DockerConfigAuths(ctx)
	if len(auths) != 2 || auths["https://index.docker.io/v1/"] == nil || auths["ghcr.io"] == nil {
		t.Errorf("auths: %v", auths)
	}
	if err := st.DeleteUpstreamCredential(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if h := m.RegistryAuth(ctx, "ghcr.io/acme/app:1"); h != "" {
		t.Error("deleted credential still used")
	}
}
