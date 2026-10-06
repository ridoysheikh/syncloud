package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func (a *app) trafficCmd() *cobra.Command {
	var s scope
	var rng string
	cmd := &cobra.Command{
		Use:   "traffic [SERVICE]",
		Short: "Requests, errors and latency per route from Traefik (§5.7)",
		Example: `  synctl traffic                 # every route
  synctl traffic -p shop         # the services of shop/production
  synctl traffic web -p shop`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: op("getTraffic", "getEnvironmentTraffic", "getServiceTraffic"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			service := ""
			if len(args) == 1 {
				if err := s.need(); err != nil {
					return err
				}
				service = args[0]
			}
			t, err := c.Traffic(ctx(cmd), s.project, s.env, service, rng)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(t.Routes))
			for _, r := range t.Routes {
				rows = append(rows, []string{r.Project + "/" + r.Environment + "/" + r.Service, fmt.Sprintf("%.2f", r.RPS),
					share(r.Errors4xx, r.RPS), share(r.Errors5xx, r.RPS), ms(r.P50Ms), ms(r.P95Ms), bytesHuman(r.BytesOut) + "/s"})
			}
			if len(rows) == 0 && a.output != "json" {
				fmt.Fprintln(a.errOut, "No requests in the last minutes.")
			}
			return a.printer().table(t, []string{"ROUTE", "REQ/S", "4XX", "5XX", "P50", "P95", "OUT"}, rows)
		},
	}
	cmd.Flags().StringVar(&rng, "range", "1h", "chart range for -o json: 15m, 1h, 6h, 24h or 7d")
	a.scopeFlags(cmd, &s)
	cmd.AddCommand(&cobra.Command{
		Use: "map", Short: "Routed services with their hostnames and the tasks answering", Args: cobra.NoArgs,
		Annotations: op("getTrafficMap"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			routes, err := c.TrafficMap(ctx(cmd))
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(routes)
			}
			for _, r := range routes {
				fmt.Fprintf(a.out, "%s/%s/%s  %.2f req/s  p95 %s  (%s)\n", r.Project, r.Environment, r.Service, r.RPS, ms(r.P95Ms), strings.Join(r.Hosts, ", "))
				for _, t := range r.Tasks {
					fmt.Fprintf(a.out, "  └ %-24s %-8s %-15s %-9s %.2f req/s\n", t.ID, t.Node, t.IP, orDash(t.Health), t.RPS)
				}
			}
			return nil
		},
	})
	return cmd
}

func share(part, total float64) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*part/total)
}

func ms(v float64) string {
	if v == 0 {
		return "-"
	}
	if v >= 1000 {
		return fmt.Sprintf("%.2fs", v/1000)
	}
	return fmt.Sprintf("%.0fms", v)
}

func (a *app) requestsCmd() *cobra.Command {
	var s scope
	var q client.LogQuery
	var follow bool
	cmd := &cobra.Command{
		Use:   "requests [SERVICE]",
		Short: "Request log from Traefik: method, path, status, latency, client (§5.7)",
		Example: `  synctl requests web -p shop -f
  synctl requests --status 5xx --since 1h   # every route`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: op("queryLogs", "tailLogs"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q.Stream = "access"
			if len(args) == 1 {
				if err := s.need(); err != nil {
					return err
				}
				q.Project, q.Environment, q.Service = s.project, s.env, args[0]
			}
			print := func(l client.LogLine) {
				if a.output == "json" {
					_ = a.printer().json(l)
					return
				}
				f := l.Fields
				fmt.Fprintf(a.out, "%s %-3s %-6s %-40s %7sms %-15s %s\n", l.Time.Local().Format("15:04:05.000"), f["status"], f["method"],
					f["host"]+f["path"], f["duration_ms"], f["client"], l.Project+"/"+l.Environment+"/"+l.Service)
			}
			lines, err := c.QueryLogs(ctx(cmd), q)
			if err != nil && !follow {
				return err
			}
			for _, l := range lines {
				print(l)
			}
			if !follow {
				return nil
			}
			last := time.Time{}
			if len(lines) > 0 {
				last = lines[len(lines)-1].Time
			}
			return c.TailLogs(ctx(cmd), q, func(l client.LogLine) {
				if l.Time.After(last) {
					print(l)
				}
			})
		},
	}
	a.scopeFlags(cmd, &s)
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new requests")
	cmd.Flags().StringVar(&q.Status, "status", "", "only this status class: 2xx, 3xx, 4xx or 5xx")
	cmd.Flags().StringVar(&q.Client, "client", "", "only requests from this client IP")
	cmd.Flags().StringVar(&q.Text, "grep", "", "only requests containing this text")
	cmd.Flags().StringVar(&q.Since, "since", "15m", "how far back to start")
	cmd.Flags().IntVar(&q.Limit, "limit", 200, "how many past requests to print")
	return cmd
}
