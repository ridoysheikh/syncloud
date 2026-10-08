package firewall

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// Names of the dynamic sets that record dropped flows (the drop log). Their
// contents change with traffic, so drift detection ignores them.
const (
	SetHostDrops = "host_drops"
	SetInDrops   = "sg_in_drops"
	SetOutDrops  = "sg_out_drops"
	setPlatform  = "sg_platform"
	// Counter names of the built-in security group rules.
	RulePlatform = "builtin:platform"
	RuleInDeny   = "builtin:sg-in-deny"
	RuleOutDeny  = "builtin:sg-out-deny"
	RuleHostDeny = "default deny"
	RuleSpoofed  = "builtin:sg-spoofed"
)

// LocalContainer is a container on this node, classified into security sets
// by its labels.
type LocalContainer struct {
	IP     string
	Labels map[string]string
}

// Security is what an agent needs to render security groups: the compiled
// policy and the node's own containers.
type Security struct {
	Policy *agentv1.SecurityPolicy
	Local  []LocalContainer
}

// Matches reports whether a container with these labels belongs to a set
// selector (see SecuritySet.match).
func Matches(sel string, labels map[string]string) bool {
	kind, v, _ := strings.Cut(sel, ":")
	switch kind {
	case "svc":
		return v != "" && labels["syncloud.service_id"] == v
	case "env":
		p, e, _ := strings.Cut(v, "/")
		return labels["syncloud.project"] == p && labels["syncloud.environment"] == e
	case "proj":
		return labels["syncloud.project"] == v
	case "standalone": // project or project/env
		if labels["syncloud.service_id"] != "" {
			return false
		}
		p, e, withEnv := strings.Cut(v, "/")
		return labels["syncloud.project"] == p && (!withEnv || labels["syncloud.environment"] == e)
	}
	return false
}

var setNameRE = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

func writeDropSet(b *strings.Builder, name, typ string) {
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n\t\tsize 4096\n\t\tflags dynamic,timeout\n\t\ttimeout 10m\n\t\tcounter\n\t}\n", name, typ)
}

// renderSecurity writes the member sets and the sg_in / sg_out chains, and
// returns the rules for the forward chain that send container traffic there.
//
// Same-node traffic is routed too (bridge ports are isolated, §8.3), so it
// passes sg_out on the way out and sg_in on the way in. sg_out therefore
// "returns" on allow instead of accepting, which would skip sg_in.
func renderSecurity(b *strings.Builder, cfg *agentv1.NetworkConfig, sec *Security) (string, error) {
	pol := sec.Policy // platform IPs: every node's mesh address
	subnet, err := netip.ParsePrefix(cfg.GetContainerSubnet())
	if err != nil || !subnet.Addr().Is4() {
		return "", fmt.Errorf("invalid container subnet %q", cfg.GetContainerSubnet())
	}
	known := map[string]bool{}
	for _, s := range pol.GetSets() {
		if !setNameRE.MatchString(s.GetName()) || s.GetName() == setPlatform || strings.HasSuffix(s.GetName(), "_drops") {
			return "", fmt.Errorf("invalid set name %q", s.GetName())
		}
		ips := map[string]bool{}
		for _, ip := range s.GetIps() {
			a, err := netip.ParseAddr(ip)
			if err != nil || !a.Is4() {
				return "", fmt.Errorf("set %s: invalid address %q", s.GetName(), ip)
			}
			ips[a.String()] = true
		}
		for _, c := range sec.Local {
			a, err := netip.ParseAddr(c.IP)
			if err != nil || !a.Is4() {
				continue
			}
			for _, m := range s.GetMatch() {
				if Matches(m, c.Labels) {
					ips[a.String()] = true
				}
			}
		}
		fmt.Fprintf(b, "\tset %s {\n\t\ttype ipv4_addr\n%s\t}\n", s.GetName(), elements(sortedKeys(ips)))
		known[s.GetName()] = true
	}
	var platform []string
	for _, ip := range pol.GetPlatformIps() {
		if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
			platform = append(platform, a.String())
		}
	}
	fmt.Fprintf(b, "\tset %s {\n\t\ttype ipv4_addr\n%s\t}\n", setPlatform, elements(platform))
	writeDropSet(b, SetInDrops, "ipv4_addr . ipv4_addr . inet_proto . inet_service")
	writeDropSet(b, SetOutDrops, "ipv4_addr . ipv4_addr . inet_proto . inet_service")

	var in, out strings.Builder
	fmt.Fprintf(&in, "\t\tip saddr @%s accept comment %q\n", setPlatform, RulePlatform)
	for _, r := range pol.GetRules() {
		lines, err := renderSecurityRule(r, known)
		if err != nil {
			return "", fmt.Errorf("rule %s: %w", r.GetId(), err)
		}
		dst := &in
		if r.GetDirection() == "out" {
			dst = &out
		}
		for _, l := range lines {
			dst.WriteString("\t\t" + l + "\n")
		}
	}
	drop := "\t\tmeta l4proto { tcp, udp } update @%s { ip saddr . ip daddr . meta l4proto . th dport }\n\t\tcounter drop comment %q\n"
	fmt.Fprintf(b, "\tchain sg_in {\n%s"+drop+"\t}\n", in.String(), SetInDrops, RuleInDeny)
	fmt.Fprintf(b, "\tchain sg_out {\n%s"+drop+"\t}\n", out.String(), SetOutDrops, RuleOutDeny)
	// Containers may only send from their own subnet: a spoofed node address
	// would otherwise pass as the platform.
	return fmt.Sprintf("\t\tiifname \"%[1]s\" ip saddr != %[2]s counter drop comment %[3]q\n\t\tct state established,related accept\n"+
		"\t\tiifname \"%[1]s\" jump sg_out\n\t\toifname \"%[1]s\" ip daddr %[2]s jump sg_in\n",
		Bridge, subnet, RuleSpoofed), nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateSecurityRule checks protocol and ports like host rules do.
