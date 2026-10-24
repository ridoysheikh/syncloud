package firewall

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

// internalIfaces are interfaces traffic never comes from the internet on:
// loopback, the mesh and the local container bridges ("br-*" is matched
// separately, nftables sets cannot hold wildcards).
var internalIfaces = fmt.Sprintf("%q, %q, %q, %q", "lo", Interface, Bridge, "docker0")

// ValidatePorts checks a port list: numbers and ranges separated by commas.
func ValidatePorts(ports string) error {
	if ports == "" {
		return errors.New("ports are required")
	}
	for _, p := range strings.Split(ports, ",") {
		if err := ValidateRule("tcp", strings.TrimSpace(p), nil); err != nil {
			return err
		}
	}
	return nil
}

// portSet renders a port list as an nftables value or anonymous set.
func portSet(ports string) string {
	parts := strings.Split(ports, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// ValidateGuard checks a guard before it is rendered.
func ValidateGuard(g *agentv1.FirewallGuard) error {
	if !idRE.MatchString(g.GetId()) {
		return errors.New("invalid guard id")
	}
	if p := g.GetProtocol(); p != "tcp" && p != "udp" {
		return errors.New("guard protocol must be tcp or udp")
	}
	if err := ValidatePorts(g.GetPorts()); err != nil {
		return err
	}
	if g.GetRate() > 0 {
		if u := g.GetUnit(); u != "second" && u != "minute" {
			return errors.New("guard unit must be second or minute")
		}
		if g.GetRate() > 1_000_000 || g.GetBurst() > 1_000_000 {
			return errors.New("guard rate and burst must be at most 1000000")
		}
	}
	if g.GetMaxConnections() > 1_000_000 {
		return errors.New("guard connections must be at most 1000000")
	}
	if g.GetRate() == 0 && g.GetMaxConnections() == 0 {
		return errors.New("a guard needs a rate or a connection limit")
	}
	for _, s := range g.GetExempt() {
		if _, err := parseSource(s); err != nil {
			return fmt.Errorf("exempt source %q: use an IP or a CIDR", s)
		}
	}
	return nil
}

// splitFamilies sorts sources into IPv4 and IPv6 prefixes.
func splitFamilies(sources []string) (v4, v6 []string) {
	for _, s := range sources {
		p, err := parseSource(s)
		if err != nil {
			continue
		}
		if p.Addr().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	return v4, v6
}

// renderGuards writes each guard's rules into the input chain (b) and its
// sets into sets. A source over a limit has its new connections dropped and
// is recorded in the limit drop sets, which the agent reports.
func renderGuards(b, sets *strings.Builder, guards []*agentv1.FirewallGuard) error {
	for i, g := range guards {
		if err := ValidateGuard(g); err != nil {
			return fmt.Errorf("guard %s: %w", g.GetId(), err)
		}
		ex4, ex6 := splitFamilies(g.GetExempt())
		match := fmt.Sprintf("%s dport %s ct state new", g.GetProtocol(), portSet(g.GetPorts()))
		for _, fam := range []struct {
			ip, addr, suffix, drops string
			exempt                  []string
		}{
			{"ip", "ipv4_addr", "4", SetLimitDrops, ex4},
			{"ip6", "ipv6_addr", "6", SetLimitDrops6, ex6},
		} {
			cond := match
			if len(fam.exempt) > 0 {
				name := fmt.Sprintf("g%d_x%s", i, fam.suffix)
				fmt.Fprintf(sets, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n%s\t}\n", name, fam.addr, elements(fam.exempt))
				cond += fmt.Sprintf(" %s saddr != @%s", fam.ip, name)
			}
			record := fmt.Sprintf("update @%s { %s saddr . meta l4proto . th dport } counter drop comment %q", fam.drops, fam.ip, g.GetId())
			if g.GetRate() > 0 {
				name := fmt.Sprintf("g%d_r%s", i, fam.suffix)
				timeout := "10s"
				if g.GetUnit() == "minute" {
					timeout = "2m" // longer than the window, or a slow scanner resets its bucket
				}
				fmt.Fprintf(sets, "\tset %s {\n\t\ttype %s\n\t\tsize 65535\n\t\tflags dynamic,timeout\n\t\ttimeout %s\n\t}\n", name, fam.addr, timeout)
				burst := g.GetBurst()
				if burst == 0 {
					burst = 1
				}
				fmt.Fprintf(b, "\t\t%s add @%s { %s saddr limit rate over %d/%s burst %d packets } %s\n",
					cond, name, fam.ip, g.GetRate(), g.GetUnit(), burst, record)
			}
			if g.GetMaxConnections() > 0 {
				name := fmt.Sprintf("g%d_c%s", i, fam.suffix)
				fmt.Fprintf(sets, "\tset %s {\n\t\ttype %s\n\t\tsize 65535\n\t\tflags dynamic\n\t}\n", name, fam.addr)
				fmt.Fprintf(b, "\t\t%s add @%s { %s saddr ct count over %d } %s\n", cond, name, fam.ip, g.GetMaxConnections(), record)
			}
		}
	}
	return nil
}

// ValidateBlock checks a blocked source: an IP or a CIDR, never everything.
func ValidateBlock(source string) (netip.Prefix, error) {
	p, err := parseSource(source)
	if err != nil {
		return p, fmt.Errorf("%q is not an IP or a CIDR", source)
	}
	if p.Addr().Is4() && p.Bits() < 8 || !p.Addr().Is4() && p.Bits() < 16 {
		return p, errors.New("a block may cover at most a /8 (IPv4) or /16 (IPv6)")
	}
	return p, nil
}

// renderBlocks writes the blocked sources and a prerouting chain that drops
// them on public interfaces before connection tracking (cheap under a flood).
func renderBlocks(b *strings.Builder, blocks []*agentv1.FirewallBlock) error {
	var v4, v6 []string
	for _, bl := range blocks {
		p, err := ValidateBlock(bl.GetSource())
		if err != nil {
			return err
		}
		e := p.String()
		if p.IsSingleIP() {
			e = p.Addr().String()
		}
		if t := bl.GetTimeoutSeconds(); t > 0 {
			e += fmt.Sprintf(" timeout %ds", t)
		}
		if p.Addr().Is4() {
			v4 = append(v4, e)
		} else {
			v6 = append(v6, e)
		}
	}
	fmt.Fprintf(b, "\tset blocked4 {\n\t\ttype ipv4_addr\n\t\tflags interval,timeout\n%s\t}\n", elements(v4))
	fmt.Fprintf(b, "\tset blocked6 {\n\t\ttype ipv6_addr\n\t\tflags interval,timeout\n%s\t}\n", elements(v6))
	fmt.Fprintf(b, "\tchain blocked {\n\t\ttype filter hook prerouting priority raw; policy accept;\n"+
		"\t\tiifname { %s } accept\n\t\tiifname \"br-*\" accept\n"+
		"\t\tip saddr @blocked4 counter drop comment %q\n\t\tip6 saddr @blocked6 counter drop comment %q\n\t}\n",
		internalIfaces, RuleBlocked, RuleBlocked)
	return nil
}
