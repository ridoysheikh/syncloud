package gitremote

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func TestLsRemoteSmartAndDumb(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	smart := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); p != "tok" {
			_ = u
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		fmt.Fprint(w, pkt("# service=git-upload-pack\n")+"0000"+pkt(sha+" HEAD\x00multi_ack symref=HEAD:refs/heads/main\n")+pkt(sha+" refs/heads/main\n")+"0000")
	}))
	defer smart.Close()
	refs, err := LsRemote(context.Background(), smart.URL+"/org/app.git", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := BranchSHA(refs, "main"); !ok || got != sha {
		t.Fatalf("smart: %v", refs)
	}
	if _, err := LsRemote(context.Background(), smart.URL+"/org/app.git", "wrong"); err == nil {
		t.Fatal("bad token accepted")
	}

	dumb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s\trefs/heads/dev\n", sha)
	}))
	defer dumb.Close()
	refs, err = LsRemote(context.Background(), dumb.URL+"/app.git", "")
	if err != nil || refs["refs/heads/dev"] != sha {
		t.Fatalf("dumb: %v %v", refs, err)
	}
	for _, bad := range []string{"ftp://x/y", "https://user:pw@github.com/a/b", "not a url"} {
		if ValidateURL(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestMatch(t *testing.T) {
	a, b, c, d := "aaaa", "bbbb", "cccc", "dddd"
	refs := map[string]string{
		"HEAD": a, "refs/heads/main": a, "refs/heads/release/1.0": b, "refs/heads/release/2.0/hotfix": c,
		"refs/tags/v1.0": d, "refs/tags/v1.0^{}": b, "refs/tags/v2.0": c, "refs/tags/other": a,
	}
	got := Match(refs, "release/*", "v*")
	want := map[string]string{"refs/heads/release/1.0": b, "refs/tags/v1.0": b, "refs/tags/v2.0": c}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if got := Match(refs, "main", ""); len(got) != 1 || got["refs/heads/main"] != a {
		t.Errorf("main: %v", got)
	}
	for _, p := range []string{"main", "release/*", "v*", "feat-[0-9]*"} {
		if err := ValidatePattern(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"", "a b", "../x", "x:y", "[", "a/"} {
		if ValidatePattern(p) == nil {
			t.Errorf("%q accepted", p)
		}
	}
}
