// Package firewall validates host firewall rules and renders the agent's
// nftables table (§8.3). The controller uses the same rendering to show the
// effective rules of any node.
package firewall

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

// Names the rendered table refers to (kept in sync with the agent).
const (
	Interface = "wg-syncloud"
	Bridge    = "syncloud0"
	// SourceCluster matches the addresses of all cluster nodes.
	SourceCluster = "cluster"
)

var (
	portsRE = regexp.MustCompile(`^(\d{1,5})(-(\d{1,5}))?$`)
	idRE    = regexp.MustCompile(`^[a-z0-9_:.-]{1,64}$`)
)

// ValidateRule checks one rule. Everything in a rule ends up in nftables
// syntax, so only strictly parsed values are accepted.
func ValidateRule(protocol, ports string, sources []string) error {
	switch protocol {
	case "tcp", "udp":
	case "icmp", "any":
		if ports != "" {
			return fmt.Errorf("ports only apply to tcp and udp")
		}
	default:
		return fmt.Errorf("protocol must be tcp, udp, icmp or any")
	}
	if ports != "" {
		m := portsRE.FindStringSubmatch(ports)
		if m == nil {
			return fmt.Errorf("ports must be a number or a range like 8000-8100")
		}
		lo, _ := strconv.Atoi(m[1])
		hi := lo
		if m[3] != "" {
			hi, _ = strconv.Atoi(m[3])
		}
		if lo < 1 || hi > 65535 || lo > hi {
			return fmt.Errorf("ports must be between 1 and 65535")
		}
	}
	for _, s := range sources {
		if s == SourceCluster {
			continue
		}
		if _, err := parseSource(s); err != nil {
			return fmt.Errorf("source %q: use an IP, a CIDR or %q", s, SourceCluster)
		}
	}
	return nil
}

func parseSource(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// Render returns the complete "inet syncloud" table for cfg. It replaces the
// table atomically when fed to `nft -f`. fw may differ from cfg.Firewall
// (after a rollback the agent renders the last confirmed firewall).
func Render(cfg *agentv1.NetworkConfig, fw *agentv1.Firewall) (string, error) {
	var b strings.Builder
	internal := strings.Join([]string{cfg.GetMeshCidr(), cfg.GetContainerCidr(), cfg.GetServiceCidr()}, ", ")
	// "ip syncloud" was the table name before the host firewall existed.
	b.WriteString("table ip syncloud\ndelete table ip syncloud\ntable inet syncloud\ndelete table inet syncloud\n")
	b.WriteString("table inet syncloud {\n")

	if fw != nil {
		var v4, v6 []string
		for _, s := range fw.GetClusterSources() {
			if a, err := netip.ParseAddr(s); err == nil {
				if a.Is4() || a.Is4In6() {
					v4 = append(v4, a.Unmap().String())
				} else {
					v6 = append(v6, a.String())
				}
			}
		}
		fmt.Fprintf(&b, "\tset cluster4 {\n\t\ttype ipv4_addr\n%s\t}\n", elements(v4))
		fmt.Fprintf(&b, "\tset cluster6 {\n\t\ttype ipv6_addr\n%s\t}\n", elements(v6))
		b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter - 10; policy accept;\n")
		b.WriteString("\t\tct state established,related accept\n")
		b.WriteString("\t\tct state invalid drop\n")
		b.WriteString("\t\tiifname \"lo\" accept\n")
		fmt.Fprintf(&b, "\t\tiifname { \"%s\", \"%s\", \"docker0\" } accept comment \"mesh and local containers\"\n", Interface, Bridge)
		b.WriteString("\t\tiifname \"br-*\" accept comment \"other Docker networks\"\n")
		b.WriteString("\t\tip protocol icmp icmp type { echo-request, destination-unreachable, time-exceeded, parameter-problem } accept\n")
		b.WriteString("\t\tmeta l4proto ipv6-icmp accept comment \"neighbor discovery and path MTU\"\n")
		port := cfg.GetListenPort()
		fmt.Fprintf(&b, "\t\tudp dport %d ip saddr @cluster4 accept comment \"builtin:wireguard\"\n", port)
		fmt.Fprintf(&b, "\t\tudp dport %d ip6 saddr @cluster6 accept comment \"builtin:wireguard\"\n", port)
		for _, r := range fw.GetRules() {
			lines, err := renderRule(r)
			if err != nil {
				return "", fmt.Errorf("rule %s: %w", r.GetId(), err)
			}
			for _, l := range lines {
				b.WriteString("\t\t" + l + "\n")
			}
		}
		b.WriteString("\t\tcounter drop comment \"default deny\"\n\t}\n")
	}

	fmt.Fprintf(&b, `	chain forward {
		type filter hook forward priority filter - 10; policy accept;
		# Container IPs are only reachable from the mesh and the local bridge.
		ip daddr %[1]s iifname != { "%[2]s", "%[3]s" } ct state new drop
	}
	chain postrouting {
		type nat hook postrouting priority srcnat + 10; policy accept;
		# No NAT inside the private network; masquerade to the internet.
		ip saddr %[4]s ip daddr != { %[5]s } oifname != "%[3]s" masquerade
	}
}
`, cfg.GetContainerCidr(), Interface, Bridge, cfg.GetContainerSubnet(), internal)
	return b.String(), nil
}

func elements(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return "\t\telements = { " + strings.Join(xs, ", ") + " }\n"
}

// renderRule turns one rule into nftables rules (one per address family when
// sources mix IPv4 and IPv6).
func renderRule(r *agentv1.FirewallRule) ([]string, error) {
	if err := ValidateRule(r.GetProtocol(), r.GetPorts(), r.GetSources()); err != nil {
		return nil, err
	}
	if !idRE.MatchString(r.GetId()) {
		return nil, errors.New("invalid rule id")
	}
	var match string
	switch p := r.GetProtocol(); p {
	case "tcp", "udp":
		if r.GetPorts() != "" {
			match = p + " dport " + r.GetPorts()
		} else {
			match = "meta l4proto " + p
		}
	case "icmp":
		match = "meta l4proto { icmp, ipv6-icmp }"
	}
	suffix := fmt.Sprintf(" counter accept comment %q", r.GetId())
	join := func(parts ...string) string {
		var out []string
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, " ") + suffix
	}
	if len(r.GetSources()) == 0 {
		return []string{join(match)}, nil
	}
	var v4, v6 []string
	var lines []string
	for _, s := range r.GetSources() {
		if s == SourceCluster {
			lines = append(lines, join("ip saddr @cluster4", match), join("ip6 saddr @cluster6", match))
			continue
		}
		p, _ := parseSource(s)
		if p.Addr().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	if len(v4) > 0 {
		lines = append(lines, join("ip saddr { "+strings.Join(v4, ", ")+" }", match))
	}
	if len(v6) > 0 {
		lines = append(lines, join("ip6 saddr { "+strings.Join(v6, ", ")+" }", match))
	}
	return lines, nil
}
