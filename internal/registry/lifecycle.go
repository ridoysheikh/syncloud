package registry

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Rule is one lifecycle rule (§5.10). Rules are applied in priority order
// (lowest first); an image belongs to the first rule whose tag prefix
// matches one of its names (any of its tags, or the branch it was built
// from), so a lower-priority rule never expires an image that a
// higher-priority rule kept. Untagged manifests are always removed by
// garbage collection.
type Rule struct {
	Priority    int    `json:"priority"`
	Description string `json:"description,omitempty"`
	// TagPrefix selects images with a name starting with it ("" = every image).
	TagPrefix string `json:"tagPrefix"`
	// Exactly one of the two:
	KeepLast      int `json:"keepLast,omitempty"`      // keep the newest N, expire the rest
	OlderThanDays int `json:"olderThanDays,omitempty"` // expire images last pushed (else built) more than N days ago
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

// Decision says what a policy does with one image. An image is one
// manifest digest with every tag pointing at it: a build pushes a commit id
// and a branch tag to the same image, and the two are judged together.
type Decision struct {
	Image           // Tag is the primary tag; the others are in Tags
	Tags   []string `json:"tags"`
	Names  []string `json:"names"` // names the image answers to besides its tags (the branch a commit was built from)
	Expire bool     `json:"expire"`
	// InUse images are never expired (a service runs or can roll back to them).
	InUse bool `json:"inUse"`
	// WouldExpire marks an image the rules selected but something kept: in
	// use by a service.
	WouldExpire bool `json:"wouldExpire"`
	// Cache marks the builders' layer cache, which rules never touch.
	Cache  bool   `json:"cache"`
	Rule   int    `json:"rule"` // priority of the deciding rule; 0 = no rule matched
	Reason string `json:"reason"`
}

// CacheTag is the tag builds keep their layer cache under.
const CacheTag = "buildcache"

// IsCacheTag reports the tag of the builders' layer cache.
func IsCacheTag(tag string) bool { return tag == CacheTag }

var idTagRE = regexp.MustCompile(`^[0-9a-f]{7,64}(-r\d+)?$`)

// IsIDTag reports a commit-id tag such as the ones builds push.
func IsIDTag(tag string) bool { return idTagRE.MatchString(tag) }

// Stamp is the time an image's age counts from: when it was last pushed
// here, else when it was built. Pushing an old image into the registry
// must not make it look old.
func (i Image) Stamp() *time.Time {
	if i.PushedAt != nil {
		return i.PushedAt
	}
	return i.Created
}

// Options tune an evaluation.
type Options struct {
	// InUse reports digests that must be kept.
	InUse func(digest string) bool
	// Lineage returns extra names a tag answers to: an old image keeps only
	// its commit-id tag, which says nothing about the branch it came from.
	Lineage func(tag string) []string
}

type imageGroup struct {
	img   Image
	tags  []string
	names []string
	cache bool
}

// groupImages collapses per-tag entries into one entry per digest, newest
// first. Unreadable manifests (no digest) stay on their own.
func groupImages(images []Image, lineage func(string) []string) []*imageGroup {
	var groups []*imageGroup
	byDigest := map[string]*imageGroup{}
	for _, img := range images {
		g := byDigest[img.Digest]
		if g == nil || img.Digest == "" {
			g = &imageGroup{img: img, cache: true}
			groups = append(groups, g)
			if img.Digest != "" {
				byDigest[img.Digest] = g
			}
		}
		if g.img.Created == nil {
			g.img.Created = img.Created
		}
		if g.img.PushedAt == nil {
			g.img.PushedAt = img.PushedAt
		}
		g.tags = append(g.tags, img.Tag)
		if !IsCacheTag(img.Tag) {
			g.cache = false
			g.names = append(g.names, img.Tag)
			if lineage != nil {
				g.names = append(g.names, lineage(img.Tag)...)
			}
		}
	}
	for _, g := range groups {
		// Named tags first: "latest-main" tells more than a commit id.
		sort.SliceStable(g.tags, func(i, j int) bool { return !IsIDTag(g.tags[i]) && IsIDTag(g.tags[j]) })
		g.img.Tag = g.tags[0]
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i].img.Stamp(), groups[j].img.Stamp()
		if (a == nil) != (b == nil) {
			return a != nil
		}
		return a != nil && a.After(*b)
	})
	return groups
}

