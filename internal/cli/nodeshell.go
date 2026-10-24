package cli

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
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

func (a *app) projectIngressCmd() *cobra.Command {
	var nodes []string
	var controller, cutOver bool
	cmd := &cobra.Command{
		Use:   "ingress PROJECT",
		Short: "Show or choose the nodes that serve a project's public traffic (its load balancers)",
		Long: `By default the controller (and any edge pool) serves a project's public
traffic. With --nodes, those nodes run Traefik for the project and serve all
of it; the controller stops serving it. The first node gives the generated
addresses (<name>.<its IP>.sslip.io); point custom domains at every node.

The previous ingress keeps serving each custom domain until its DNS points at
the new one. --cut-over ends that now.`,
		Example: `  synctl projects ingress shop
  synctl projects ingress shop --nodes lb1,lb2
  synctl projects ingress shop --controller
  synctl projects ingress shop --cut-over`,
		Args:        cobra.ExactArgs(1),
		Annotations: op("getProjectIngress", "setProjectIngress", "cutOverProjectIngress"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if controller && len(nodes) > 0 {
				return fmt.Errorf("--nodes and --controller conflict")
			}
			var in client.ProjectIngress
			switch {
			case controller || len(nodes) > 0:
				in, err = c.SetProjectIngress(ctx(cmd), args[0], nodes)
			case cutOver:
				in, err = c.CutOverProjectIngress(ctx(cmd), args[0])
			default:
				in, err = c.GetProjectIngress(ctx(cmd), args[0])
			}
			if err != nil {
				return err
			}
			if cutOver && (controller || len(nodes) > 0) {
				if in, err = c.CutOverProjectIngress(ctx(cmd), args[0]); err != nil {
					return err
				}
			}
			if a.output == "json" {
				return a.printer().json(in)
			}
			if len(in.Nodes) == 0 {
				fmt.Fprintf(a.out, "Project %s is served by the controller\n", in.Project)
			} else {
				fmt.Fprintf(a.out, "Project %s is served by its own load balancers:\n", in.Project)
				for _, n := range in.Nodes {
					fmt.Fprintf(a.out, "  %-20s %-16s %s\n", n.Name, n.Address, n.State)
				}
				fmt.Fprintf(a.out, "Generated addresses: *.%s\n", in.Base)
			}
			if len(in.Targets) > 0 {
				fmt.Fprintf(a.out, "Point custom domains at: %s\n", strings.Join(in.Targets, ", "))
			}
			if in.Move != nil {
				from := "the controller"
				if len(in.Move.From) > 0 {
					from = strings.Join(in.Move.From, ", ")
				}
				fmt.Fprintf(a.out, "Moving: %s still serves %s until their DNS moves (--cut-over to stop now)\n", from, strings.Join(in.Pending, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&nodes, "nodes", nil, "the load-balancer nodes, in order (the first gives the generated addresses)")
	cmd.Flags().BoolVar(&controller, "controller", false, "go back to the controller")
	cmd.Flags().BoolVar(&cutOver, "cut-over", false, "stop the previous ingress serving the project now")
	return cmd
}

func (a *app) projectProtectionCmd() *cobra.Command {
	var on, off, genSecret, clearSecret bool
	var rate, burst, concurrent, bodyMB, guardRate, guardConns, guardBan int
	var period, clientIP, valkey, createValkey string
	var block, bypassIPs, bypassPaths []string
	cmd := &cobra.Command{
		Use:   "protection PROJECT",
		Short: "Show or change a project's traffic protection (rate limits, block and bypass lists, network guard)",
		Long: `Protection applies to every route of the project, on whichever load balancer
serves it. Blocked clients get 403; bypassing requests (IPs, a secret header,
paths) skip the rate and concurrency limits. With a Valkey, the project's load
balancers share their counters; while it is down each counts on its own. The
network guard works on the project's own load-balancer nodes only.`,
		Example: `  synctl projects protection shop
  synctl projects protection shop --on --rate 50 --per 1s --burst 100 --concurrent 20
  synctl projects protection shop --client-ip header:CF-Connecting-IP
  synctl projects protection shop --block 203.0.113.0/24 --bypass-ips 198.51.100.7 --bypass-paths /healthz,/webhooks
  synctl projects protection shop --generate-secret        # prints the bypass header's secret once
  synctl projects protection shop --create-valkey production
  synctl projects protection shop --guard-rate 100 --guard-conns 200 --guard-ban 30`,
		Args:        cobra.ExactArgs(1),
		Annotations: op("getProjectProtection", "setProjectProtection", "createProtectionValkey"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if createValkey != "" {
				if _, err := c.CreateProtectionValkey(ctx(cmd), args[0], createValkey); err != nil {
					return err
				}
			}
			v, err := c.GetProjectProtection(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			s := v.Spec
			f := cmd.Flags()
			changed := false
			set := func(name string, apply func()) {
				if f.Changed(name) {
					apply()
					changed = true
				}
			}
			set("on", func() { s.Enabled = true })
			set("off", func() { s.Enabled = false })
			set("rate", func() { s.RateLimit.Average = rate })
			set("burst", func() { s.RateLimit.Burst = burst })
			set("per", func() { s.RateLimit.Period = period })
			set("concurrent", func() { s.MaxConcurrent = concurrent })
			set("body-mb", func() { s.MaxBodyMB = bodyMB })
			set("block", func() { s.Block = block })
			set("bypass-ips", func() { s.Bypass.IPs = bypassIPs })
			set("bypass-paths", func() { s.Bypass.Paths = bypassPaths })
			set("valkey", func() { s.Redis.DatabaseID = valkey })
			set("guard-rate", func() { s.Guard.Rate = guardRate })
			set("guard-conns", func() { s.Guard.MaxConnections = guardConns })
			set("guard-ban", func() { s.Guard.BanMinutes = guardBan })
			var cerr error
			set("client-ip", func() {
				kind, arg, _ := strings.Cut(clientIP, ":")
				s.ClientIP.Source, s.ClientIP.Depth, s.ClientIP.Header = kind, 0, ""
				switch kind {
				case "forwarded":
					if s.ClientIP.Depth, cerr = strconv.Atoi(arg); cerr != nil {
						cerr = fmt.Errorf("--client-ip forwarded:DEPTH")
					}
				case "header":
					s.ClientIP.Header = arg
				}
			})
			if cerr != nil {
				return cerr
			}
			extra := map[string]any{}
			if genSecret {
				extra["generateSecret"], changed = true, true
			}
			if clearSecret {
				extra["clearSecret"], changed = true, true
			}
			if changed {
				if v, err = c.SetProjectProtection(ctx(cmd), args[0], s, extra); err != nil {
					return err
				}
				s = v.Spec
			}
			if a.output == "json" {
				return a.printer().json(v)
			}
			state := "off (settings kept)"
			if s.Enabled {
				state = "on"
			}
			fmt.Fprintf(a.out, "Protection of %s: %s\n", v.Project, state)
			if s.RateLimit.Average > 0 {
				fmt.Fprintf(a.out, "  Rate limit:   %d per %s and client (burst %d), by %s\n", s.RateLimit.Average, s.RateLimit.Period, s.RateLimit.Burst, clientIPText(s))
			}
			if s.MaxConcurrent > 0 {
				fmt.Fprintf(a.out, "  Concurrent:   %d requests per client\n", s.MaxConcurrent)
			}
			if s.MaxBodyMB > 0 {
				fmt.Fprintf(a.out, "  Body limit:   %d MB\n", s.MaxBodyMB)
			}
			if len(s.Block) > 0 {
				fmt.Fprintf(a.out, "  Blocked:      %s\n", strings.Join(s.Block, ", "))
			}
			var by []string
			if len(s.Bypass.IPs) > 0 {
				by = append(by, strings.Join(s.Bypass.IPs, ", "))
			}
			if s.Bypass.HasSecret {
				by = append(by, "header "+s.Bypass.Header)
			}
			if len(s.Bypass.Paths) > 0 {
				by = append(by, "paths "+strings.Join(s.Bypass.Paths, ", "))
			}
			if len(by) > 0 {
				fmt.Fprintf(a.out, "  Bypass:       %s\n", strings.Join(by, "; "))
			}
			switch {
			case v.Redis.Fallback:
				fmt.Fprintf(a.out, "  Counters:     per node (Valkey %s is %s)\n", v.Redis.Name, v.Redis.Health)
			case v.Redis.Active:
				fmt.Fprintf(a.out, "  Counters:     shared in Valkey %s\n", v.Redis.Name)
			default:
				fmt.Fprintln(a.out, "  Counters:     per node")
			}
			if g := s.Guard; g.Rate > 0 || g.MaxConnections > 0 {
				where := "on its own load balancers"
				if !v.OwnNodes {
					where = "(applies once the project has its own load balancers)"
				}
				fmt.Fprintf(a.out, "  Network guard: %d new connections/s, %d at once per source, ban %d min %s\n", g.Rate, g.MaxConnections, g.BanMinutes, where)
			}
			if v.Stats != nil {
				fmt.Fprintf(a.out, "  Last hour:    %.0f requests, %.0f limited, %.0f blocked\n", v.Stats.Total, v.Stats.Limited, v.Stats.Blocked)
			}
			if v.Secret != "" {
				fmt.Fprintf(a.out, "\nBypass secret (shown once): send header %s: %s\n", s.Bypass.Header, v.Secret)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&on, "on", false, "apply the protection")
	fl.BoolVar(&off, "off", false, "keep the settings but apply none")
	fl.IntVar(&rate, "rate", 0, "requests per period and client (0 = no rate limit)")
	fl.StringVar(&period, "per", "1s", "rate period: 1s, 1m or 1h")
	fl.IntVar(&burst, "burst", 0, "requests allowed at once over the rate")
	fl.StringVar(&clientIP, "client-ip", "", "how clients are told apart: connection, forwarded:DEPTH or header:NAME")
	fl.IntVar(&concurrent, "concurrent", 0, "requests in flight per client (0 = no cap)")
	fl.IntVar(&bodyMB, "body-mb", 0, "request body cap in MB (0 = no cap)")
	fl.StringSliceVar(&block, "block", nil, "client IPs/CIDRs answered with 403")
	fl.StringSliceVar(&bypassIPs, "bypass-ips", nil, "client IPs/CIDRs that skip the limits")
	fl.StringSliceVar(&bypassPaths, "bypass-paths", nil, "path prefixes that skip the limits")
	fl.BoolVar(&genSecret, "generate-secret", false, "make a new bypass secret (printed once)")
	fl.BoolVar(&clearSecret, "clear-secret", false, "remove the bypass secret")
	fl.StringVar(&valkey, "valkey", "", "the Valkey (database ID) that keeps shared counters; empty = per node")
	fl.StringVar(&createValkey, "create-valkey", "", "create a small Valkey in this environment for shared counters")
	fl.IntVar(&guardRate, "guard-rate", 0, "network guard: new connections per second and source")
	fl.IntVar(&guardConns, "guard-conns", 0, "network guard: concurrent connections per source")
	fl.IntVar(&guardBan, "guard-ban", 0, "network guard: ban sources over it for this many minutes (0 = no ban)")
	return cmd
}

func clientIPText(s client.ProtectionSpec) string {
	switch s.ClientIP.Source {
	case "forwarded":
		return fmt.Sprintf("X-Forwarded-For (depth %d)", s.ClientIP.Depth)
	case "header":
		return "header " + s.ClientIP.Header
	}
	return "connection address"
}
