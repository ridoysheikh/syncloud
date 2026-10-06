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
	for _, d := range Evaluate(images, rules, inUse, now) {
		got[d.Tag] = d
	}
	want := map[string]bool{
		"v4": false, "v3": false, // newest two v*
		"v2":      true,  // beyond keepLast
		"v1":      false, // in use
		"v5":      false, // unreadable, counts as oldest
		"latest":  false, // rule 20, 10 days old
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
	if got["latest"].Rule != 20 {
		t.Errorf("latest decided by rule %d", got["latest"].Rule)
	}

	// A tag kept by a rule protects every tag sharing its digest.
	shared := Evaluate([]Image{
		{Tag: "v9", Digest: "sha256:x", Created: ago(1)},
		{Tag: "nightly", Digest: "sha256:x", Created: ago(1)},
	}, []Rule{{Priority: 1, TagPrefix: "nightly", KeepLast: 1}, {Priority: 2, TagPrefix: "v", OlderThanDays: 1}}, nil, now.Add(48*time.Hour))
	for _, d := range shared {
		if d.Expire {
			t.Errorf("%s expired although its digest is kept: %s", d.Tag, d.Reason)
		}
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
