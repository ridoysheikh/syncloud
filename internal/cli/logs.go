package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ridoysheikh/syncloud/internal/client"
)

func (a *app) logsCmd() *cobra.Command {
	var s scope
	var q client.LogQuery
	var follow, all bool
	cmd := &cobra.Command{
		Use:   "logs [service/]NAME",
		Short: "Merged logs of a service from every node (§9.2)",
		Example: `  synctl logs web -p shop
  synctl logs -f service/web -p shop --grep timeout
  synctl logs -A --since 15m        # every service`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: op("queryLogs", "tailLogs"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if err := s.need(); err != nil {
					return err
				}
				q.Project, q.Environment, q.Service = s.project, s.env, strings.TrimPrefix(args[0], "service/")
			} else if !all {
				return fmt.Errorf("give a service, or -A for all logs")
			}
			print := func(l client.LogLine) {
				if a.output == "json" {
					_ = a.printer().json(l)
					return
				}
				task := strings.TrimPrefix(l.TaskID, "task_")
				if len(task) > 6 {
					task = task[:6]
				}
				src := l.Service + "/" + task
				if all {
					src = l.Project + "/" + l.Environment + "/" + src
				}
				fmt.Fprintf(a.out, "%s %s %-8s %s\n", l.Time.Local().Format("15:04:05.000"), src, l.Node, l.Message)
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
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new lines")
	cmd.Flags().BoolVarP(&all, "all", "A", false, "logs of every service")
	cmd.Flags().StringVar(&q.Since, "since", "1h", "how far back, e.g. 15m, 24h")
	cmd.Flags().IntVar(&q.Limit, "tail", 200, "lines of history to show")
	cmd.Flags().StringVar(&q.Text, "grep", "", "only lines containing this text (case-insensitive)")
	cmd.Flags().StringVar(&q.Task, "task", "", "only this task")
	cmd.Flags().StringVar(&q.Node, "node", "", "only this node")
	return cmd
}
