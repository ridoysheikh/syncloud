package registry

import "testing"

func TestParseRef(t *testing.T) {
	hosts := []string{"registry.example.com", "127.0.0.1:5000"}
	for _, c := range []struct {
		in, repo, tag, digest string
		ok                    bool
	}{
		{"@registry/shop/web:abc", "shop/web", "abc", "", true},
		{"@registry/shop/web", "shop/web", "latest", "", true},
		{"@registry/shop/web@sha256:1", "shop/web", "", "sha256:1", true},
		{"registry.example.com/shop/api:v2", "shop/api", "v2", "", true},
		{"127.0.0.1:5000/shop/api:v2@sha256:9", "shop/api", "v2", "sha256:9", true},
		{"nginx:1.27", "", "", "", false},
		{"ghcr.io/acme/app:1", "", "", "", false},
	} {
		repo, tag, digest, ok := ParseRef(c.in, hosts)
		if repo != c.repo || tag != c.tag || digest != c.digest || ok != c.ok {
			t.Errorf("%s: got %q %q %q %v", c.in, repo, tag, digest, ok)
		}
	}
}
