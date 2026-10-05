// Package domain handles the platform's base domain (§5.0.2): public IP
// detection, sslip.io/nip.io names and the hostnames derived from the base.
package domain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Wildcard DNS services that resolve "<a-b-c-d>.<service>" to a.b.c.d.
var WildcardServices = []string{"sslip.io", "nip.io"}

// Wildcard returns the zero-config domain for ip on service, e.g.
// Wildcard("203.0.113.10", "sslip.io") = "203-0-113-10.sslip.io".
func Wildcard(ip, service string) string {
	return strings.NewReplacer(".", "-", ":", "-").Replace(ip) + "." + service
}

// RegistryHost is the registry's hostname for a base domain.
func RegistryHost(base string) string { return "registry." + base }

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Normalize lowercases d, strips a trailing dot and validates it as a DNS name
// with at least two labels.
func Normalize(d string) (string, error) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	if strings.Contains(d, "://") || strings.ContainsAny(d, "/:") {
		return "", errors.New("enter a hostname only, without scheme, port or path")
	}
	if len(d) > 240 { // leaves room for "registry." and service prefixes
		return "", errors.New("domain is too long")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", errors.New("domain must have at least two labels, e.g. example.com")
	}
	for _, l := range labels {
		if !labelRE.MatchString(l) {
			return "", fmt.Errorf("invalid domain label %q", l)
		}
	}
	return d, nil
}

// IsPublic reports whether ip is routable on the internet.
func IsPublic(a netip.Addr) bool {
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a) // CGNAT
}

// Detector finds the host's public IPv4 address and caches it.
type Detector struct {
	// Override is used as-is when set (--public-ip).
	Override string
	// Endpoints return the caller's address as plain text.
	Endpoints []string
	Client    *http.Client
	TTL       time.Duration

	mu  sync.Mutex
	ip  string
	err error
	at  time.Time
}

func NewDetector(override string) *Detector {
	return &Detector{
		Override:  override,
		Endpoints: []string{"https://api.ipify.org", "https://ipv4.icanhazip.com", "https://ifconfig.me/ip"},
		Client:    &http.Client{Timeout: 5 * time.Second},
		TTL:       10 * time.Minute,
	}
}

// PublicIP returns the cached address, detecting it when stale.
func (d *Detector) PublicIP(ctx context.Context) (string, error) {
	if d.Override != "" {
		return d.Override, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.at.IsZero() && time.Since(d.at) < d.TTL {
		return d.ip, d.err
	}
	d.ip, d.err = d.detect(ctx)
	d.at = time.Now()
	return d.ip, d.err
}

func (d *Detector) detect(ctx context.Context) (string, error) {
	// A public address on an interface wins (most VPS providers).
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() && IsPublic(p.Addr()) {
				return p.Addr().String(), nil
			}
		}
	}
	// Behind NAT (cloud 1:1 NAT, home labs): ask an echo service.
	var errs []error
	for _, u := range d.Endpoints {
		ip, err := d.ask(ctx, u)
		if err == nil {
			return ip, nil
		}
		errs = append(errs, err)
	}
	return "", fmt.Errorf("detect public IP: %w", errors.Join(errs...))
}

func (d *Detector) ask(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	a, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil || !a.Is4() {
		return "", fmt.Errorf("%s: unexpected answer %q", u, strings.TrimSpace(string(b)))
	}
	return a.String(), nil
}
