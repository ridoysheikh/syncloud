package traefik

import (
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"time"
)

// SettingGlobal holds the global Traefik settings as JSON.
const SettingGlobal = "traefik.settings"

// Settings are the cluster-wide Traefik options (§5.7) for every replica,
// on the controller and on edge nodes. The static ones become command-line
// flags, so changing them restarts Traefik; the dynamic ones are part of
// the served configuration and apply within seconds.
type Settings struct {
	// Static: entrypoints and proxy behaviour.
	LogLevel string `json:"logLevel"` // DEBUG | INFO | WARN | ERROR
	// Responding timeouts of the public entrypoints ("" = Traefik's default;
	// "0s" disables the limit).
	ReadTimeout  string `json:"readTimeout"`
	WriteTimeout string `json:"writeTimeout"`
	IdleTimeout  string `json:"idleTimeout"`
	// TrustedIPs are proxies (a CDN or a load balancer) whose
	// X-Forwarded-* headers are kept; from anyone else they are replaced.
	TrustedIPs []string `json:"trustedIPs"`
	// ProxyProtocol accepts the PROXY protocol from TrustedIPs, for load
	// balancers that pass the client address that way.
	ProxyProtocol bool `json:"proxyProtocol"`
	// HTTP3 serves HTTP/3 (QUIC) on the HTTPS port as well.
	HTTP3 bool `json:"http3"`
	// Upstream connections to tasks.
	DialTimeout           string `json:"dialTimeout"`
	ResponseHeaderTimeout string `json:"responseHeaderTimeout"`
	MaxIdleConnsPerHost   int    `json:"maxIdleConnsPerHost"`

	// Dynamic: defaults for every service route.
	RedirectHTTPS bool   `json:"redirectHttps"` // HTTP requests to service domains redirect to HTTPS
	MinTLS        string `json:"minTls"`        // "1.2" | "1.3"
	SNIStrict     bool   `json:"sniStrict"`     // refuse TLS clients without a known server name
	RetryAttempts int    `json:"retryAttempts"` // 0 disables the default retry
	HSTSSeconds   int    `json:"hstsSeconds"`   // 0 sends no Strict-Transport-Security
	Compress      bool   `json:"compress"`      // gzip/brotli/zstd responses
	MaxBodyMB     int    `json:"maxBodyMb"`     // request body limit, 0 = unlimited
}

// DefaultDialTimeout bounds connecting to a task (over the private network).
const DefaultDialTimeout = "2s"

// DefaultSettings are what a fresh install uses.
func DefaultSettings() Settings {
	return Settings{LogLevel: "INFO", TrustedIPs: []string{}, RedirectHTTPS: true, MinTLS: "1.2", RetryAttempts: 2}
}

// ParseSettings reads stored settings; missing fields keep their defaults.
func ParseSettings(text string) Settings {
	s := DefaultSettings()
	if text != "" {
		_ = json.Unmarshal([]byte(text), &s)
	}
	if s.TrustedIPs == nil {
		s.TrustedIPs = []string{}
	}
	return s
}

// Validate checks every field and normalizes the trusted addresses.
func (s *Settings) Validate() error {
	s.LogLevel = strings.ToUpper(strings.TrimSpace(s.LogLevel))
	if s.LogLevel == "" {
		s.LogLevel = "INFO"
	}
	if !slices.Contains([]string{"DEBUG", "INFO", "WARN", "ERROR"}, s.LogLevel) {
		return fmt.Errorf("logLevel must be DEBUG, INFO, WARN or ERROR")
	}
	for name, v := range map[string]*string{"readTimeout": &s.ReadTimeout, "writeTimeout": &s.WriteTimeout, "idleTimeout": &s.IdleTimeout,
		"dialTimeout": &s.DialTimeout, "responseHeaderTimeout": &s.ResponseHeaderTimeout} {
		*v = strings.TrimSpace(*v)
		if *v == "" {
			continue
		}
		d, err := time.ParseDuration(*v)
		if err != nil || d < 0 || d > 24*time.Hour {
			return fmt.Errorf("%s must be a duration such as 30s or 5m (at most 24h)", name)
		}
	}
	ips := make([]string, 0, len(s.TrustedIPs))
	for _, ip := range s.TrustedIPs {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(ip); err != nil {
			if net.ParseIP(ip) == nil {
				return fmt.Errorf("trusted proxy %q is not an IP address or CIDR", ip)
			}
		}
		if strings.ContainsAny(ip, ", ") {
			return fmt.Errorf("trusted proxy %q is not an IP address or CIDR", ip)
		}
		ips = append(ips, ip)
	}
	s.TrustedIPs = ips
	if s.ProxyProtocol && len(ips) == 0 {
		return fmt.Errorf("the PROXY protocol needs trusted proxies: anyone could otherwise forge client addresses")
	}
	if s.MaxIdleConnsPerHost < 0 || s.MaxIdleConnsPerHost > 10000 {
		return fmt.Errorf("maxIdleConnsPerHost must be between 0 and 10000")
	}
	if s.MinTLS == "" {
		s.MinTLS = "1.2"
	}
	if s.MinTLS != "1.2" && s.MinTLS != "1.3" {
		return fmt.Errorf("minTls must be 1.2 or 1.3")
	}
	if s.RetryAttempts < 0 || s.RetryAttempts > 10 {
		return fmt.Errorf("retryAttempts must be between 0 and 10")
	}
	if s.HSTSSeconds < 0 || s.HSTSSeconds > 2*365*24*3600 {
		return fmt.Errorf("hstsSeconds must be between 0 and two years")
	}
	if s.MaxBodyMB < 0 || s.MaxBodyMB > 100*1024 {
		return fmt.Errorf("maxBodyMb must be between 0 and 102400")
	}
	return nil
}

