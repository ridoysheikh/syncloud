package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ridoysheikh/syncloud/internal/client"
)

func sgRuleText(r client.SecurityRule, dir string) string {
	what := r.Protocol
	if r.Ports != "" {
		what += " " + r.Ports
	}
	word := "from"
	if dir == "out" {
		word = "to"
	}
	return what + " " + word + " " + strings.Join(r.Peers, ",")
}

func (a *app) securityGroupsCmd() *cobra.Command {
	var s scope
	sg := &cobra.Command{Use: "sg", Aliases: []string{"security-groups", "securitygroups"}, Short: "Security groups: allow rules between containers (§8.3)",
		Long: "Each project has a default group (services in the same environment can reach each other, all outbound traffic is allowed).\n" +
			"A service uses the groups attached to it, or its project's default group. Rules only allow; everything else is dropped.\n" +
			"Peers: any, cluster, an IPv4 address or CIDR, group:[project/]name, service:[project/]env/name,\n" +
			"environment:self|[project/]env, project:self|name."}
	a.scopeFlags(sg, &s)

	sg.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List security groups (all projects without --project)", Args: cobra.NoArgs,
		Annotations: op("listAllSecurityGroups", "listSecurityGroups"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var gs []client.SecurityGroup
			var nodeErrs map[string]string
			if s.project != "" {
				gs, err = c.ListSecurityGroups(ctx(cmd), s.project)
			} else {
				gs, nodeErrs, err = c.ListAllSecurityGroups(ctx(cmd))
			}
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(gs))
			for _, g := range gs {
				name := g.Name
				if g.Default {
					name += " (default)"
				}
				rows = append(rows, []string{g.Project, name, strconv.Itoa(len(g.Inbound)), strconv.Itoa(len(g.Outbound)), joinOrDash(g.Members)})
			}
			if err := a.printer().table(gs, []string{"PROJECT", "NAME", "IN", "OUT", "MEMBERS"}, rows); err != nil {
				return err
			}
			for n, e := range nodeErrs {
				fmt.Fprintf(a.errOut, "warning: node %s does not enforce security groups: %s\n", n, e)
			}
			return nil
		},
	})
	sg.AddCommand(&cobra.Command{
		Use: "get NAME", Short: "Show a group as JSON, with rule hit counters (edit and re-apply with apply -f)", Args: cobra.ExactArgs(1),
		Annotations: op("getSecurityGroup"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.need(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			g, err := c.GetSecurityGroup(ctx(cmd), s.project, args[0])
			if err != nil {
				return err
			}
			return a.printer().json(g)
		},
	})

	var file string
	var dryRun bool
	apply := &cobra.Command{
		Use: "apply -f FILE", Short: "Create or update a group from JSON (matched by name); --dry-run shows what would change", Args: cobra.NoArgs,
		Annotations: op("createSecurityGroup", "updateSecurityGroup", "previewSecurityGroup"),
		Example: `  cat <<'JSON' | synctl sg apply -p shop -f -
  {"name": "db", "description": "Postgres",
   "inbound": [{"protocol": "tcp", "ports": "5432", "peers": ["environment:self", "service:billing/production/worker"]}],
   "outbound": [{"protocol": "udp", "ports": "53", "peers": ["any"]}],
   "services": ["production/db"]}
  JSON`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := s.need(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var r io.Reader = a.in
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				r = f
			}
			var g client.SecurityGroup
			dec := json.NewDecoder(r)
			if err := dec.Decode(&g); err != nil {
				return fmt.Errorf("parse group: %w", err)
			}
			if g.Name == "" {
				return errors.New("the group needs a name")
			}
			existing := ""
			if _, err := c.GetSecurityGroup(ctx(cmd), s.project, g.Name); err == nil {
				existing = g.Name
			}
			changes, err := c.PreviewSecurityGroup(ctx(cmd), s.project, existing, g)
			if err != nil {
				return err
			}
			if a.output == "json" && dryRun {
				return a.printer().json(changes)
			}
			if len(changes) == 0 {
				fmt.Fprintln(a.out, "No service's effective rules change.")
			}
			for _, ch := range changes {
				fmt.Fprintf(a.out, "%s (%d tasks on %s)\n", ch.Service, ch.Tasks, joinOrDash(ch.Nodes))
				for _, l := range ch.Lines {
					fmt.Fprintln(a.out, "  "+l)
				}
			}
			if dryRun {
				return nil
			}
			verb := "Created"
			if existing != "" {
				_, err = c.UpdateSecurityGroup(ctx(cmd), s.project, existing, g)
				verb = "Updated"
			} else {
				_, err = c.CreateSecurityGroup(ctx(cmd), s.project, g)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s security group %s/%s. Nodes enforce it within seconds.\n", verb, s.project, g.Name)
			return nil
		},
	}
	apply.Flags().StringVarP(&file, "file", "f", "", "group JSON file, or - for stdin")
	apply.Flags().BoolVar(&dryRun, "dry-run", false, "only show what would change")
	_ = apply.MarkFlagRequired("file")
	sg.AddCommand(apply)

	sg.AddCommand(&cobra.Command{
		Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a group (its services fall back to the default group)", Args: cobra.ExactArgs(1),
		Annotations: op("deleteSecurityGroup"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.need(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.DeleteSecurityGroup(ctx(cmd), s.project, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Deleted security group %s/%s\n", s.project, args[0])
			return nil
		},
	})
	sg.AddCommand(&cobra.Command{
		Use: "service SERVICE", Short: "The groups and rules that apply to a service", Args: cobra.ExactArgs(1),
		Annotations: op("getServiceSecurity"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.need(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			v, err := c.ServiceSecurity(ctx(cmd), s.project, s.env, args[0])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(v)
			}
			for _, l := range v.Lines {
				fmt.Fprintln(a.out, l)
			}
			return nil
		},
	})
	var proto string
	var port int
	check := &cobra.Command{
		Use: "check FROM TO --port N", Short: "Can FROM reach TO? (FROM: PROJECT/ENV/SERVICE, an IP or platform; TO: PROJECT/ENV/SERVICE)",
		Example: "  synctl sg check shop/production/web shop/production/db --port 5432", Args: cobra.ExactArgs(2),
		Annotations: op("checkReachability"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			v, err := c.CheckReachability(ctx(cmd), args[0], args[1], proto, port)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(v)
			}
			word := "DENIED"
			if v.Verdict.Allowed {
				word = "ALLOWED"
			}
			fmt.Fprintf(a.out, "%s: %s → %s %s/%d\n  %s\n", word, v.From, v.To, v.Protocol, v.Port, v.Verdict.Reason)
			for _, m := range []*client.RuleMatch{v.Verdict.Egress, v.Verdict.Ingress} {
				if m != nil {
					fmt.Fprintf(a.out, "  %s rule %d of %s: %s\n", m.Direction, m.Index+1, m.Group, sgRuleText(m.Rule, m.Direction))
				}
			}
			return nil
		},
	}
	check.Flags().StringVar(&proto, "protocol", "tcp", "tcp, udp or icmp")
	check.Flags().IntVar(&port, "port", 0, "destination port")
	sg.AddCommand(check)
	return sg
}

func (a *app) firewallVisibilityCmds() []*cobra.Command {
	var since, node, dir, ip string
	drops := &cobra.Command{
		Use: "drops", Short: "The drop log: connection attempts a default deny dropped", Args: cobra.NoArgs,
		Annotations: op("listFirewallDrops"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ds, err := c.FirewallDrops(ctx(cmd), since, node, dir, ip)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(ds))
			for _, d := range ds {
				src, dst := d.Src, d.Dst
				if d.SrcName != "" {
					src += " (" + d.SrcName + ")"
				}
				if d.DstName != "" {
					dst += " (" + d.DstName + ")"
				}
				if d.Direction == "host" {
					dst = "node " + d.Node
				}
				at := d.At
				rows = append(rows, []string{age(at), d.Node, d.Direction, src, orDash(dst), d.Protocol + "/" + strconv.Itoa(d.Port), strconv.FormatUint(d.Packets, 10)})
			}
			return a.printer().table(ds, []string{"WHEN", "NODE", "DIR", "SOURCE", "DESTINATION", "PORT", "PACKETS"}, rows)
		},
	}
	drops.Flags().StringVar(&since, "since", "1h", "how far back, e.g. 15m, 24h")
	drops.Flags().StringVar(&node, "node", "", "only this node")
	drops.Flags().StringVar(&dir, "direction", "", "host, in or out")
	drops.Flags().StringVar(&ip, "ip", "", "only flows from or to this address")
	var cnode string
	counters := &cobra.Command{
		Use: "counters", Short: "Hit counters of every rule (since each agent started)", Args: cobra.NoArgs,
		Annotations: op("listFirewallCounters"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			cs, err := c.FirewallCounters(ctx(cmd), cnode)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(cs))
			for _, x := range cs {
				rows = append(rows, []string{x.ID, strconv.FormatUint(x.Packets, 10), strconv.FormatUint(x.Bytes, 10)})
			}
			return a.printer().table(cs, []string{"RULE", "PACKETS", "BYTES"}, rows)
		},
	}
	counters.Flags().StringVar(&cnode, "node", "", "one node (name)")
	return []*cobra.Command{drops, counters}
}
