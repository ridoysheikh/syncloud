package dns

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
)

// DefaultResolvers are the public resolvers the lookup tool asks.
var DefaultResolvers = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

// Answer is what one server said.
type Answer struct {
	Server string   `json:"server"`
	Kind   string   `json:"kind"` // public | authoritative
	Rcode  string   `json:"rcode"`
	Values []string `json:"values"`
	TTL    uint32   `json:"ttl,omitempty"`
	Error  string   `json:"error,omitempty"`
}

var qtypes = map[string]uint16{
	"A": mdns.TypeA, "AAAA": mdns.TypeAAAA, "CNAME": mdns.TypeCNAME, "TXT": mdns.TypeTXT, "MX": mdns.TypeMX,
	"NS": mdns.TypeNS, "SRV": mdns.TypeSRV, "CAA": mdns.TypeCAA, "SOA": mdns.TypeSOA,
}

// QueryTypes lists the types Lookup knows.
func QueryTypes() []string {
	out := make([]string, 0, len(qtypes))
	for t := range qtypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Query asks one server for one name and type.
func Query(ctx context.Context, server, name, qtype string) Answer {
	a := Answer{Server: server, Values: []string{}}
	t, ok := qtypes[strings.ToUpper(qtype)]
	if !ok {
		a.Error = "unknown record type " + qtype
		return a
	}
	if !strings.Contains(server, ":") || strings.Count(server, ":") > 1 && !strings.HasPrefix(server, "[") {
		server = net.JoinHostPort(strings.Trim(server, "[]"), "53")
	}
	m := new(mdns.Msg)
	m.SetQuestion(mdns.Fqdn(Fqdn(name)), t)
	m.RecursionDesired = true
	c := &mdns.Client{Timeout: 4 * time.Second}
	dl, ok := ctx.Deadline()
	if ok {
		if d := time.Until(dl); d > 0 && d < c.Timeout {
			c.Timeout = d
		}
	}
	in, _, err := c.ExchangeContext(ctx, m, server)
	if err != nil {
		a.Error = err.Error()
		return a
	}
	if in.Truncated {
		tc := &mdns.Client{Net: "tcp", Timeout: c.Timeout}
		if in2, _, err := tc.ExchangeContext(ctx, m, server); err == nil {
			in = in2
		}
	}
	a.Rcode = mdns.RcodeToString[in.Rcode]
	for _, rr := range in.Answer {
		a.TTL = rr.Header().Ttl
		switch v := rr.(type) {
		case *mdns.A:
			a.Values = append(a.Values, v.A.String())
		case *mdns.AAAA:
			a.Values = append(a.Values, v.AAAA.String())
		case *mdns.CNAME:
			a.Values = append(a.Values, Fqdn(v.Target))
		case *mdns.TXT:
			a.Values = append(a.Values, strings.Join(v.Txt, ""))
		case *mdns.MX:
			a.Values = append(a.Values, strconv.Itoa(int(v.Preference))+" "+Fqdn(v.Mx))
		case *mdns.NS:
			a.Values = append(a.Values, Fqdn(v.Ns))
		case *mdns.SRV:
			a.Values = append(a.Values, fmt.Sprintf("%d %d %d %s", v.Priority, v.Weight, v.Port, Fqdn(v.Target)))
		case *mdns.CAA:
			a.Values = append(a.Values, fmt.Sprintf("%d %s %q", v.Flag, v.Tag, v.Value))
		case *mdns.SOA:
			a.Values = append(a.Values, Fqdn(v.Ns)+" "+Fqdn(v.Mbox))
		}
	}
	sort.Strings(a.Values)
	return a
}

// AuthoritativeServers finds the name servers of the zone a name is in,
// walking up the labels until some resolver names them.
func AuthoritativeServers(ctx context.Context, resolvers []string, name string) (zone string, servers []string) {
	name = Fqdn(name)
	labels := strings.Split(name, ".")
	for i := 0; i < len(labels)-1; i++ {
		cand := strings.Join(labels[i:], ".")
		for _, r := range resolvers {
			a := Query(ctx, r, cand, "NS")
			if len(a.Values) == 0 {
				continue
			}
			for _, ns := range a.Values {
				if ips, err := (&net.Resolver{}).LookupHost(ctx, ns); err == nil && len(ips) > 0 {
					servers = append(servers, net.JoinHostPort(ips[0], "53"))
				}
			}
			if len(servers) > 0 {
				return cand, servers
			}
		}
	}
	return "", nil
}

// Lookup asks public resolvers, the zone's authoritative servers, or both.
func Lookup(ctx context.Context, resolvers []string, name, qtype, via string) []Answer {
	if len(resolvers) == 0 {
		resolvers = DefaultResolvers
	}
	var out []Answer
	if via == "" || via == "public" {
		for _, r := range resolvers {
			a := Query(ctx, r, name, qtype)
			a.Kind = "public"
			out = append(out, a)
		}
	}
	if via == "" || via == "authoritative" {
		_, servers := AuthoritativeServers(ctx, resolvers, name)
		for _, s := range servers {
			a := Query(ctx, s, name, qtype)
			a.Kind = "authoritative"
			out = append(out, a)
		}
	}
	return out
}

// HasTXT reports whether every value is among the TXT answers at name,
// asking each resolver given: a value must be visible to all of them.
func HasTXT(ctx context.Context, resolvers []string, name string, values []string) (missing []string, err error) {
	if len(resolvers) == 0 {
		resolvers = DefaultResolvers
	}
	seen := map[string]int{}
	asked := 0
	for _, r := range resolvers {
		a := Query(ctx, r, name, "TXT")
		if a.Error != "" {
			continue
		}
		asked++
		for _, v := range a.Values {
			seen[v]++
		}
	}
	if asked == 0 {
		return values, fmt.Errorf("no resolver answered")
	}
	for _, v := range values {
		if seen[v] < asked {
			missing = append(missing, v)
		}
	}
	return missing, nil
}