func ValidateSecurityRule(protocol, ports string) error {
	return ValidateRule(protocol, ports, nil)
}

func renderSecurityRule(r *agentv1.SecurityRule, known map[string]bool) ([]string, error) {
	if err := ValidateRule(r.GetProtocol(), r.GetPorts(), nil); err != nil {
		return nil, err
	}
	if !idRE.MatchString(r.GetId()) {
		return nil, fmt.Errorf("invalid rule id")
	}
	local, peer, verdict := "daddr", "saddr", "accept"
	if r.GetDirection() == "out" {
		local, peer, verdict = "saddr", "daddr", "return"
	} else if r.GetDirection() != "in" {
		return nil, fmt.Errorf("direction must be in or out")
	}
	if !known[r.GetLocalSet()] {
		return nil, fmt.Errorf("unknown set %q", r.GetLocalSet())
	}
	var match string
	switch p := r.GetProtocol(); p {
	case "tcp", "udp":
		if r.GetPorts() != "" {
			match = " " + p + " dport " + r.GetPorts()
		} else {
			match = " meta l4proto " + p
		}
	case "icmp":
		match = " meta l4proto icmp"
	}
	head := fmt.Sprintf("ip %s @%s", local, r.GetLocalSet())
	tail := fmt.Sprintf("%s counter %s comment %q", match, verdict, r.GetId())
	var lines []string
	for _, s := range r.GetPeerSets() {
		if !known[s] {
			return nil, fmt.Errorf("unknown set %q", s)
		}
		lines = append(lines, fmt.Sprintf("%s ip %s @%s%s", head, peer, s, tail))
	}
	var cidrs []string
	for _, c := range r.GetPeerCidrs() {
		p, err := parseSource(c)
		if err != nil || !p.Addr().Is4() {
			return nil, fmt.Errorf("invalid IPv4 CIDR %q", c)
		}
		if p.Bits() == 0 {
			lines = append(lines, head+tail) // anywhere
			cidrs = nil
			break
		}
		cidrs = append(cidrs, p.String())
	}
	if len(cidrs) > 0 {
		lines = append(lines, fmt.Sprintf("%s ip %s { %s }%s", head, peer, strings.Join(cidrs, ", "), tail))
	}
	return lines, nil
}

// StripDynamic removes what changes with traffic from an `nft -s list
// table` listing (the drop sets' elements and counts), so drift detection
// only sees configuration changes.
func StripDynamic(listing string) string {
	var out []string
	inDrops, inElems := false, false
	for _, l := range strings.Split(listing, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "set ") && strings.Contains(t, "_drops {"):
			inDrops = true
		case inDrops && t == "}":
			inDrops, inElems = false, false
		case inDrops && strings.HasPrefix(t, "elements = {"):
			inElems = !strings.HasSuffix(t, "}")
			continue
		case inElems:
			inElems = !strings.HasSuffix(t, "}")
			continue
		}
		if inDrops {
			if i := strings.Index(l, "\t# count"); i >= 0 {
				l = l[:i]
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// Counts are a rule's packets and bytes.
type Counts struct{ Packets, Bytes uint64 }

// DropKey identifies a dropped flow.
type DropKey struct {
	Direction, Src, Dst, Protocol string
	Port                          uint32
}

// ParseCounters reads `nft -j list table inet syncloud`: rule counters by
// comment (summed over rules with the same comment) and the drop sets'
// per-flow packet counts.
func ParseCounters(data []byte) (map[string]Counts, map[DropKey]uint64, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	rules := map[string]Counts{}
	drops := map[DropKey]uint64{}
	for _, obj := range doc.Nftables {
		if raw, ok := obj["rule"]; ok {
			var r struct {
				Comment string                       `json:"comment"`
				Expr    []map[string]json.RawMessage `json:"expr"`
			}
			if json.Unmarshal(raw, &r) != nil || r.Comment == "" {
				continue
			}
			for _, e := range r.Expr {
				if c, ok := e["counter"]; ok {
					var n struct{ Packets, Bytes uint64 }
					if json.Unmarshal(c, &n) == nil {
						x := rules[r.Comment]
						x.Packets += n.Packets
						x.Bytes += n.Bytes
						rules[r.Comment] = x
					}
				}
			}
		}
		if raw, ok := obj["set"]; ok {
			var s struct {
				Name string `json:"name"`
				Elem []struct {
					Elem struct {
						Val struct {
							Concat []json.RawMessage `json:"concat"`
						} `json:"val"`
						Counter struct{ Packets uint64 } `json:"counter"`
					} `json:"elem"`
				} `json:"elem"`
			}
			if json.Unmarshal(raw, &s) != nil {
				continue
			}
			dir := map[string]string{SetHostDrops: "host", SetInDrops: "in", SetOutDrops: "out"}[s.Name]
			if dir == "" {
				continue
			}
			for _, e := range s.Elem {
				var parts []string
				var port uint32
				for _, c := range e.Elem.Val.Concat {
					var str string
					if json.Unmarshal(c, &str) == nil {
						parts = append(parts, str)
					} else {
						_ = json.Unmarshal(c, &port)
					}
				}
				k := DropKey{Direction: dir, Port: port}
				switch {
				case dir == "host" && len(parts) == 2:
					k.Src, k.Protocol = parts[0], parts[1]
				case dir != "host" && len(parts) == 3:
					k.Src, k.Dst, k.Protocol = parts[0], parts[1], parts[2]
				default:
					continue
				}
				drops[k] += e.Elem.Counter.Packets
			}
		}
	}
	return rules, drops, nil
}
