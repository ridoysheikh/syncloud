package traefik

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Custom configuration (§5.7 "Advanced"): raw Traefik dynamic configuration
// for what the UI does not cover. Traefik rejects a whole configuration it
// cannot decode and keeps the previous one, which would freeze every route,
// so the YAML is checked strictly before it is stored: known sections and
// fields with the right types, names that cannot collide with generated
// ones, and references that resolve.

const maxCustom = 64 << 10

var customNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

// Reserved prefixes of generated names.
var reservedPrefixes = []string{"svc-", "dom-", "mw-", "syncloud-"}

// MiddlewareKinds are Traefik's HTTP middleware types.
var MiddlewareKinds = []string{
	"addPrefix", "basicAuth", "buffering", "chain", "circuitBreaker", "compress", "contentType", "digestAuth", "errors",
	"forwardAuth", "grpcWeb", "headers", "ipAllowList", "inFlightReq", "passTLSClientCert", "rateLimit", "redirectRegex",
	"redirectScheme", "replacePath", "replacePathRegex", "retry", "stripPrefix", "stripPrefixRegex",
}

var tcpMiddlewareKinds = []string{"ipAllowList", "inFlightConn"}

type fieldKind int

const (
	kString fieldKind = iota
	kInt
	kBool
	kStrings
	kMap // nested object, passed through
	kList
)

var routerFields = map[string]fieldKind{
	"rule": kString, "ruleSyntax": kString, "service": kString, "priority": kInt, "entryPoints": kStrings, "middlewares": kStrings, "tls": kMap, "observability": kMap,
}

var tcpRouterFields = map[string]fieldKind{"rule": kString, "service": kString, "priority": kInt, "entryPoints": kStrings, "middlewares": kStrings, "tls": kMap}

var udpRouterFields = map[string]fieldKind{"service": kString, "entryPoints": kStrings}

var serviceKinds = map[string]map[string]fieldKind{
	"loadBalancer": {"servers": kList, "sticky": kMap, "healthCheck": kMap, "passHostHeader": kBool, "responseForwarding": kMap, "serversTransport": kString},
	"weighted":     {"services": kList, "sticky": kMap, "healthCheck": kMap},
	"mirroring":    {"service": kString, "mirrors": kList, "maxBodySize": kInt, "mirrorBody": kBool, "healthCheck": kMap},
	"failover":     {"service": kString, "fallback": kString, "healthCheck": kMap},
}

