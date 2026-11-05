package dbs

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// `app` is no superuser, so every extension the platform offers must be
// marked trusted in the image, or CREATE EXTENSION fails in the console.
func TestImageTrustsOfferedExtensions(t *testing.T) {
	raw, err := os.ReadFile("../../images/postgres/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ARG TRUSTED_EXTENSIONS="([^"]*)"`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("images/postgres/Dockerfile has no TRUSTED_EXTENSIONS")
	}
	trusted := strings.Fields(string(m[1]))
	for _, e := range pgExtensionAllowList {
		if !slices.Contains(trusted, e) {
			t.Errorf("%s is offered but the image does not trust it: add it to TRUSTED_EXTENSIONS", e)
		}
	}
	for _, e := range trusted {
		if !slices.Contains(pgExtensionAllowList, e) {
			t.Errorf("%s is trusted by the image but not offered: it would be open to every owner", e)
		}
	}
}
