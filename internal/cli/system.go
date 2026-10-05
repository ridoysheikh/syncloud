package cli

import (
	"github.com/spf13/cobra"
)

func (a *app) systemCmd() *cobra.Command {
	sys := &cobra.Command{Use: "system", Short: "Platform components on the controller node"}
	sys.AddCommand(&cobra.Command{
		Use: "tasks", Aliases: []string{"components"}, Short: "List system tasks (Traefik, VictoriaMetrics, …)", Args: cobra.NoArgs,
		Annotations: op("listSystemTasks"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ts, err := c.ListSystemTasks(ctx(cmd))
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(ts))
			for _, t := range ts {
				up := "-"
				if t.StartedAt != nil && t.State == "running" {
					up = age(*t.StartedAt)
				}
				rows = append(rows, []string{t.Name, t.State, orDash(t.Health), t.Image, up, orDash(t.Error)})
			}
			return a.printer().table(ts, []string{"NAME", "STATE", "HEALTH", "IMAGE", "STARTED", "ERROR"}, rows)
		},
	})
	return sys
}
