package cli

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	"github.com/ridoysheikh/syncloud/internal/client"
)

// nodeID resolves a node name or ID.
func nodeID(cmd *cobra.Command, c *client.Client, ref string) (string, error) {
	ns, err := c.ListNodes(ctx(cmd))
	if err != nil {
		return "", err
	}
	for _, n := range ns {
		if n.ID == ref || n.Name == ref {
			return n.ID, nil
		}
	}
	return "", fmt.Errorf("no node %s", ref)
}

func (a *app) nodeShellCmd() *cobra.Command {
	var tty bool
	cmd := &cobra.Command{
		Use:   "shell NODE [-- COMMAND...]",
		Short: "Open a shell on a node itself (the controller node too), as the agent's user",
		Example: `  synctl nodes shell w1
  synctl nodes shell ctl-0 -- df -h`,
		Args:        cobra.MinimumNArgs(1),
		Annotations: op("nodeShell"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := nodeID(cmd, c, args[0])
			if err != nil {
				return err
			}
			command := args[1:]
			return a.terminal(cmd, tty, cmd.Flags().Changed("tty"), func(tty bool, cols, rows int) (*websocket.Conn, error) {
				return c.NodeShellDial(ctx(cmd), id, command, tty, cols, rows)
			})
		},
	}
	cmd.Flags().BoolVarP(&tty, "tty", "t", false, "allocate a terminal (default: when stdin is a terminal)")
	return cmd
}

func (a *app) nodeMetricsCmd() *cobra.Command {
	var rng string
	cmd := &cobra.Command{
		Use:         "metrics NODE",
		Short:       "A node's resource history: latest, average and peak of each chart",
		Example:     `  synctl nodes metrics w1 --range 6h`,
		Args:        cobra.ExactArgs(1),
		Annotations: op("getNodeMetrics"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := nodeID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var m struct {
				Charts map[string][]struct {
					Key    string       `json:"key"`
					Points [][2]float64 `json:"points"`
				} `json:"charts"`
			}
			if err := a.do(cmd, "GET", "/api/v1/nodes/"+url.PathEscape(id)+"/metrics?range="+url.QueryEscape(rng), nil, &m); err != nil {
				return err
			}
			format := map[string]func(float64) string{
				"cpu":        func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
				"load":       func(v float64) string { return fmt.Sprintf("%.2f", v) },
				"tasks":      func(v float64) string { return fmt.Sprintf("%.0f", v) },
				"serviceCpu": func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
			}
			charts := make([]string, 0, len(m.Charts))
			for k := range m.Charts {
				charts = append(charts, k)
			}
			sort.Strings(charts)
			var rows [][]string
			for _, chart := range charts {
				f := format[chart]
				if f == nil {
					f = func(v float64) string { return bytesIEC(uint64(v)) }
					if chart == "network" || chart == "mesh" {
						f = func(v float64) string { return bytesIEC(uint64(v)) + "/s" }
					}
				}
				for _, s := range m.Charts[chart] {
					last, sum, peak, n := math.NaN(), 0.0, math.Inf(-1), 0
					for _, p := range s.Points {
						if math.IsNaN(p[1]) {
							continue
						}
						last, sum, peak, n = p[1], sum+p[1], max(peak, p[1]), n+1
					}
					if n == 0 {
						continue
					}
					rows = append(rows, []string{chart, s.Key, f(last), f(sum / float64(n)), f(peak)})
				}
			}
			return a.printer().table(m, []string{"CHART", "SERIES", "LATEST", "AVERAGE (" + strings.ToUpper(rng) + ")", "PEAK"}, rows)
		},
	}
	cmd.Flags().StringVar(&rng, "range", "1h", "time range: 15m, 1h, 6h, 24h or 7d")
	return cmd
}

func (a *app) projectNodesCmd() *cobra.Command {
	var anyNode bool
	cmd := &cobra.Command{
		Use:   "nodes PROJECT [NODE...]",
		Short: "Show or set the nodes a project's services and jobs may run on",
		Long: `With no NODE, prints the project's allowed nodes. With nodes, replaces the
list; --any clears it. Tasks on nodes no longer allowed move, new task first.
Naming the controller node lets the project run there even when it takes no
general workloads.`,
		Example: `  synctl projects nodes shop
  synctl projects nodes shop w1 w2
  synctl projects nodes shop --any`,
		Args:        cobra.MinimumNArgs(1),
		Annotations: op("setProjectNodes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var p client.Project
			if len(args) == 1 && !anyNode {
				ps, err := c.ListProjects(ctx(cmd))
				if err != nil {
					return err
				}
				found := false
				for _, x := range ps {
					if x.Name == args[0] {
						p, found = x, true
					}
				}
				if !found {
					return fmt.Errorf("no project %s", args[0])
				}
			} else if p, err = c.SetProjectNodes(ctx(cmd), args[0], args[1:]); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(p)
			}
			if len(p.Nodes) == 0 {
				fmt.Fprintf(a.out, "Project %s runs on any schedulable node\n", p.Name)
			} else {
				fmt.Fprintf(a.out, "Project %s runs only on: %s\n", p.Name, strings.Join(p.Nodes, ", "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&anyNode, "any", false, "allow every node again")
	return cmd
}
