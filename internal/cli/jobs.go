package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ridoysheikh/syncloud/internal/client"
)

func runRows(rs []client.JobRun) [][]string {
	rows := make([][]string, 0, len(rs))
	for _, r := range rs {
		dur, code := "-", "-"
		if r.StartedAt != nil && r.FinishedAt != nil {
			dur = r.FinishedAt.Sub(*r.StartedAt).Round(time.Second).String()
		}
		if r.ExitCode != nil {
			code = fmt.Sprint(*r.ExitCode)
		}
		rows = append(rows, []string{r.ID, r.Status, r.Trigger, fmt.Sprint(r.Attempt), orDash(r.Node), code, dur, age(r.CreatedAt), orDash(r.Message)})
	}
	return rows
}

var runHeaders = []string{"RUN", "STATUS", "TRIGGER", "TRY", "NODE", "EXIT", "DURATION", "CREATED", "MESSAGE"}

func (a *app) jobsCmd() *cobra.Command {
	var s scope
	j := &cobra.Command{Use: "jobs", Aliases: []string{"job"}, Short: "One-off, scheduled and deploy-hook jobs (§5.11)"}
	a.scopeFlags(j, &s)
	with := func(f func(*cobra.Command, *client.Client, []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			if err := s.need(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			return f(cmd, c, args)
		}
	}
	var file string
	apply := &cobra.Command{
		Use: "apply -f FILE", Short: "Create or update a job from JSON ({\"name\": …, \"kind\": …})", Args: cobra.NoArgs,
		Annotations: op("applyJob"),
		Example:     `  echo '{"name":"nightly","service":"api","command":["node","report.js"],"schedule":"0 2 * * *","timezone":"Asia/Dhaka"}' | synctl jobs apply -p shop -f -`,
		RunE: with(func(cmd *cobra.Command, c *client.Client, _ []string) error {
			var r io.Reader = a.in
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				r = f
			}
			var doc map[string]any
			if err := json.NewDecoder(r).Decode(&doc); err != nil {
				return fmt.Errorf("parse job: %w", err)
			}
			name, _ := doc["name"].(string)
			if name == "" {
				return errors.New(`the file needs a "name"`)
			}
			delete(doc, "name")
			job, err := c.ApplyJob(ctx(cmd), s.project, s.env, name, doc)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "job/%s/%s/%s applied", job.Project, job.Environment, job.Name)
			if job.NextRunAt != nil {
				fmt.Fprintf(a.out, "; next run %s", job.NextRunAt.Local().Format(time.RFC1123))
			}
			fmt.Fprintln(a.out)
			return nil
		}),
	}
	apply.Flags().StringVarP(&file, "file", "f", "", "job JSON file, or - for stdin")
	_ = apply.MarkFlagRequired("file")

	var all bool
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List jobs", Args: cobra.NoArgs,
		Annotations: op("listJobs", "listAllJobs", "getJob"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			project := s.project
			if all {
				project = ""
			} else if err := s.need(); err != nil {
				return fmt.Errorf("%w (or -A)", err)
			}
			js, err := c.ListJobs(ctx(cmd), project, s.env)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(js))
			for _, x := range js {
				next, last := "-", "-"
				if x.NextRunAt != nil {
					next = x.NextRunAt.Local().Format("Jan 2 15:04")
				}
				if x.LastRun != nil {
					last = x.LastRun.Status + " " + age(x.LastRun.CreatedAt)
				}
				kind, _ := x.Spec["kind"].(string)
				sched, _ := x.Spec["schedule"].(string)
				rows = append(rows, []string{x.Project + "/" + x.Environment + "/" + x.Name, kind, orDash(sched), next, last})
			}
			return a.printer().table(js, []string{"JOB", "KIND", "SCHEDULE", "NEXT RUN", "LAST RUN"}, rows)
		},
	}
	list.Flags().BoolVarP(&all, "all-projects", "A", false, "jobs of every project")

	var wait bool
	run := &cobra.Command{
		Use: "run NAME [-- COMMAND...]", Short: "Run a job now", Args: cobra.MinimumNArgs(1),
		Annotations: op("runJob"),
		RunE: with(func(cmd *cobra.Command, c *client.Client, args []string) error {
			r, err := c.RunJob(ctx(cmd), s.project, s.env, args[0], args[1:])
			if err != nil {
				return err
			}
			return a.followRun(cmd, c, r, wait)
		}),
	}
	run.Flags().BoolVarP(&wait, "wait", "w", false, "stream the run's logs and exit with its exit code")

	j.AddCommand(list, apply, run,
		&cobra.Command{
			Use: "runs NAME", Short: "Run history of a job", Args: cobra.ExactArgs(1),
			Annotations: op("listJobRuns"),
			RunE: with(func(cmd *cobra.Command, c *client.Client, args []string) error {
				rs, err := c.JobRuns(ctx(cmd), s.project, s.env, args[0])
				if err != nil {
					return err
				}
				return a.printer().table(rs, runHeaders, runRows(rs))
			}),
		},
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a job", Args: cobra.ExactArgs(1),
			Annotations: op("deleteJob"),
			RunE: with(func(cmd *cobra.Command, c *client.Client, args []string) error {
				if err := c.DeleteJob(ctx(cmd), s.project, s.env, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted job %s\n", args[0])
				return nil
			}),
		},
	)
	return j
}

