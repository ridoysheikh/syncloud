package traefik

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Middleware presets (§5.7): the toggles users attach to services, rendered
// into Traefik middlewares. A route applies them in a fixed order, so access
// checks run before anything else.
var PresetTypes = []string{
	"ip-allowlist", "rate-limit", "basic-auth", "redirect-www", "cors", "security-headers", "circuit-breaker", "compress", "retry",
}

type RateLimit struct {
	Average         int              `json:"average"`
	Burst           int              `json:"burst"`
	Period          string           `json:"period,omitempty"`
	SourceCriterion *SourceCriterion `json:"sourceCriterion,omitempty"`
}

type SourceCriterion struct {
	IPStrategy struct {
		Depth int `json:"depth"`
	} `json:"ipStrategy"`
}

type BasicAuth struct {
	Users        []string `json:"users"`
	Realm        string   `json:"realm,omitempty"`
	RemoveHeader bool     `json:"removeHeader"`
}

type IPAllowList struct {
	SourceRange []string `json:"sourceRange"`
}

type Headers struct {
	STSSeconds                    int      `json:"stsSeconds,omitempty"`
	STSIncludeSubdomains          bool     `json:"stsIncludeSubdomains,omitempty"`
	FrameDeny                     bool     `json:"frameDeny,omitempty"`
	ContentTypeNosniff            bool     `json:"contentTypeNosniff,omitempty"`
	BrowserXSSFilter              bool     `json:"browserXssFilter,omitempty"`
	ReferrerPolicy                string   `json:"referrerPolicy,omitempty"`
	AccessControlAllowOriginList  []string `json:"accessControlAllowOriginList,omitempty"`
	AccessControlAllowMethods     []string `json:"accessControlAllowMethods,omitempty"`
	AccessControlAllowHeaders     []string `json:"accessControlAllowHeaders,omitempty"`
	AccessControlAllowCredentials bool     `json:"accessControlAllowCredentials,omitempty"`
	AccessControlMaxAge           int      `json:"accessControlMaxAge,omitempty"`
	AddVaryHeader                 bool     `json:"addVaryHeader,omitempty"`
}

type Compress struct{}

type CircuitBreaker struct {
	Expression string `json:"expression"`
}

// ── preset configs (what the API stores and returns) ───────────────────────

type rateLimitConfig struct {
	Average int `json:"average"` // requests per second, per client IP
	Burst   int `json:"burst"`
}

type authUser struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // write-only; stored as a bcrypt hash
	Hash     string `json:"hash,omitempty"`     // never returned by the API
}

type basicAuthConfig struct {
	Users []authUser `json:"users"`
	Realm string     `json:"realm,omitempty"`
}

type allowListConfig struct {
	SourceRange []string `json:"sourceRange"`
}

type headersConfig struct {
	HSTS           bool   `json:"hsts"`
	HSTSSeconds    int    `json:"hstsSeconds,omitempty"`
	FrameDeny      bool   `json:"frameDeny"`
	NoSniff        bool   `json:"noSniff"`
	ReferrerPolicy string `json:"referrerPolicy,omitempty"`
}

type corsConfig struct {
	Origins     []string `json:"origins"`
	Methods     []string `json:"methods,omitempty"`
	Headers     []string `json:"headers,omitempty"`
	Credentials bool     `json:"credentials,omitempty"`
	MaxAge      int      `json:"maxAge,omitempty"`
}

type breakerConfig struct {
	Expression string `json:"expression"`
}

type retryConfig struct {
	Attempts int `json:"attempts"`
}

var (
	userRE    = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
	originRE  = regexp.MustCompile(`^(\*|https?://[a-z0-9.*-]+(:[0-9]{1,5})?)$`)
	methodRE  = regexp.MustCompile(`^[A-Z]{3,7}$`)
	headerRE  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	breakerRE = regexp.MustCompile(`^(NetworkErrorRatio\(\)|ResponseCodeRatio\(\d{3}, ?\d{3}, ?\d{1,3}, ?\d{3}\)|LatencyAtQuantileMS\(\d{1,2}(\.\d+)?\)|[<>=!&| ().0-9])+$`)
	referrers = []string{"", "no-referrer", "no-referrer-when-downgrade", "origin", "origin-when-cross-origin", "same-origin", "strict-origin", "strict-origin-when-cross-origin", "unsafe-url"}
)

