package registry

import (
	"testing"
	"time"
)

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	ago := func(days int) *time.Time { x := now.Add(-time.Duration(days) * 24 * time.Hour); return &x }
	images := []Image{
		{Tag: "v1", Digest: "sha256:1", Created: ago(40)},
		{Tag: "v2", Digest: "sha256:2", Created: ago(30)},
		{Tag: "v3", Digest: "sha256:3", Created: ago(20)},
		{Tag: "v4", Digest: "sha256:4", Created: ago(10)},
		{Tag: "latest", Digest: "sha256:4", Created: ago(10)},
		{Tag: "sha-aaa", Digest: "sha256:a", Created: ago(100)},
		{Tag: "sha-bbb", Digest: "sha256:b", Created: ago(1)},
		{Tag: "dev", Digest: "sha256:d", Created: ago(5)},
		{Tag: "old", Digest: "sha256:o", Created: ago(400)},
		{Tag: "v5", Digest: "", Created: nil}, // unreadable
	}
	rules := []Rule{
		{Priority: 20, TagPrefix: "", OlderThanDays: 90},
		{Priority: 10, TagPrefix: "v", KeepLast: 2},
		{Priority: 15, TagPrefix: "sha-", OlderThanDays: 30},
	}
	if err := ValidateRules(rules); err != nil {
		t.Fatal(err)
	}
	inUse := func(d string) bool { return d == "sha256:1" }
	got := map[string]Decision{}
	for _, d := range Evaluate(images, rules, Options{InUse: inUse}, now) {
		for _, tag := range d.Tags {
			got[tag] = d
		}
	}
	want := map[string]bool{
		"v4": false, "v3": false, // newest two v*
		"v2":      true,  // beyond keepLast
		"v1":      false, // in use
		"v5":      false, // unreadable, counts as oldest
		"latest":  false, // v4's other tag: rule 10 keeps the image
		"sha-aaa": true, "sha-bbb": false,
		"dev": false, "old": true,
	}
	for tag, exp := range want {
		if got[tag].Expire != exp {
			t.Errorf("%s: expire=%v (%s), want %v", tag, got[tag].Expire, got[tag].Reason, exp)
		}
	}
	if !got["v1"].InUse || got["v1"].Rule != 10 {
		t.Errorf("v1: %+v", got["v1"])
	}
	if got["latest"].Rule != 10 || got["latest"].Digest != got["v4"].Digest {
		t.Errorf("latest should be v4's image, decided by rule 10: %+v", got["latest"])
	}

	// The build scenario: every build pushes a commit id and a branch tag.
	// The next build moves the branch tag, leaving the old image with its id
	// alone; a rule on the branch tag must still reach it, and keepLast must
	// count builds, not tags.
	builds := []Image{
		{Tag: "aaaaaaaaaaaa", Digest: "sha256:a", Created: ago(50)},
		{Tag: "bbbbbbbbbbbb", Digest: "sha256:b", Created: ago(40)},
		{Tag: "cccccccccccc", Digest: "sha256:c", Created: ago(30)},
		{Tag: "dddddddddddd", Digest: "sha256:d", Created: ago(20)},
		{Tag: "latest-main", Digest: "sha256:d", Created: ago(20)},
		{Tag: "eeeeeeeeeeee", Digest: "sha256:e", Created: ago(10)},
		{Tag: "ffffffffffff", Digest: "sha256:f", Created: ago(5)},
		{Tag: "latest-feat", Digest: "sha256:f", Created: ago(5)},
		{Tag: "buildcache", Digest: "sha256:cache"},
	}
	built := map[string]string{"aaaaaaaaaaaa": "latest-main", "bbbbbbbbbbbb": "latest-main", "cccccccccccc": "latest-main",
		"dddddddddddd": "latest-main", "eeeeeeeeeeee": "latest-feat", "ffffffffffff": "latest-feat"}
	opt := Options{Lineage: func(tag string) []string {
		if b := built[tag]; b != "" {
			return []string{b}
		}
		return nil
	}}
	byTag := func(ds []Decision) map[string]Decision {
		m := map[string]Decision{}
		for _, d := range ds {
			for _, tag := range d.Tags {
				m[tag] = d
			}
		}
		return m
	}
	g := byTag(Evaluate(builds, []Rule{{Priority: 1, TagPrefix: "latest-main", KeepLast: 2}, {Priority: 2, TagPrefix: "latest-feat", OlderThanDays: 7}}, opt, now))
	for tag, exp := range map[string]bool{
		"dddddddddddd": false, "latest-main": false, "cccccccccccc": false, // the newest two main builds
		"bbbbbbbbbbbb": true, "aaaaaaaaaaaa": true, // id-only images expire with their branch
		"eeeeeeeeeeee": true, "ffffffffffff": false, // feat: 10 days old vs 5
		"buildcache": false,
	} {
		if g[tag].Expire != exp {
			t.Errorf("build %s: expire=%v (%s), want %v", tag, g[tag].Expire, g[tag].Reason, exp)
		}
	}
	if !g["buildcache"].Cache || g["buildcache"].Rule != 0 {
		t.Errorf("buildcache should be left alone: %+v", g["buildcache"])
	}
	if g["dddddddddddd"].Tag != "latest-main" || len(g["dddddddddddd"].Tags) != 2 {
		t.Errorf("an image names its branch tag first: %+v", g["dddddddddddd"])
	}
	// Without lineage the old id-only images match nothing: kept forever.
	if bare := byTag(Evaluate(builds, []Rule{{Priority: 1, TagPrefix: "latest-main", KeepLast: 1}}, Options{}, now)); bare["aaaaaaaaaaaa"].Expire {
		t.Error("an id-only image cannot match a branch rule without lineage")
	}

	// A catch-all rule reaches id-only images too, and a just-pushed old
	// image is young: age counts from the push.
	pushed := []Image{
		{Tag: "base", Digest: "sha256:x", Created: ago(400), PushedAt: ago(1)},
		{Tag: "stale", Digest: "sha256:y", Created: ago(400), PushedAt: ago(200)},
	}
	for _, d := range Evaluate(pushed, []Rule{{Priority: 1, OlderThanDays: 90}}, Options{}, now) {
		if d.Expire != (d.Tag == "stale") {
			t.Errorf("%s: expire=%v (%s)", d.Tag, d.Expire, d.Reason)
		}
	}

	// Freed bytes count layers no kept image shares.
	ds := []Decision{
		{Image: Image{blobs: []Blob{{"base", 100}, {"a", 10}}}, Expire: true},
		{Image: Image{blobs: []Blob{{"base", 100}, {"b", 20}}}},
		{Image: Image{blobs: []Blob{{"c", 5}}}, Expire: true},
	}
	if n := FreesBytes(ds); n != 15 {
		t.Errorf("FreesBytes = %d, want 15", n)
	}

	for _, bad := range [][]Rule{
		nil,
		{{Priority: 1, KeepLast: 1, OlderThanDays: 1}},
		{{Priority: 1}},
		{{Priority: 1, KeepLast: 1}, {Priority: 1, KeepLast: 2}},
		{{Priority: 0, KeepLast: 1}},
		{{Priority: 1, KeepLast: 1, TagPrefix: "a/b"}},
	} {
		if ValidateRules(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