// runCmd is `synctl run service/NAME -- COMMAND…` (§5.11 one-off task).
func (a *app) runCmd() *cobra.Command {
	var s scope
	var detach bool
	cmd := &cobra.Command{
		Use:         "run service/NAME -- COMMAND...",
		Short:       "Run a command once with a service's task definition (e.g. a migration)",
		Example:     "  synctl run service/api -p shop -- rails db:migrate",
		Args:        cobra.MinimumNArgs(2),
		Annotations: op("runService"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.need(); err != nil {
				return err
			}
			name, ok := strings.CutPrefix(args[0], "service/")
			if !ok {
				return errors.New("the target must be service/NAME")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := c.RunService(ctx(cmd), s.project, s.env, name, args[1:])
			if err != nil {
				return err
			}
			return a.followRun(cmd, c, r, !detach)
		},
	}
	a.scopeFlags(cmd, &s)
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "print the run ID and return immediately")
	return cmd
}

func (a *app) runsCmd() *cobra.Command {
	r := &cobra.Command{Use: "runs", Short: "Job runs"}
	r.AddCommand(
		&cobra.Command{
			Use: "get ID", Short: "Show a run", Args: cobra.ExactArgs(1),
			Annotations: op("getRun"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				x, err := c.GetRun(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				return a.printer().table(x, runHeaders, runRows([]client.JobRun{x}))
			},
		},
		&cobra.Command{
			Use: "cancel ID", Short: "Stop an active run", Args: cobra.ExactArgs(1),
			Annotations: op("cancelRun"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.CancelRun(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Cancelling %s\n", args[0])
				return nil
			},
		},
	)
	return r
}

// followRun streams a run's logs until it finishes and returns its exit code.
func (a *app) followRun(cmd *cobra.Command, c *client.Client, r client.JobRun, wait bool) error {
	if !wait {
		fmt.Fprintf(a.out, "Started %s. Follow it with: synctl logs -A --task %s -f\n", r.ID, r.ID)
		return nil
	}
	fmt.Fprintf(a.errOut, "Run %s started\n", r.ID)
	lctx, stop := context.WithCancel(ctx(cmd))
	defer stop()
	go func() {
		_ = c.TailLogs(lctx, client.LogQuery{Task: r.ID}, func(l client.LogLine) { fmt.Fprintln(a.out, l.Message) })
	}()
	for {
		select {
		case <-ctx(cmd).Done():
			return ctx(cmd).Err()
		case <-time.After(time.Second):
		}
		x, err := c.GetRun(ctx(cmd), r.ID)
		if err != nil {
			return err
		}
		switch x.Status {
		case "pending", "running":
			continue
		}
		time.Sleep(1500 * time.Millisecond) // let the last log lines arrive
		stop()
		fmt.Fprintf(a.errOut, "Run %s %s", x.ID, x.Status)
		if x.Message != "" {
			fmt.Fprintf(a.errOut, ": %s", x.Message)
		}
		fmt.Fprintln(a.errOut)
		if x.Status == "succeeded" {
			return nil
		}
		if x.ExitCode != nil && *x.ExitCode != 0 {
			return exitError(*x.ExitCode)
		}
		return exitError(1)
	}
}