func decodeStrict(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ValidatePreset checks and normalizes a preset's config. For basic-auth,
// new passwords are hashed, and a user given without a password keeps the
// hash from prev (the stored config being replaced).
func ValidatePreset(typ string, raw, prev json.RawMessage) (json.RawMessage, error) {
	var v any
	switch typ {
	case "rate-limit":
		c := rateLimitConfig{}
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if c.Average < 1 || c.Average > 100000 {
			return nil, errors.New("average must be between 1 and 100000 requests per second")
		}
		if c.Burst == 0 {
			c.Burst = c.Average * 2
		}
		if c.Burst < 1 || c.Burst > 1000000 {
			return nil, errors.New("burst must be between 1 and 1000000")
		}
		v = c
	case "basic-auth":
		c := basicAuthConfig{}
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		old := map[string]string{}
		var p basicAuthConfig
		if len(prev) > 0 && json.Unmarshal(prev, &p) == nil {
			for _, u := range p.Users {
				old[u.Username] = u.Hash
			}
		}
		if len(c.Users) == 0 || len(c.Users) > 50 {
			return nil, errors.New("basic-auth needs between 1 and 50 users")
		}
		seen := map[string]bool{}
		for i := range c.Users {
			u := &c.Users[i]
			if !userRE.MatchString(u.Username) || seen[u.Username] {
				return nil, fmt.Errorf("user %q: usernames are unique letters, digits and . _ @ -", u.Username)
			}
			seen[u.Username] = true
			switch {
			case u.Password != "":
				if len(u.Password) < 8 || len(u.Password) > 72 {
					return nil, fmt.Errorf("user %s: the password must be 8 to 72 characters", u.Username)
				}
				h, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
				if err != nil {
					return nil, err
				}
				u.Hash, u.Password = string(h), ""
			case old[u.Username] != "":
				u.Hash = old[u.Username]
			default:
				return nil, fmt.Errorf("user %s needs a password", u.Username)
			}
		}
		if len(c.Realm) > 100 || strings.ContainsAny(c.Realm, `"\`) {
			return nil, errors.New("realm must be under 100 characters, without quotes")
		}
		v = c
	case "ip-allowlist":
		c := allowListConfig{}
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if len(c.SourceRange) == 0 || len(c.SourceRange) > 100 {
			return nil, errors.New("the allow-list needs between 1 and 100 addresses or CIDRs")
		}
		for i, s := range c.SourceRange {
			p, err := netip.ParsePrefix(strings.TrimSpace(s))
			if err != nil {
				a, aerr := netip.ParseAddr(strings.TrimSpace(s))
				if aerr != nil {
					return nil, fmt.Errorf("%q is not an IP address or CIDR", s)
				}
				p = netip.PrefixFrom(a, a.BitLen())
			}
			c.SourceRange[i] = p.Masked().String()
		}
		v = c
	case "redirect-www":
		if err := decodeStrict(raw, &struct{}{}); err != nil {
			return nil, err
		}
		v = struct{}{}
	case "security-headers":
		c := headersConfig{}
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if c.HSTS && c.HSTSSeconds == 0 {
			c.HSTSSeconds = 31536000
		}
		if c.HSTSSeconds < 0 || c.HSTSSeconds > 63072000 {
			return nil, errors.New("hstsSeconds must be at most two years")
		}
		if !slices.Contains(referrers, c.ReferrerPolicy) {
			return nil, fmt.Errorf("referrerPolicy must be one of %s", strings.Join(referrers[1:], ", "))
		}
		v = c
	case "cors":
		c := corsConfig{}
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if len(c.Origins) == 0 {
			return nil, errors.New("cors needs at least one origin (or *)")
		}
		for _, o := range c.Origins {
			if !originRE.MatchString(o) {
				return nil, fmt.Errorf("origin %q: use * or https://host[:port]", o)
			}
		}
		if len(c.Methods) == 0 {
			c.Methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
		}
		for _, m := range c.Methods {
			if !methodRE.MatchString(m) {
				return nil, fmt.Errorf("method %q is not valid", m)
			}
		}
		for _, h := range c.Headers {
			if !headerRE.MatchString(h) {
				return nil, fmt.Errorf("header %q is not valid", h)
			}
		}
		if c.MaxAge < 0 || c.MaxAge > 86400 {
			return nil, errors.New("maxAge must be between 0 and 86400 seconds")
		}
		v = c
	case "circuit-breaker":
		c := breakerConfig{}
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if c.Expression == "" {
			c.Expression = "NetworkErrorRatio() > 0.30 || ResponseCodeRatio(500, 600, 0, 600) > 0.25"
		}
		if len(c.Expression) > 300 || !breakerRE.MatchString(c.Expression) {
			return nil, errors.New("expression may use NetworkErrorRatio(), ResponseCodeRatio(a, b, c, d), LatencyAtQuantileMS(q), numbers, comparisons, && and ||")
		}
		v = c
	case "compress":
		if err := decodeStrict(raw, &struct{}{}); err != nil {
			return nil, err
		}
		v = struct{}{}
	case "retry":
		c := retryConfig{}
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if c.Attempts < 1 || c.Attempts > 10 {
			return nil, errors.New("attempts must be between 1 (no retry) and 10")
		}
		v = c
	default:
		return nil, fmt.Errorf("type must be one of %s", strings.Join(PresetTypes, ", "))
	}
	out, err := json.Marshal(v)
	return out, err
}

// PublicPreset strips secrets (password hashes) from a stored config.
func PublicPreset(typ string, raw json.RawMessage) json.RawMessage {
	if typ != "basic-auth" {
		return raw
	}
	var c basicAuthConfig
	if json.Unmarshal(raw, &c) != nil {
		return json.RawMessage("{}")
	}
	for i := range c.Users {
		c.Users[i].Hash = ""
	}
	out, _ := json.Marshal(c)
	return out
}

// RenderPreset builds the Traefik middleware for a stored preset.
func RenderPreset(typ string, raw json.RawMessage) (Middleware, error) {
	switch typ {
	case "rate-limit":
		var c rateLimitConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return Middleware{}, err
		}
		return Middleware{RateLimit: &RateLimit{Average: c.Average, Burst: c.Burst, Period: "1s", SourceCriterion: &SourceCriterion{}}}, nil
	case "basic-auth":
		var c basicAuthConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return Middleware{}, err
		}
		ba := &BasicAuth{Realm: c.Realm, RemoveHeader: true}
		for _, u := range c.Users {
			ba.Users = append(ba.Users, u.Username+":"+u.Hash)
		}
		return Middleware{BasicAuth: ba}, nil
	case "ip-allowlist":
		var c allowListConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return Middleware{}, err
		}
		return Middleware{IPAllowList: &IPAllowList{SourceRange: c.SourceRange}}, nil
	case "redirect-www":
		return Middleware{RedirectRegex: &RedirectRegex{Regex: `^(https?)://www\.([^/]+)(.*)`, Replacement: "${1}://${2}${3}", Permanent: true}}, nil
	case "security-headers":
		var c headersConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return Middleware{}, err
		}
		h := &Headers{FrameDeny: c.FrameDeny, ContentTypeNosniff: c.NoSniff, ReferrerPolicy: c.ReferrerPolicy}
		if c.HSTS {
			h.STSSeconds, h.STSIncludeSubdomains = c.HSTSSeconds, true
		}
		return Middleware{Headers: h}, nil
	case "cors":
		var c corsConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return Middleware{}, err
		}
		return Middleware{Headers: &Headers{AccessControlAllowOriginList: c.Origins, AccessControlAllowMethods: c.Methods, AccessControlAllowHeaders: c.Headers,
			AccessControlAllowCredentials: c.Credentials, AccessControlMaxAge: c.MaxAge, AddVaryHeader: true}}, nil
	case "circuit-breaker":
		var c breakerConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return Middleware{}, err
		}
		return Middleware{CircuitBreaker: &CircuitBreaker{Expression: c.Expression}}, nil
	case "compress":
		return Middleware{Compress: &Compress{}}, nil
	case "retry":
		var c retryConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return Middleware{}, err
		}
		return Middleware{Retry: &Retry{Attempts: c.Attempts, InitialInterval: "100ms"}}, nil
	}
	return Middleware{}, fmt.Errorf("unknown middleware type %q", typ)
}

// PresetOrder sorts attached presets the way a route applies them.
func PresetOrder(typ string) int {
	if i := slices.Index(PresetTypes, typ); i >= 0 {
		return i
	}
	return len(PresetTypes)
}
