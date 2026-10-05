package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func (a *app) firewallCmd() *cobra.Command {
	fw := &cobra.Command{Use: "firewall", Aliases: []string{"fw"}, Short: "Host firewall policies (§8.3)"}

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
	)
	return fw
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
