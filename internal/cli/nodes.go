package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) nodesCmd() *cobra.Command {
	nodes := &cobra.Command{Use: "nodes", Aliases: []string{"node", "no"}, Short: "Worker nodes"}

	nodes.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls", "get"}, Short: "List nodes with live status", Args: cobra.NoArgs,
			Annotations: op("listNodes"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				ns, err := c.ListNodes(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(ns))
				for _, n := range ns {
					cpu, mem, disk := "-", "-", "-"
					if m := n.Metrics; m != nil {
						cpu = fmt.Sprintf("%.0f%%", m.CPUPercent)
						mem = fmt.Sprintf("%s/%s", bytesIEC(m.MemoryUsedBytes), bytesIEC(m.MemoryTotalBytes))
						disk = fmt.Sprintf("%.0f%%", pct(m.DiskUsedBytes, m.DiskTotalBytes))
					}
					rows = append(rows, []string{n.Name, n.Status, orDash(n.Info.OS), cpu, mem, disk, ago(n.LastSeenAt), n.ID})
				}
				return a.printer().table(ns, []string{"NAME", "STATUS", "OS", "CPU", "MEMORY", "DISK", "LAST SEEN", "ID"}, rows)
			},
		},
		&cobra.Command{
			Use: "delete ID", Aliases: []string{"rm"}, Short: "Remove a node (revokes its certificate)", Args: cobra.ExactArgs(1),
			Annotations: op("deleteNode"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteNode(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Removed node %s\n", args[0])
				return nil
			},
		},
		a.schedulableCmd("cordon", "Stop placing new tasks on a node", false),
		a.schedulableCmd("uncordon", "Allow new tasks on a node again (ends a drain)", true),
		&cobra.Command{
			Use: "drain ID", Short: "Move a node's tasks to other nodes (replacements first)", Args: cobra.ExactArgs(1),
			Annotations: op("drainNode"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				n, err := c.DrainNode(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Draining %s: its tasks move to other nodes as replacements become healthy\n", n.Name)
				return nil
			},
		},
		a.joinTokensCmd(),
		a.nodeShellCmd(),
		a.nodeMetricsCmd(),
	)
	nodes.AddCommand(a.agentUpgradeCmds()...)
	return nodes
}

func (a *app) joinTokensCmd() *cobra.Command {
	jt := &cobra.Command{Use: "join-tokens", Short: "Tokens that let new nodes join"}

	var desc string
	var ttl time.Duration
	var reusable bool
	create := &cobra.Command{
		Use: "create", Short: "Create a join token and print the join command", Args: cobra.NoArgs,
		Annotations: op("createJoinToken"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			p, _ := resolveProfile(a.profile, a.endpoint)
			t, err := c.CreateJoinToken(ctx(cmd), desc, int(ttl/time.Minute), !reusable)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(t)
			}
			command := t.Command
			if command == "" { // older controllers
				command = "curl -fsSL " + p.Endpoint + "/join.sh | sudo bash -s -- --token " + t.Token
			}
			fmt.Fprintf(a.out, "Join token (expires %s, %s):\n\n  %s\n\nRun on the new server:\n\n  %s\n",
				t.ExpiresAt.Local().Format(time.RFC822), map[bool]string{true: "single use", false: "reusable"}[t.SingleUse], t.Token, command)
			return nil
		},
	}
	create.Flags().StringVar(&desc, "description", "", "what the token is for")
	create.Flags().DurationVar(&ttl, "ttl", time.Hour, "how long the token is valid (max 168h)")
	create.Flags().BoolVar(&reusable, "reusable", false, "allow several nodes to join with this token")

	jt.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List active join tokens", Args: cobra.NoArgs,
			Annotations: op("listJoinTokens"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				ts, err := c.ListJoinTokens(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(ts))
				for _, t := range ts {
					rows = append(rows, []string{t.ID, orDash(t.Description), t.ExpiresAt.Local().Format("2006-01-02 15:04"), fmt.Sprint(t.SingleUse), fmt.Sprint(t.Uses)})
				}
				return a.printer().table(ts, []string{"ID", "DESCRIPTION", "EXPIRES", "SINGLE USE", "USES"}, rows)
			},
		},
		create,
		&cobra.Command{
			Use: "delete ID", Aliases: []string{"rm"}, Short: "Revoke a join token", Args: cobra.ExactArgs(1),
			Annotations: op("deleteJoinToken"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteJoinToken(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Revoked join token %s\n", args[0])
				return nil
			},
		},
	)
	return jt
}

func bytesIEC(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ci", float64(b)/float64(div), "KMGTPE"[exp])
}

func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func (a *app) schedulableCmd(use, short string, on bool) *cobra.Command {
	return &cobra.Command{
		Use: use + " ID", Short: short, Args: cobra.ExactArgs(1),
		Annotations: op("setNodeSchedulable"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			n, err := c.SetNodeSchedulable(ctx(cmd), args[0], on)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Node %s schedulable: %v\n", n.Name, n.Schedulable)
			return nil
		},
	}
}
