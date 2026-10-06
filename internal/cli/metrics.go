package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

func (a *app) metricsCmd() *cobra.Command {
	var s scope
	var rng string
	cmd := &cobra.Command{
		Use:         "metrics [SERVICE]",
		Short:       "Resource usage: per task of a service, or per service of an environment (§9.1)",
		Example:     "  synctl metrics -p shop            # every service in production\n  synctl metrics web -p shop --range 24h",
		Args:        cobra.MaximumNArgs(1),
		Annotations: op("getServiceMetrics", "getEnvironmentMetrics"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.need(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			service := ""
			if len(args) == 1 {
				service = args[0]
			}
			m, err := c.Metrics(ctx(cmd), s.project, s.env, service, rng)
			if err != nil {
				return err
			}
			// The latest and peak value of each series.
			type row struct{ key, node string }
			latest := map[row]map[string]float64{}
			peak := map[row]float64{}
			for chart, series := range m.Charts {
				for _, sr := range series {
					k := row{sr.Key, sr.Node}
					if latest[k] == nil {
						latest[k] = map[string]float64{}
					}
					if n := len(sr.Points); n > 0 {
						latest[k][chart] = sr.Points[n-1][1]
					}
					if chart == "cpu" {
						for _, p := range sr.Points {
							peak[k] = max(peak[k], p[1])
						}
					}
				}
			}
			keys := make([]row, 0, len(latest))
			for k := range latest {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return keys[i].key < keys[j].key })
			first := "SERVICE"
			if service != "" {
				first = "TASK"
			}
			rows := make([][]string, 0, len(keys))
			for _, k := range keys {
				v := latest[k]
				rows = append(rows, []string{k.key, orDash(k.node), fmt.Sprintf("%.1f%%", v["cpu"]), fmt.Sprintf("%.1f%%", peak[k]),
					bytesHuman(v["memory"]), bytesHuman(v["netRx"]) + "/s", bytesHuman(v["netTx"]) + "/s"})
			}
			return a.printer().table(m, []string{first, "NODE", "CPU", "PEAK CPU (" + rng + ")", "MEMORY", "NET IN", "NET OUT"}, rows)
		},
	}
	a.scopeFlags(cmd, &s)
	cmd.Flags().StringVar(&rng, "range", "1h", "time range: 15m, 1h, 6h, 24h or 7d")
	return cmd
}

func bytesHuman(b float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", b, units[i])
	}
	return fmt.Sprintf("%.1f %s", b, units[i])
}
