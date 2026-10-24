package traefik

import (
	"regexp"
	"strings"
)

// MiddlewareDeny answers 403 to everyone: blocked clients' routers use it
// (Traefik 3.7 has no ipDenyList).
const MiddlewareDeny = "syncloud-deny"

// Protection is a project's traffic protection on its routes (Phase 25).
type Protection struct {
	// Limits are skipped by requests that bypass them (rate limit,
	// concurrent requests); Always run for every request (body limit).
	Limits, Always []string
	// Deny are client CIDRs answered with 403.
	Deny []string
	// Requests from BypassIPs, with header BypassHeader set to
	// BypassSecret, or under BypassPaths skip the limits.
	BypassIPs                  []string
	BypassHeader, BypassSecret string
	BypassPaths                []string
}

var (
	cidrSafe   = regexp.MustCompile(`^[0-9a-fA-F:.]+(/\d{1,3})?$`)
	headerSafe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	// SecretSafe is what a bypass secret may contain (it sits in a rule).
	SecretSafe = regexp.MustCompile(`^[A-Za-z0-9._~-]{16,128}$`)
)

// clientIPs matches any of cidrs ("" when none is valid).
func clientIPs(cidrs []string) string {
	var parts []string
	for _, c := range cidrs {
		if cidrSafe.MatchString(c) {
			parts = append(parts, "ClientIP(`"+c+"`)")
		}
	}
	return strings.Join(parts, " || ")
}

// bypassRule matches the requests that skip the limits ("" when none).
func (p *Protection) bypassRule() string {
	var parts []string
	if c := clientIPs(p.BypassIPs); c != "" {
		parts = append(parts, c)
	}
	if headerSafe.MatchString(p.BypassHeader) && SecretSafe.MatchString(p.BypassSecret) {
		parts = append(parts, "Header(`"+p.BypassHeader+"`, `"+p.BypassSecret+"`)")
	}
	for _, path := range p.BypassPaths {
		if pathSafe.MatchString(path) && !strings.Contains(path, "`") {
			parts = append(parts, pathPrefix(path))
		}
	}
	return strings.Join(parts, " || ")
}
