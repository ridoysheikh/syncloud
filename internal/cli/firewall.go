package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ridoysheikh/syncloud/internal/client"
)

func (a *app) firewallCmd() *cobra.Command {
	fw := &cobra.Command{Use: "firewall", Aliases: []string{"fw"}, Short: "Host firewall: policies, SSH access, blocks (§8.3)"}

	find := func(cmd *cobra.Command, c *client.Client, ref string) (client.FirewallPolicy, error) {
		ps, err := c.ListFirewallPolicies(ctx(cmd))
		if err != nil {
			return client.FirewallPolicy{}, err
		}
		for _, p := range ps {
			if p.ID == ref || p.Name == ref {
				return p, nil
			}
		}
		return client.FirewallPolicy{}, fmt.Errorf("no firewall policy %q", ref)
	}

	var file string
	apply := &cobra.Command{
		Use: "apply -f FILE", Short: "Create or update a policy from JSON (matched by name)", Args: cobra.NoArgs,
		Annotations: op("createFirewallPolicy", "updateFirewallPolicy"),
		Example: `  cat <<'JSON' | synctl firewall apply -f -
  {"name": "default", "targets": ["*"], "rules": [
    {"protocol": "tcp", "ports": "22", "sources": ["203.0.113.5"], "description": "SSH from the office"}
  ]}
  JSON`,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			var p client.FirewallPolicy
			dec := json.NewDecoder(r)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&p); err != nil {
				return fmt.Errorf("parse policy: %w", err)
			}
			existing, err := find(cmd, c, p.Name)
			var out client.FirewallPolicy
			verb := "Created"
			if err == nil {
				out, err = c.UpdateFirewallPolicy(ctx(cmd), existing.ID, p)
				verb = "Updated"
			} else {
				out, err = c.CreateFirewallPolicy(ctx(cmd), p)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s firewall policy %s (%s). Nodes apply it within seconds, with automatic rollback if they lose the controller.\n", verb, out.Name, out.ID)
			return nil
		},
	}
	apply.Flags().StringVarP(&file, "file", "f", "", "policy JSON file, or - for stdin")
	_ = apply.MarkFlagRequired("file")

	fw.AddCommand(a.firewallVisibilityCmds()...)
	fw.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List policies", Args: cobra.NoArgs,
			Annotations: op("listFirewallPolicies"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				ps, err := c.ListFirewallPolicies(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(ps))
				for _, p := range ps {
					rules := make([]string, 0, len(p.Rules))
					for _, r := range p.Rules {
						rules = append(rules, ruleText(r))
					}
					rows = append(rows, []string{p.Name, strings.Join(p.Targets, ","), joinOrDash(rules), p.ID})
				}
				return a.printer().table(ps, []string{"NAME", "TARGETS", "RULES", "ID"}, rows)
			},
		},
		&cobra.Command{
			Use: "get NAME|ID", Short: "Show a policy as JSON (edit and re-apply with apply -f)", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				p, err := find(cmd, c, args[0])
				if err != nil {
					return err
				}
				return a.printer().json(p)
			},
		},
		apply,
		&cobra.Command{
			Use: "delete NAME|ID", Aliases: []string{"rm"}, Short: "Delete a policy", Args: cobra.ExactArgs(1),
			Annotations: op("deleteFirewallPolicy"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				p, err := find(cmd, c, args[0])
				if err != nil {
					return err
				}
				if err := c.DeleteFirewallPolicy(ctx(cmd), p.ID); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted firewall policy %s\n", p.Name)
				return nil
			},
		},
		&cobra.Command{
			Use: "effective NODE-ID", Short: "Rules a node enforces, including built-in ones", Args: cobra.ExactArgs(1),
			Annotations: op("getEffectiveFirewall"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				e, err := c.EffectiveFirewall(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.printer().json(e)
				}
				fmt.Fprint(a.out, e.Ruleset)
				return nil
			},
		},
		a.firewallSSHCmd(),
		a.firewallBlocksCmd(),
		a.firewallBlockCmd(),
		&cobra.Command{
			Use: "unblock ID", Short: "Remove a block", Args: cobra.ExactArgs(1),
			Annotations: op("deleteFirewallBlock"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteFirewallBlock(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Unblocked %s\n", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use: "node NODE-ID", Short: "A node's firewall: open ports, listening sockets and their verdict, recent attempts, blocks", Args: cobra.ExactArgs(1),
			Annotations: op("getNodeFirewall"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				f, err := c.GetNodeFirewall(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.printer().json(f)
				}
				if !f.Enabled {
					fmt.Fprintln(a.out, "The host firewall is off on this cluster (--firewall=0).")
					return nil
				}
				fmt.Fprintf(a.out, "Open on %s:\n", f.Node)
				for _, r := range f.Rules {
					src := "anyone"
					if len(r.Sources) > 0 {
						src = strings.Join(r.Sources, ",")
					}
					fmt.Fprintf(a.out, "  %-4s %-12s %-14s from %-24s %s\n", r.Protocol, r.Ports, r.Kind, src, r.Description)
				}
				fmt.Fprintln(a.out, "\nListening:")
				for _, l := range f.Listeners {
					fmt.Fprintf(a.out, "  %-4s %-28s %-10s %s\n", l.Protocol, joinHostPort(l.Address, l.Port), l.Verdict, l.Process)
				}
				if len(f.Blocks) > 0 {
					fmt.Fprintln(a.out, "\nBlocked:")
					for _, b := range f.Blocks {
						fmt.Fprintf(a.out, "  %-20s %s  %s\n", b.Source, blockUntil(b.ExpiresAt), b.Reason)
					}
				}
				if len(f.Drops) > 0 {
					fmt.Fprintln(a.out, "\nRecent attempts:")
					for i, d := range f.Drops {
						if i == 20 {
							break
						}
						fmt.Fprintf(a.out, "  %s  %-40s %s/%d  %d packets  %s\n", d.At.Local().Format("15:04:05"), d.Src, d.Protocol, d.Port, d.Packets, d.Direction)
					}
				}
				return nil
			},
		},
	)
	return fw
}

