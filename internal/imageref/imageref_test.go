package imageref

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]Ref{
		"nginx":                              {Host: "docker.io", Repo: "library/nginx", Tag: "latest"},
		"nginx:1.27":                         {Host: "docker.io", Repo: "library/nginx", Tag: "1.27"},
		"acme/api:v2":                        {Host: "docker.io", Repo: "acme/api", Tag: "v2"},
		"ghcr.io/acme/api":                   {Host: "ghcr.io", Repo: "acme/api", Tag: "latest"},
		"registry.example.com:5000/a/b:main": {Host: "registry.example.com:5000", Repo: "a/b", Tag: "main"},
		"localhost/app:dev":                  {Host: "localhost", Repo: "app", Tag: "dev"},
		"nginx:1.27@sha256:ab":               {Host: "docker.io", Repo: "library/nginx", Tag: "1.27", Digest: "sha256:ab"},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", in, got, err, want)
		}
	}
	if _, err := Parse("a b"); err == nil {
		t.Error("a reference with a space was accepted")
	}
}

// A registry behind a token service, as Docker Hub and GHCR are.
func TestDigestTokenFlow(t *testing.T) {
	const digest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			u, p, ok := r.BasicAuth()
			if r.URL.Query().Get("scope") != "repository:acme/api:pull" || r.URL.Query().Get("service") != "reg" || !ok || u != "bot" || p != "pw" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"T1"}`))
		case r.URL.Path == "/v2/acme/api/manifests/main":
			if r.Header.Get("Authorization") != "Bearer T1" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:acme/api:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.Contains(r.Header.Get("Accept"), "image.index") {
				http.Error(w, "accept", http.StatusBadRequest)
				return
			}
			w.Header().Set("Docker-Content-Digest", digest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	res := &Resolver{HTTP: srv.Client(), PlainHTTP: func(string) bool { return false },
		Creds: func(_ context.Context, h string) (string, string, bool) { return "bot", "pw", h == host }}
	got, err := res.Digest(context.Background(), host+"/acme/api:main")
	if err != nil || got != digest {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := res.Digest(context.Background(), host+"/acme/gone:main"); err == nil {
		t.Error("a missing image resolved")
	}
	// A pinned reference needs no registry.
	if got, _ := (&Resolver{}).Digest(context.Background(), "x/y@sha256:aa"); got != "sha256:aa" {
		t.Error(got)
	}
}

func TestParseChallenge(t *testing.T) {
	s, p := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`)
	if s != "Bearer" || p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" || p["scope"] != "repository:library/nginx:pull" {
		t.Errorf("%s %v", s, p)
	}
}
