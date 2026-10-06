package registry

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Rule is one lifecycle rule (§5.10). Rules are applied in priority order
// (lowest first); an image belongs to the first rule whose tag prefix
// matches it, so a lower-priority rule never expires an image that a
// higher-priority rule kept. Untagged manifests are always removed by
// garbage collection.
type Rule struct {
	Priority    int    `json:"priority"`
	Description string `json:"description,omitempty"`
	// TagPrefix selects tags starting with it ("" = every tag).
	TagPrefix string `json:"tagPrefix"`
	// Exactly one of the two:
	KeepLast      int `json:"keepLast,omitempty"`      // keep the newest N, expire the rest
	OlderThanDays int `json:"olderThanDays,omitempty"` // expire images created more than N days ago
}

// ValidateRules checks rules and sorts them by priority.
func ValidateRules(rules []Rule) error {
	if len(rules) == 0 {
		return errors.New("a policy needs at least one rule")
	}
	if len(rules) > 50 {
		return errors.New("a policy can have at most 50 rules")
	}
	seen := map[int]bool{}
	for i, r := range rules {
		switch {
		case r.Priority < 1 || r.Priority > 1000:
			return fmt.Errorf("rule %d: priority must be 1–1000", i+1)
		case seen[r.Priority]:
			return fmt.Errorf("rule %d: priority %d is used twice", i+1, r.Priority)
		case (r.KeepLast > 0) == (r.OlderThanDays > 0):
			return fmt.Errorf("rule %d: set either keepLast or olderThanDays", i+1)
		case r.KeepLast < 0 || r.KeepLast > 10000 || r.OlderThanDays < 0 || r.OlderThanDays > 3650:
			return fmt.Errorf("rule %d: keepLast must be 1–10000 and olderThanDays 1–3650", i+1)
		case strings.ContainsAny(r.TagPrefix, " /:@"):
			return fmt.Errorf("rule %d: invalid tag prefix %q", i+1, r.TagPrefix)
		}
		seen[r.Priority] = true
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Priority < rules[j].Priority })
	return nil
}

// Decision says what a policy does with one image.
type Decision struct {
	Image
	Expire bool `json:"expire"`
	// InUse images are never expired (a service runs or can roll back to them).
	InUse  bool   `json:"inUse"`
	Rule   int    `json:"rule"` // priority of the deciding rule; 0 = no rule matched
	Reason string `json:"reason"`
}

// Evaluate applies rules (validated) to a repository's images. inUse
// reports digests that must be kept. Deleting a manifest removes every tag
// pointing at it, so an image is only expired when all its tags expire.
func Evaluate(images []Image, rules []Rule, inUse func(digest string) bool, now time.Time) []Decision {
	// Newest first; images without a creation time count as oldest for
	// keepLast but are never expired by age.
	sorted := append([]Image(nil), images...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].Created, sorted[j].Created
		if (a == nil) != (b == nil) {
			return a != nil
		}
		return a != nil && a.After(*b)
	})
	out := make([]Decision, len(sorted))
	taken := make([]bool, len(sorted))
	for i, img := range sorted {
		out[i] = Decision{Image: img, Reason: "no rule matches"}
	}
	for _, r := range rules {
		kept := 0
		for i, img := range sorted {
			if taken[i] || !strings.HasPrefix(img.Tag, r.TagPrefix) {
				continue
			}
			taken[i] = true
			d := &out[i]
			d.Rule = r.Priority
			sel := "every tag"
			if r.TagPrefix != "" {
				sel = "tags " + r.TagPrefix + "*"
			}
			switch {
			case r.KeepLast > 0:
				if kept < r.KeepLast {
					kept++
					d.Reason = fmt.Sprintf("among the newest %d of %s", r.KeepLast, sel)
				} else {
					d.Expire, d.Reason = true, fmt.Sprintf("beyond the newest %d of %s", r.KeepLast, sel)
				}
			case img.Created == nil:
				d.Reason = "creation time unknown"
			case now.Sub(*img.Created) > time.Duration(r.OlderThanDays)*24*time.Hour:
				d.Expire, d.Reason = true, fmt.Sprintf("%s older than %d days", sel, r.OlderThanDays)
			default:
				d.Reason = fmt.Sprintf("%s newer than %d days", sel, r.OlderThanDays)
			}
		}
	}
	// A digest survives if any of its tags survives, or a service uses it.
	keep := map[string]bool{}
	for i := range out {
		d := &out[i]
		if d.Digest != "" && inUse != nil && inUse(d.Digest) {
			d.InUse = true
		}
		if !d.Expire || d.InUse {
			keep[d.Digest] = true
		}
	}
	for i := range out {
		d := &out[i]
		if d.Expire && keep[d.Digest] {
			d.Expire = false
			if d.InUse {
				d.Reason = "in use by a service (would be " + d.Reason + ")"
			} else {
				d.Reason = "shares its digest with a kept tag"
			}
		}
		if d.Digest == "" && d.Expire {
			d.Expire, d.Reason = false, "manifest unreadable"
		}
	}
	return out
}

// ParseRef splits a private-registry image reference into repository,
// tag and digest. Other images return ok=false.
func ParseRef(image string, hosts []string) (repo, tag, digest string, ok bool) {
	path, found := strings.CutPrefix(image, "@registry/")
	for _, h := range hosts {
		if found {
			break
		}
		if h != "" {
			path, found = strings.CutPrefix(image, h+"/")
		}
	}
	if !found {
		return "", "", "", false
	}
	if i := strings.IndexByte(path, '@'); i >= 0 {
		path, digest = path[:i], path[i+1:]
	}
	if i := strings.LastIndexByte(path, ':'); i > strings.LastIndexByte(path, '/') {
		path, tag = path[:i], path[i+1:]
	} else if digest == "" {
		tag = "latest"
	}
	return path, tag, digest, path != ""
}
