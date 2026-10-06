package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func (a *app) autoscaleCmd() *cobra.Command {
	var s scope
	cmd := &cobra.Command{Use: "autoscale", Aliases: []string{"autoscaling", "as"}, Short: "Target tracking autoscaling of services (§5.5)"}
	a.scopeFlags(cmd, &s)
	need := func() (*client.Client, error) {
		if err := s.need(); err != nil {
			return nil, err
		}
		return a.client()
	}
	print := func(v client.Autoscaling) error {
		if a.output == "json" {
			return a.printer().json(v)
		}
		if v.Policy == nil {
			fmt.Fprintln(a.out, "Not autoscaled.")
			return nil
		}
		p := v.Policy
		state := "on"
		if !p.Enabled {
			state = "paused"
		}
		fmt.Fprintf(a.out, "Autoscaling %s: %d–%d tasks, %s target %g (%s)\n", state, p.Min, p.Max, p.Metric, p.Target, v.Units[p.Metric])
		fmt.Fprintf(a.out, "  cooldowns: out %ds, in %ds after %d checks below target\n", p.ScaleOutCooldown, p.ScaleInCooldown, p.ScaleInChecks)
		if v.Current.Value != nil {
			fmt.Fprintf(a.out, "  now:       %.1f (%s)\n", *v.Current.Value, ago(v.Current.EvaluatedAt))
		} else if v.Current.EvaluatedAt != nil {
			fmt.Fprintf(a.out, "  now:       no data (%s)\n", ago(v.Current.EvaluatedAt))
		}
		return nil
	}

	var p client.ScalingPolicy
	var cpu, mem, rps, lat float64
	var paused bool
	set := &cobra.Command{
		Use: "set SERVICE --min N --max N (--cpu|--memory|--rps|--latency) TARGET", Short: "Keep a metric near its target by adding and removing tasks",
		Example: "  synctl autoscale set web -p shop --min 2 --max 10 --cpu 60\n  synctl autoscale set api -p shop --min 1 --max 20 --rps 50",
		Args:    cobra.ExactArgs(1), Annotations: op("putAutoscaling"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := need()
			if err != nil {
				return err
			}
			n := 0
			for m, v := range map[string]float64{"cpu": cpu, "memory": mem, "rps": rps, "latency": lat} {
				if v != 0 {
					p.Metric, p.Target = m, v
					n++
				}
			}
			if n != 1 {
				return errors.New("give exactly one target: --cpu, --memory, --rps or --latency")
			}
			p.Enabled = !paused
			v, err := c.PutAutoscaling(ctx(cmd), s.project, s.env, args[0], p)
			if err != nil {
				return err
			}
			return print(v)
		},
	}
	set.Flags().IntVar(&p.Min, "min", 1, "fewest tasks")
	set.Flags().IntVar(&p.Max, "max", 0, "most tasks")
	set.Flags().Float64Var(&cpu, "cpu", 0, "target average CPU, % of the reservation")
	set.Flags().Float64Var(&mem, "memory", 0, "target average memory, % of the reservation")
	set.Flags().Float64Var(&rps, "rps", 0, "target requests per second per task")
	set.Flags().Float64Var(&lat, "latency", 0, "target p95 latency in ms")
	set.Flags().IntVar(&p.ScaleOutCooldown, "scale-out-cooldown", 0, "seconds between scale-outs (default 60)")
	set.Flags().IntVar(&p.ScaleInCooldown, "scale-in-cooldown", 0, "seconds after a change before scaling in (default 300)")
	set.Flags().IntVar(&p.ScaleInChecks, "scale-in-checks", 0, "15s checks below target before scaling in (default 4)")
	set.Flags().BoolVar(&paused, "paused", false, "save the policy without acting on it")
	_ = set.MarkFlagRequired("max")

	var limit int
	history := &cobra.Command{
		Use: "history SERVICE", Short: "The autoscaler's changes and why", Args: cobra.ExactArgs(1),
		Annotations: op("listScalingEvents"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := need()
			if err != nil {
				return err
			}
			evs, err := c.ScalingEvents(ctx(cmd), s.project, s.env, args[0], limit)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(evs))
			for _, e := range evs {
				rows = append(rows, []string{age(e.At), fmt.Sprintf("%d → %d", e.From, e.To), e.Reason})
			}
			return a.printer().table(evs, []string{"WHEN", "TASKS", "REASON"}, rows)
		},
	}
	history.Flags().IntVar(&limit, "limit", 20, "how many changes to show")

	cmd.AddCommand(set, history,
		&cobra.Command{
			Use: "get SERVICE", Short: "Show a service's policy and the latest measurement", Args: cobra.ExactArgs(1),
			Annotations: op("getAutoscaling"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := need()
				if err != nil {
					return err
				}
				v, err := c.GetAutoscaling(ctx(cmd), s.project, s.env, args[0])
				if err != nil {
					return err
				}
				return print(v)
			},
		},
		&cobra.Command{
			Use: "off SERVICE", Aliases: []string{"delete", "rm"}, Short: "Stop autoscaling (the task count stays where it is)", Args: cobra.ExactArgs(1),
			Annotations: op("deleteAutoscaling"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := need()
				if err != nil {
					return err
				}
				if err := c.DeleteAutoscaling(ctx(cmd), s.project, s.env, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s is no longer autoscaled\n", args[0])
				return nil
			},
		},
	)
	return cmd
}
