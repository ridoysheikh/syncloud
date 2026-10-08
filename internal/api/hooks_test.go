package api

import "testing"

func TestHookJobName(t *testing.T) {
	for _, c := range []struct {
		service, kind string
		n             int
		want          string
	}{
		{"api", "pre-deploy", 1, "api-pre-1"},
		{"api", "post-deploy", 2, "api-post-2"},
		{"a-very-long-service-name-of-32ch", "pre-deploy", 10, "a-very-long-service-name-pre-10"},
	} {
		if got := hookJobName(c.service, c.kind, c.n); got != c.want || len(got) > 32 {
			t.Errorf("hookJobName(%q, %q, %d) = %q, want %q", c.service, c.kind, c.n, got, c.want)
		}
	}
}
