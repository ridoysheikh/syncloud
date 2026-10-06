package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func buildRows(bs []client.Build) [][]string {
	rows := make([][]string, 0, len(bs))
	for _, b := range bs {
		dur := "-"
		if b.StartedAt != nil && b.FinishedAt != nil {
			dur = b.FinishedAt.Sub(*b.StartedAt).Round(time.Second).String()
		}
		sha := b.SHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		deployed := "-"
		if b.Deployed {
			deployed = "yes"
		}
		rows = append(rows, []string{b.ID, b.Project + "/" + b.Environment + "/" + b.Service, sha, b.Status, b.Trigger, deployed, dur, age(b.CreatedAt), orDash(b.Message)})
	}
	return rows
}

var buildHeaders = []string{"BUILD", "SERVICE", "COMMIT", "STATUS", "TRIGGER", "DEPLOYED", "DURATION", "CREATED", "MESSAGE"}

func (a *app) buildsCmd() *cobra.Command {
	var s scope
	b := &cobra.Command{Use: "builds", Aliases: []string{"build"}, Short: "Git sources and image builds (§5.8)"}
	a.scopeFlags(b, &s)
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
	printSource := func(src client.GitSource) error {
		if a.output == "json" {
			return a.printer().json(src)
		}
		fmt.Fprintf(a.out, "%s (branch %s", src.URL, src.Branch)
		if src.Tags != "" {
			fmt.Fprintf(a.out, ", tags %s", src.Tags)
		}
		if src.Builder != "" && src.Builder != "auto" {
			fmt.Fprintf(a.out, ", %s", src.Builder)
		}
		if src.Builder != "nixpacks" && src.Builder != "static" {
			fmt.Fprintf(a.out, ", %s", src.Dockerfile)
		}
		if src.Context != "" {
			fmt.Fprintf(a.out, " in %s", src.Context)
		}
		fmt.Fprintf(a.out, "), polled every %ds", src.PollSeconds)
		if src.AutoDeploy != nil && *src.AutoDeploy {
			fmt.Fprint(a.out, ", auto-deploy")
		}
		fmt.Fprintln(a.out)
		if len(src.Paths) > 0 {
			fmt.Fprintf(a.out, "  paths:   %s\n", strings.Join(src.Paths, " "))
		}
		refs := make([]string, 0, len(src.Refs))
		for r := range src.Refs {
			refs = append(refs, r)
		}
		sort.Strings(refs)
		for _, r := range refs {
			fmt.Fprintf(a.out, "  %-8s %s\n", r+":", src.Refs[r][:min(12, len(src.Refs[r]))])
		}
		if len(refs) == 0 && src.LastSHA != "" {
			fmt.Fprintf(a.out, "  head:    %s\n", src.LastSHA)
		}
		if src.LastError != "" {
			fmt.Fprintf(a.out, "  error:   %s\n", src.LastError)
		}
		fmt.Fprintf(a.out, "  webhook: <dashboard URL>%s\n  secret:  %s\n", src.WebhookPath, src.WebhookSecret)
		return nil
	}

	var in client.GitSource
	var noAuto bool
	set := &cobra.Command{
		Use: "connect SERVICE --url URL", Short: "Build and deploy a service from a Git repository", Args: cobra.ExactArgs(1),
		Annotations: op("setGitSource"),
		Example:     "  SYNCLOUD_GIT_TOKEN=ghp_… synctl builds connect api -p shop --url https://github.com/acme/api.git --branch main",
		RunE: with(func(cmd *cobra.Command, c *client.Client, args []string) error {
			src := in
			if src.Token == "" {
				src.Token = os.Getenv("SYNCLOUD_GIT_TOKEN")
			}
			auto := !noAuto
			src.AutoDeploy = &auto
			out, err := c.SetGitSource(ctx(cmd), s.project, s.env, args[0], src)
			if err != nil {
				return err
			}
			return printSource(out)
		}),
	}
	set.Flags().StringVar(&in.URL, "url", "", "https:// clone URL")
	set.Flags().StringVar(&in.Branch, "branch", "main", `branch to build, or a pattern such as "release/*"`)
	set.Flags().StringVar(&in.Tags, "tags", "", `also build new tags matching this pattern, e.g. "v*"`)
	set.Flags().StringArrayVar(&in.Paths, "path", nil, `only build commits changing these paths (repeatable; "!" excludes), e.g. "services/api/**"`)
	set.Flags().StringVar(&in.Builder, "builder", "auto", "auto (Dockerfile, then Nixpacks, then static), dockerfile, nixpacks or static")
	set.Flags().StringVar(&in.Dockerfile, "dockerfile", "Dockerfile", "Dockerfile path, relative to the context")
	set.Flags().StringVar(&in.Context, "context", "", "build context directory in the repository")
	set.Flags().StringVar(&in.Token, "token", "", "access token for a private repository (default $SYNCLOUD_GIT_TOKEN)")
	set.Flags().IntVar(&in.PollSeconds, "poll", 60, "seconds between branch checks")
	set.Flags().BoolVar(&noAuto, "no-auto-deploy", false, "build new commits but deploy them by hand")
	_ = set.MarkFlagRequired("url")

	var all bool
	list := &cobra.Command{
		Use: "list [SERVICE]", Aliases: []string{"ls"}, Short: "List a service's builds, or recent builds with -A", Args: cobra.MaximumNArgs(1),
		Annotations: op("listServiceBuilds", "listBuilds"),
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
			} else if !all {
				return errors.New("name a service, or use -A for recent builds everywhere")
			}
			bs, err := c.ListBuilds(ctx(cmd), s.project, s.env, service)
			if err != nil {
				return err
			}
			return a.printer().table(bs, buildHeaders, buildRows(bs))
		},
	}
	list.Flags().BoolVarP(&all, "all-projects", "A", false, "recent builds of every service")

	var sha, ref string
	var wait bool
	run := &cobra.Command{
		Use: "run SERVICE", Short: "Build the branch head (or --sha) now", Args: cobra.ExactArgs(1),
		Annotations: op("startBuild"),
		RunE: with(func(cmd *cobra.Command, c *client.Client, args []string) error {
			x, err := c.StartBuild(ctx(cmd), s.project, s.env, args[0], ref, sha)
			if err != nil {
				return err
			}
			if !wait {
				fmt.Fprintf(a.out, "Queued %s (%s)\n", x.ID, x.SHA[:12])
				return nil
			}
			return a.followBuild(cmd, c, x)
		}),
	}
	run.Flags().StringVar(&sha, "sha", "", "full commit hash to build (default: the branch head)")
	run.Flags().StringVar(&ref, "ref", "", "branch or tag whose head to build (default: the source's branch)")
	run.Flags().BoolVarP(&wait, "wait", "w", false, "stream the build log and fail if the build fails")

	sources := &cobra.Command{
		Use: "sources", Short: "List every Git source and the service it builds", Args: cobra.NoArgs,
		Annotations: op("listGitSources"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ss, err := c.ListGitSources(ctx(cmd))
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(ss))
			for _, x := range ss {
				watch := x.Branch
				if x.Tags != "" {
					watch += " +tags " + x.Tags
				}
				status := "ok"
				if x.LastError != "" {
					status = x.LastError
				}
				rows = append(rows, []string{x.Project + "/" + x.Environment + "/" + x.Service, x.URL, watch, orDash(x.Builder), ago(x.LastCheckedAt), status})
			}
			return a.printer().table(ss, []string{"SERVICE", "REPOSITORY", "WATCHING", "BUILDER", "CHECKED", "STATUS"}, rows)
		},
	}
	b.AddCommand(set, list, run, sources,
		&cobra.Command{
			Use: "source SERVICE", Short: "Show a service's Git source and webhook", Args: cobra.ExactArgs(1),
			Annotations: op("getGitSource"),
			RunE: with(func(cmd *cobra.Command, c *client.Client, args []string) error {
				src, err := c.GetGitSource(ctx(cmd), s.project, s.env, args[0])
				if err != nil {
					return err
				}
				return printSource(src)
			}),
		},
		&cobra.Command{
			Use: "disconnect SERVICE", Short: "Stop building a service from Git", Args: cobra.ExactArgs(1),
			Annotations: op("deleteGitSource"),
			RunE: with(func(cmd *cobra.Command, c *client.Client, args []string) error {
				if err := c.DeleteGitSource(ctx(cmd), s.project, s.env, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Disconnected %s from Git\n", args[0])
				return nil
			}),
		},
		&cobra.Command{
			Use: "deploy BUILD", Short: "Deploy a succeeded build as a new revision", Args: cobra.ExactArgs(1),
			Annotations: op("deployBuild"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				x, err := c.DeployBuild(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deploying %s to %s/%s/%s\n", x.Image, x.Project, x.Environment, x.Service)
				return nil
			},
		},
	)
	return b
}

// followBuild streams a build's log until it finishes.
func (a *app) followBuild(cmd *cobra.Command, c *client.Client, x client.Build) error {
	fmt.Fprintf(a.errOut, "Build %s queued (%s)\n", x.ID, x.SHA[:12])
	lctx, stop := context.WithCancel(ctx(cmd))
	defer stop()
	tailing := false
	for {
		select {
		case <-ctx(cmd).Done():
			return ctx(cmd).Err()
		case <-time.After(time.Second):
		}
		bs, err := c.ListBuilds(ctx(cmd), x.Project, x.Environment, x.Service)
		if err != nil {
			return err
		}
		for _, b := range bs {
			if b.ID != x.ID {
				continue
			}
			if b.RunID != "" && !tailing {
				tailing = true
				go func() {
					_ = c.TailLogs(lctx, client.LogQuery{Task: b.RunID}, func(l client.LogLine) { fmt.Fprintln(a.out, l.Message) })
				}()
			}
			switch b.Status {
			case "succeeded":
				time.Sleep(time.Second) // let the last log lines arrive
				fmt.Fprintf(a.errOut, "Build %s succeeded: %s", b.ID, b.Image)
				if b.Deployed {
					fmt.Fprint(a.errOut, " (deploying)")
				}
				fmt.Fprintln(a.errOut)
				if b.Message != "" {
					return errors.New(b.Message)
				}
				return nil
			case "skipped":
				time.Sleep(time.Second)
				fmt.Fprintf(a.errOut, "Build %s skipped: %s\n", b.ID, b.Message)
				return nil
			case "failed":
				time.Sleep(time.Second)
				return fmt.Errorf("build %s failed: %s", b.ID, b.Message)
			}
		}
	}
}