// ValidateCustom parses and checks custom YAML against the generated
// configuration gen (as served, without custom parts). It returns the
// configuration to merge.
func ValidateCustom(text string, gen map[string]any) (map[string]any, error) {
	if len(text) > maxCustom {
		return nil, fmt.Errorf("the configuration is larger than %d KiB", maxCustom>>10)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("YAML: %v", err)
	}
	if doc == nil {
		return map[string]any{}, nil
	}
	allowedSections := map[string][]string{
		"http": {"routers", "services", "middlewares", "serversTransports"},
		"tcp":  {"routers", "services", "middlewares", "serversTransports"},
		"udp":  {"routers", "services"},
	}
	names := func(proto, sec string) map[string]bool {
		out := map[string]bool{}
		for _, src := range []map[string]any{gen, doc} {
			if p, ok := src[proto].(map[string]any); ok {
				if s, ok := p[sec].(map[string]any); ok {
					for n := range s {
						out[n] = true
					}
				}
			}
		}
		return out
	}
	var errs []string
	fail := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	for _, proto := range sortedKeys(doc) {
		secs, ok := doc[proto].(map[string]any)
		if _, known := allowedSections[proto]; !known || !ok {
			fail("%s: only http, tcp and udp sections are allowed (certificates are managed by the controller)", proto)
			continue
		}
		for _, sec := range sortedKeys(secs) {
			if !slices.Contains(allowedSections[proto], sec) {
				fail("%s.%s: unknown section (use %s)", proto, sec, strings.Join(allowedSections[proto], ", "))
				continue
			}
			entries, ok := secs[sec].(map[string]any)
			if !ok {
				fail("%s.%s: must be a mapping of names", proto, sec)
				continue
			}
			for _, name := range sortedKeys(entries) {
				where := proto + "." + sec + "." + name
				if !customNameRE.MatchString(name) {
					fail("%s: names are letters, digits, - and _", where)
				}
				for _, p := range reservedPrefixes {
					if strings.HasPrefix(name, p) {
						fail("%s: names starting with %s are reserved for SynCloud", where, p)
					}
				}
				if g, ok := gen[proto].(map[string]any); ok {
					if s, ok := g[sec].(map[string]any); ok {
						if _, taken := s[name]; taken {
							fail("%s: already defined by SynCloud", where)
						}
					}
				}
				e, ok := entries[name].(map[string]any)
				if !ok {
					fail("%s: must be a mapping", where)
					continue
				}
				switch sec {
				case "routers":
					fields := map[string]map[string]fieldKind{"http": routerFields, "tcp": tcpRouterFields, "udp": udpRouterFields}[proto]
					checkFields(where, e, fields, fail)
					svc, _ := e["service"].(string)
					if svc == "" {
						fail("%s: needs a service", where)
					} else if strings.Contains(svc, "@") {
						fail("%s: service %q: provider services (@internal, …) are not allowed", where, svc)
					} else if !names(proto, "services")[svc] {
						fail("%s: service %q is not defined", where, svc)
					}
					if proto != "udp" {
						if r, _ := e["rule"].(string); r == "" {
							fail("%s: needs a rule", where)
						}
					}
					if eps, ok := e["entryPoints"].([]any); ok {
						for _, ep := range eps {
							if s, _ := ep.(string); s != "web" && s != "websecure" {
								fail("%s: entry point %v does not exist (web, websecure)", where, ep)
							}
						}
					}
					if mws, ok := e["middlewares"].([]any); ok {
						known := names(proto, "middlewares")
						for _, mw := range mws {
							if s, _ := mw.(string); !known[s] {
								fail("%s: middleware %v is not defined", where, mw)
							}
						}
					}
				case "middlewares":
					kinds := MiddlewareKinds
					if proto == "tcp" {
						kinds = tcpMiddlewareKinds
					}
					if len(e) != 1 {
						fail("%s: a middleware has exactly one type (%s)", where, strings.Join(kinds, ", "))
						continue
					}
					for k, v := range e {
						if !slices.Contains(kinds, k) {
							fail("%s: unknown middleware type %q", where, k)
						} else if _, ok := v.(map[string]any); !ok && v != nil {
							fail("%s.%s: must be a mapping", where, k)
						}
					}
				case "services":
					if len(e) != 1 {
						fail("%s: a service has exactly one of loadBalancer, weighted, mirroring, failover", where)
						continue
					}
					for k, v := range e {
						fields, ok := serviceKinds[k]
						if !ok {
							fail("%s: unknown service type %q", where, k)
							continue
						}
						m, ok := v.(map[string]any)
						if !ok {
							fail("%s.%s: must be a mapping", where, k)
							continue
						}
						checkFields(where+"."+k, m, fields, fail)
						if k == "loadBalancer" {
							servers, _ := m["servers"].([]any)
							if len(servers) == 0 {
								fail("%s: a load balancer needs servers", where)
							}
							for i, s := range servers {
								sm, ok := s.(map[string]any)
								if !ok {
									fail("%s: server %d must be a mapping", where, i+1)
									continue
								}
								addr, _ := sm["url"].(string)
								if addr == "" {
									addr, _ = sm["address"].(string)
								}
								if addr == "" {
									fail("%s: server %d needs a url (http) or address (tcp, udp)", where, i+1)
								}
							}
						}
					}
				case "serversTransports":
					// passed through: options only
				}
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return doc, nil
}

func checkFields(where string, e map[string]any, fields map[string]fieldKind, fail func(string, ...any)) {
	for _, k := range sortedKeys(e) {
		kind, ok := fields[k]
		if !ok {
			fail("%s: unknown field %q", where, k)
			continue
		}
		v := e[k]
		bad := false
		switch kind {
		case kString:
			_, ok := v.(string)
			bad = !ok
		case kInt:
			_, ok := v.(int)
			bad = !ok
		case kBool:
			_, ok := v.(bool)
			bad = !ok
		case kStrings:
			list, ok := v.([]any)
			bad = !ok
			for _, x := range list {
				if _, ok := x.(string); !ok {
					bad = true
				}
			}
		case kMap:
			_, ok := v.(map[string]any)
			bad = !ok && v != nil
		case kList:
			_, ok := v.([]any)
			bad = !ok
		}
		if bad {
			fail("%s.%s: wrong type", where, k)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
