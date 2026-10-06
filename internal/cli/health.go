package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) healthCmd() *cobra.Command {
	h := &cobra.Command{Use: "health", Short: "Service health, uptime and incidents (§5.6)"}
	var open bool
	inc := &cobra.Command{
		Use: "incidents", Short: "Incidents, newest first", Args: cobra.NoArgs,
		Annotations: op("listIncidents"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			is, err := c.Incidents(ctx(cmd), open)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(is))
			for _, i := range is {
				closed := "open"
				if i.ClosedAt != nil {
					closed = i.ClosedAt.Sub(i.OpenedAt).Round(1e9).String()
				}
				rows = append(rows, []string{i.Project + "/" + i.Environment + "/" + i.Service, i.State, age(i.OpenedAt), closed, i.Cause})
			}
			return a.printer().table(is, []string{"SERVICE", "STATE", "OPENED", "DURATION", "CAUSE"}, rows)
		},
	}
	inc.Flags().BoolVar(&open, "open", false, "only open incidents")
	h.AddCommand(
		&cobra.Command{
			Use: "services", Aliases: []string{"ls"}, Short: "Health and uptime of every service", Args: cobra.NoArgs,
			Annotations: op("listServiceHealth"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				hs, err := c.ServiceHealth(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(hs))
				for _, x := range hs {
					up, lat := "-", "-"
					if x.Uptime24h != nil {
						up = fmt.Sprintf("%.2f%%", *x.Uptime24h)
					}
					if x.LastCheck != nil {
						lat = fmt.Sprintf("%.0fms", x.LastCheck.LatencyMs)
					}
					rows = append(rows, []string{x.Project + "/" + x.Environment + "/" + x.Service, x.State, fmt.Sprintf("%d/%d", x.Serving, x.Desired), up, lat, orDash(x.Reason)})
				}
				return a.printer().table(hs, []string{"SERVICE", "STATE", "SERVING", "UPTIME 24H", "LATENCY", "REASON"}, rows)
			},
		},
		inc,
	)
	return h
}