func joinHostPort(h string, p int) string { return net.JoinHostPort(h, strconv.Itoa(p)) }

func blockUntil(t *time.Time) string {
	if t == nil {
		return "until removed"
	}
	return "until " + t.Local().Format("Jan 2 15:04")
}

func (a *app) firewallSSHCmd() *cobra.Command {
	var sources, exempt []string
	var ports []int
	var rate, burst, ban int
	var autoBan, anywhere bool
	cmd := &cobra.Command{
		Use:   "ssh",
		Short: "Show or change the built-in SSH access of every node",
		Long: `SSH is always open on port 22 and on every port sshd listens on, so a node
stays reachable for disaster recovery. These settings decide who may connect
and how often: a source over the rate limit is dropped, and with auto-ban
blocked on that node for a while.`,
		Example: `  synctl firewall ssh
  synctl firewall ssh --sources 203.0.113.0/24,198.51.100.7
  synctl firewall ssh --anywhere --rate 10 --burst 5 --auto-ban --ban-minutes 30`,
		Args:        cobra.NoArgs,
		Annotations: op("getFirewallSSH", "setFirewallSSH"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			s, err := c.GetFirewallSSH(ctx(cmd))
			if err != nil {
				return err
			}
			f := cmd.Flags()
			changed := false
			if f.Changed("sources") {
				s.Sources, changed = sources, true
			}
			if anywhere {
				s.Sources, changed = []string{}, true
			}
			if f.Changed("exempt") {
				s.Exempt, changed = exempt, true
			}
			if f.Changed("ports") {
				s.Ports, changed = ports, true
			}
			if f.Changed("rate") {
				s.Rate, changed = rate, true
			}
			if f.Changed("burst") {
				s.Burst, changed = burst, true
			}
			if f.Changed("auto-ban") {
				s.AutoBan, changed = autoBan, true
			}
			if f.Changed("ban-minutes") {
				s.BanMinutes, changed = ban, true
			}
			if changed {
				if s, err = c.SetFirewallSSH(ctx(cmd), s); err != nil {
					return err
				}
			}
			if a.output == "json" {
				return a.printer().json(s)
			}
			from := "anywhere"
			if len(s.Sources) > 0 {
				from = strings.Join(s.Sources, ", ")
			}
			fmt.Fprintf(a.out, "SSH: port 22%s and sshd's ports, from %s\n", extraPorts(s.Ports), from)
			if s.Rate > 0 {
				fmt.Fprintf(a.out, "Rate limit: %d new connections a minute per source (burst %d)\n", s.Rate, s.Burst)
			} else {
				fmt.Fprintln(a.out, "Rate limit: off")
			}
			if s.AutoBan {
				fmt.Fprintf(a.out, "Auto-ban: %d minutes\n", s.BanMinutes)
			}
			if len(s.Exempt) > 0 {
				fmt.Fprintf(a.out, "Never limited: %s\n", strings.Join(s.Exempt, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&sources, "sources", nil, "IPs or CIDRs allowed to connect")
	cmd.Flags().BoolVar(&anywhere, "anywhere", false, "allow SSH from anywhere")
	cmd.Flags().StringSliceVar(&exempt, "exempt", nil, "sources never rate limited")
	cmd.Flags().IntSliceVar(&ports, "ports", nil, "extra SSH ports besides 22")
	cmd.Flags().IntVar(&rate, "rate", 0, "new connections per minute per source (0 = no limit)")
	cmd.Flags().IntVar(&burst, "burst", 0, "connections allowed at once over the rate")
	cmd.Flags().BoolVar(&autoBan, "auto-ban", false, "block sources over the rate")
	cmd.Flags().IntVar(&ban, "ban-minutes", 0, "how long auto-ban blocks a source")
	return cmd
}

func extraPorts(ps []int) string {
	s := ""
	for _, p := range ps {
		s += ", " + strconv.Itoa(p)
	}
	return s
}

func (a *app) firewallBlocksCmd() *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use: "blocks", Short: "List blocked sources", Args: cobra.NoArgs,
		Annotations: op("listFirewallBlocks"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			bs, err := c.ListFirewallBlocks(ctx(cmd), node)
			if err != nil {
				return err
			}
			var rows [][]string
			for _, b := range bs {
				where := b.Node
				if where == "" {
					where = "all nodes"
				}
				by := b.CreatedBy
				if b.Auto {
					by = "auto-ban"
				}
				rows = append(rows, []string{b.Source, where, blockUntil(b.ExpiresAt), by, b.Reason, b.ID})
			}
			return a.printer().table(bs, []string{"SOURCE", "WHERE", "EXPIRES", "BY", "REASON", "ID"}, rows)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "only blocks that apply to this node")
	return cmd
}

func (a *app) firewallBlockCmd() *cobra.Command {
	var node, reason string
	var dur time.Duration
	var force bool
	cmd := &cobra.Command{
		Use:   "block SOURCE",
		Short: "Drop an IP or CIDR on every node (or one) before anything else",
		Example: `  synctl firewall block 203.0.113.9 --for 1h
  synctl firewall block 198.51.100.0/24 --node w1 --reason "scanner"
  synctl firewall block 192.0.2.7          # until removed`,
		Args:        cobra.ExactArgs(1),
		Annotations: op("createFirewallBlock"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			b, err := c.CreateFirewallBlock(ctx(cmd), args[0], node, int(dur/time.Minute), reason, force)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(b)
			}
			where := "every node"
			if b.Node != "" {
				where = b.Node
			}
			fmt.Fprintf(a.out, "Blocked %s on %s %s (%s)\n", b.Source, where, blockUntil(b.ExpiresAt), b.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "only on this node")
	cmd.Flags().DurationVar(&dur, "for", 0, "how long (e.g. 10m, 1h, 24h; default until removed)")
	cmd.Flags().StringVar(&reason, "reason", "", "why (shown in the list)")
	cmd.Flags().BoolVar(&force, "force", false, "block it even if it is your own address")
	return cmd
}

func ruleText(r client.FirewallRule) string {
	s := r.Protocol
	if r.Ports != "" {
		s += "/" + r.Ports
	}
	if len(r.Sources) > 0 {
		s += " from " + strings.Join(r.Sources, ",")
	}
	return s
}