// StaticArgs are the command-line flags for every Traefik replica, added
// after the built-in ones.
func (s Settings) StaticArgs() []string {
	level := s.LogLevel
	if level == "" {
		level = "INFO"
	}
	args := []string{"--log.level=" + level}
	for _, ep := range []string{"web", "websecure"} {
		for k, v := range map[string]string{"readTimeout": s.ReadTimeout, "writeTimeout": s.WriteTimeout, "idleTimeout": s.IdleTimeout} {
			if v != "" {
				args = append(args, fmt.Sprintf("--entrypoints.%s.transport.respondingTimeouts.%s=%s", ep, k, v))
			}
		}
		if len(s.TrustedIPs) > 0 {
			args = append(args, fmt.Sprintf("--entrypoints.%s.forwardedHeaders.trustedIPs=%s", ep, strings.Join(s.TrustedIPs, ",")))
			if s.ProxyProtocol {
				args = append(args, fmt.Sprintf("--entrypoints.%s.proxyProtocol.trustedIPs=%s", ep, strings.Join(s.TrustedIPs, ",")))
			}
		}
	}
	if s.HTTP3 {
		args = append(args, "--entrypoints.websecure.http3=true")
	}
	// A task that just died (a crashed node) never answers the dial; a short
	// timeout lets the retry move the request to another task quickly
	// instead of hanging for Traefik's default 30s.
	dial := s.DialTimeout
	if dial == "" {
		dial = DefaultDialTimeout
	}
	args = append(args, "--serversTransport.forwardingTimeouts.dialTimeout="+dial)
	if s.ResponseHeaderTimeout != "" {
		args = append(args, "--serversTransport.forwardingTimeouts.responseHeaderTimeout="+s.ResponseHeaderTimeout)
	}
	if s.MaxIdleConnsPerHost > 0 {
		args = append(args, fmt.Sprintf("--serversTransport.maxIdleConnsPerHost=%d", s.MaxIdleConnsPerHost))
	}
	slices.Sort(args[1:]) // map order must not change the spec (and restart Traefik)
	return args
}

// WithStatic replaces the "--log.level" flag of base flags and appends the
// other static flags.
func WithStatic(base []string, s Settings) []string {
	out := make([]string, 0, len(base)+8)
	for _, a := range base {
		if !strings.HasPrefix(a, "--log.level=") {
			out = append(out, a)
		}
	}
	return append(out, s.StaticArgs()...)
}

// Entrypoints are the flags of extra TCP entrypoints (name -> address),
// sorted by name: the public database endpoints (Phase 12e).
func Entrypoints(addrs map[string]string) []string {
	names := make([]string, 0, len(addrs))
	for n := range addrs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "--entrypoints."+n+".address="+addrs[n])
	}
	return out
}

// TLSOptions is Traefik's tls.options entry.
type TLSOptions struct {
	MinVersion    string   `json:"minVersion"`
	SNIStrict     bool     `json:"sniStrict,omitempty"`
	ALPNProtocols []string `json:"alpnProtocols,omitempty"`
}

// alpnProtocols are Traefik's defaults plus "postgresql": libpq 17 and later
// offer only that, and Traefik refuses a handshake with no common protocol.
var alpnProtocols = []string{"h2", "http/1.1", "acme-tls/1", "postgresql"}

// Buffering limits request bodies.
type Buffering struct {
	MaxRequestBodyBytes int64 `json:"maxRequestBodyBytes"`
}

// Generated names of the global middlewares.
const (
	mwRetry    = "syncloud-retry"
	mwHSTS     = "syncloud-hsts"
	mwCompress = "syncloud-compress"
	mwBody     = "syncloud-body-limit"
)

// defaults adds the global middlewares to d and returns the chain every
// service route starts with, and whether the default retry is on.
func (s Settings) defaults(d *Dynamic) (chain []string, retry bool) {
	if s.MaxBodyMB > 0 {
		d.HTTP.Middlewares[mwBody] = Middleware{Buffering: &Buffering{MaxRequestBodyBytes: int64(s.MaxBodyMB) << 20}}
		chain = append(chain, mwBody)
	}
	if s.HSTSSeconds > 0 {
		d.HTTP.Middlewares[mwHSTS] = Middleware{Headers: &Headers{STSSeconds: s.HSTSSeconds, STSIncludeSubdomains: true}}
		chain = append(chain, mwHSTS)
	}
	if s.Compress {
		d.HTTP.Middlewares[mwCompress] = Middleware{Compress: &Compress{}}
		chain = append(chain, mwCompress)
	}
	if s.RetryAttempts > 0 {
		d.HTTP.Middlewares[mwRetry] = Middleware{Retry: &Retry{Attempts: s.RetryAttempts, InitialInterval: "100ms"}}
	}
	return chain, s.RetryAttempts > 0
}

func (s Settings) tlsOptions() map[string]TLSOptions {
	v := "VersionTLS12"
	if s.MinTLS == "1.3" {
		v = "VersionTLS13"
	}
	return map[string]TLSOptions{"default": {MinVersion: v, SNIStrict: s.SNIStrict, ALPNProtocols: alpnProtocols}}
}