// Evaluate applies rules (validated) to a repository's images. Rules see
// whole images: an image is selected by the first rule whose prefix matches
// any of its names, counts once towards keepLast however many tags it has,
// and goes with all of them. Newest first; images without a time count as
// oldest for keepLast but are never expired by age.
func Evaluate(images []Image, rules []Rule, opt Options, now time.Time) []Decision {
	groups := groupImages(images, opt.Lineage)
	out := make([]Decision, len(groups))
	taken := make([]bool, len(groups))
	for i, g := range groups {
		tags := append([]string(nil), g.tags...)
		var extra []string
		for _, n := range g.names {
			if !slices.Contains(tags, n) && !slices.Contains(extra, n) {
				extra = append(extra, n)
			}
		}
		out[i] = Decision{Image: g.img, Tags: tags, Names: extra, Cache: g.cache, Reason: "no rule matches"}
		if g.cache {
			out[i].Reason = "the builders' layer cache: rules leave it alone"
			taken[i] = true
		}
	}
	for _, r := range rules {
		kept := 0
		for i, g := range groups {
			if taken[i] {
				continue
			}
			match := slices.IndexFunc(g.names, func(n string) bool { return strings.HasPrefix(n, r.TagPrefix) })
			if match < 0 {
				continue
			}
			taken[i] = true
			d := &out[i]
			d.Rule = r.Priority
			sel := "every tag"
			if r.TagPrefix != "" {
				sel = "tags " + r.TagPrefix + "*"
			}
			age := g.img.Stamp()
			switch {
			case r.KeepLast > 0:
				if kept < r.KeepLast {
					kept++
					d.Reason = fmt.Sprintf("among the newest %d of %s", r.KeepLast, sel)
				} else {
					d.Expire, d.Reason = true, fmt.Sprintf("beyond the newest %d of %s", r.KeepLast, sel)
				}
			case age == nil:
				d.Reason = "age unknown"
			case now.Sub(*age) > time.Duration(r.OlderThanDays)*24*time.Hour:
				d.Expire, d.Reason = true, fmt.Sprintf("%s older than %d days", sel, r.OlderThanDays)
			default:
				d.Reason = fmt.Sprintf("%s newer than %d days", sel, r.OlderThanDays)
			}
			if n := g.names[match]; !slices.Contains(g.tags, n) {
				d.Reason += " (built from " + strings.TrimPrefix(n, "latest-") + ")"
			}
		}
	}
	for i := range out {
		d := &out[i]
		if d.Digest != "" && opt.InUse != nil && opt.InUse(d.Digest) {
			d.InUse = true
		}
		switch {
		case d.Expire && d.InUse:
			d.Expire, d.WouldExpire = false, true
			d.Reason = "in use by a service (would be " + d.Reason + ")"
		case d.Expire && d.Digest == "":
			d.Expire, d.Reason = false, "manifest unreadable"
		}
	}
	return out
}

// FreesBytes estimates what deleting the expiring images frees: layers no
// kept image of the repository shares. Cache layers and other repositories
// are not considered, so the real figure can be a little higher.
func FreesBytes(ds []Decision) int64 {
	kept := map[string]bool{}
	for _, d := range ds {
		if !d.Expire {
			for _, b := range d.blobs {
				kept[b.Digest] = true
			}
		}
	}
	seen := map[string]bool{}
	var n int64
	for _, d := range ds {
		if !d.Expire {
			continue
		}
		for _, b := range d.blobs {
			if !kept[b.Digest] && !seen[b.Digest] {
				seen[b.Digest] = true
				n += b.Size
			}
		}
	}
	return n
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
