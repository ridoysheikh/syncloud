package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

// scope holds --project/--env, defaulting to $SYNCLOUD_PROJECT / $SYNCLOUD_ENV.
type scope struct{ project, env string }

func (a *app) scopeFlags(cmd *cobra.Command, s *scope) {
	cmd.PersistentFlags().StringVarP(&s.project, "project", "p", os.Getenv("SYNCLOUD_PROJECT"), "project (or $SYNCLOUD_PROJECT)")
	env := os.Getenv("SYNCLOUD_ENV")
	if env == "" {
		env = "production"
	}
	cmd.PersistentFlags().StringVarP(&s.env, "env", "e", env, "environment (or $SYNCLOUD_ENV)")
}

func (s scope) need() error {
	if s.project == "" {
		return errors.New("--project (or $SYNCLOUD_PROJECT) is required")
	}
	return nil
}

func (a *app) projectsCmd() *cobra.Command {
	p := &cobra.Command{Use: "projects", Aliases: []string{"project", "proj"}, Short: "Projects (the IAM boundary, §4)"}
	var desc, env string
	create := &cobra.Command{
		Use: "create NAME", Short: "Create a project with a first environment", Args: cobra.ExactArgs(1),
		Annotations: op("createProject"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			pr, err := c.CreateProject(ctx(cmd), args[0], desc, env)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Created project %s with environment %s\n", pr.Name, strings.Join(pr.Environments, ", "))
			return nil
		},
	}
	create.Flags().StringVar(&desc, "description", "", "description")
	create.Flags().StringVar(&env, "environment", "production", "first environment")
	p.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List projects", Args: cobra.NoArgs,
			Annotations: op("listProjects"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				ps, err := c.ListProjects(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(ps))
				for _, x := range ps {
					rows = append(rows, []string{x.Name, strings.Join(x.Environments, ","), orDash(x.Description), age(x.CreatedAt)})
				}
				return a.printer().table(ps, []string{"NAME", "ENVIRONMENTS", "DESCRIPTION", "CREATED"}, rows)
			},
		},
		create,
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete an empty project", Args: cobra.ExactArgs(1),
			Annotations: op("deleteProject"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteProject(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted project %s\n", args[0])
				return nil
			},
		},
	)
	return p
}

func (a *app) envsCmd() *cobra.Command {
	var s scope
	e := &cobra.Command{Use: "envs", Aliases: []string{"env", "environments"}, Short: "Environments of a project"}
	a.scopeFlags(e, &s)
	e.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List environments", Args: cobra.NoArgs,
			Annotations: op("listEnvironments"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := s.need(); err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				es, err := c.ListEnvironments(ctx(cmd), s.project)
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(es))
				for _, x := range es {
					rows = append(rows, []string{x.Name, age(x.CreatedAt)})
				}
				return a.printer().table(es, []string{"NAME", "CREATED"}, rows)
			},
		},
		&cobra.Command{
			Use: "create NAME", Short: "Create an environment", Args: cobra.ExactArgs(1),
			Annotations: op("createEnvironment"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := s.need(); err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				if _, err := c.CreateEnvironment(ctx(cmd), s.project, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Created environment %s/%s\n", s.project, args[0])
				return nil
			},
		},
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete an environment without services", Args: cobra.ExactArgs(1),
			Annotations: op("deleteEnvironment"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := s.need(); err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteEnvironment(ctx(cmd), s.project, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted environment %s/%s\n", s.project, args[0])
				return nil
			},
		},
	)
	return e
}

func (a *app) servicesCmd() *cobra.Command {
	var s scope
	svc := &cobra.Command{Use: "services", Aliases: []string{"service", "svc"}, Short: "Services: desired state, revisions and scaling (§4)"}
	a.scopeFlags(svc, &s)
	withClient := func(f func(cmd *cobra.Command, c *client.Client, args []string) error) func(*cobra.Command, []string) error {
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
	printSvc := func(v client.Service) error {
		if a.output == "json" {
			return a.printer().json(v)
		}
		fmt.Fprintf(a.out, "service/%s/%s/%s revision %d: %d/%d running\n", v.Project, v.Environment, v.Name, v.Revision, v.Running, v.DesiredCount)
		for _, e := range v.Endpoints {
			fmt.Fprintf(a.out, "  %s\n", e)
		}
		if v.VIP != "" {
			fmt.Fprintf(a.out, "  internal: %s (%s)\n", v.DNSName, v.VIP)
		}
		if v.Status != "" {
			fmt.Fprintf(a.out, "  status: %s\n", v.Status)
		}
		if d := v.Deployment; d != nil && d.Status != "succeeded" {
			fmt.Fprintf(a.out, "  deployment %d → %d: %s %s\n", d.FromRevision, d.ToRevision, d.Status, d.Message)
		}
		return nil
	}

	var all bool
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls", "get"}, Short: "List services", Args: cobra.MaximumNArgs(1),
		Annotations: op("listServices", "listAllServices", "getService"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if err := s.need(); err != nil {
					return err
				}
				v, err := c.GetService(ctx(cmd), s.project, s.env, args[0])
				if err != nil {
					return err
				}
				return printSvc(v)
			}
			project := s.project
			if all {
				project = ""
			} else if err := s.need(); err != nil {
				return fmt.Errorf("%w (or -A for all projects)", err)
			}
			vs, err := c.ListServices(ctx(cmd), project, s.env)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(vs))
			for _, v := range vs {
				state := v.Status
				if v.Deleting {
					state = "deleting"
				}
				rows = append(rows, []string{v.Project + "/" + v.Environment + "/" + v.Name, fmt.Sprintf("%d/%d", v.Running, v.DesiredCount),
					fmt.Sprint(v.Revision), v.Spec.Image, joinOrDash(v.Endpoints), orDash(state)})
			}
			return a.printer().table(vs, []string{"SERVICE", "RUNNING", "REV", "IMAGE", "ENDPOINTS", "STATUS"}, rows)
		},
	}
	list.Flags().BoolVarP(&all, "all-projects", "A", false, "list services of every project")

	// run: quick create/update from flags, like `kubectl create deployment`.
	var image string
	var replicas int
	var ports, envs []string
	var cpu float64
	var mem int
	run := &cobra.Command{
		Use: "run NAME --image IMAGE", Short: "Create or update a service from flags", Args: cobra.ExactArgs(1),
		Example: "  synctl services run web -p shop --image nginx:1.27 --port 80 --replicas 2",
		RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
			if image == "" {
				return errors.New("--image is required")
			}
			spec := client.ServiceSpec{Image: image, Env: map[string]string{}}
			for _, p := range ports {
				proto := "http"
				if num, pr, ok := strings.Cut(p, "/"); ok {
					p, proto = num, pr
				}
				n, err := strconv.Atoi(p)
				if err != nil {
					return fmt.Errorf("--port %q: want a number like 8080 or 5432/tcp", p)
				}
				spec.Ports = append(spec.Ports, client.ServicePort{Container: n, Protocol: proto})
			}
			for _, kv := range envs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--env %q: want KEY=value", kv)
				}
				spec.Env[k] = v
			}
			spec.Resources.CPU, spec.Resources.Memory = cpu, mem
			if cmd.Flags().Changed("replicas") {
				spec.DesiredCount = &replicas
			}
			v, err := c.ApplyService(ctx(cmd), s.project, s.env, args[0], spec)
			if err != nil {
				return err
			}
			return printSvc(v)
		}),
	}
	run.Flags().StringVar(&image, "image", "", "container image")
	run.Flags().IntVar(&replicas, "replicas", 1, "desired task count")
	run.Flags().StringArrayVar(&ports, "port", nil, "container port; 8080 (http) or 5432/tcp (repeatable)")
	run.Flags().StringArrayVar(&envs, "env-var", nil, "environment variable KEY=value (repeatable)")
	run.Flags().Float64Var(&cpu, "cpu", 0, "CPU cores reserved (default 0.1)")
	run.Flags().IntVar(&mem, "memory", 0, "memory reserved in MiB (default 128)")

	var file string
	apply := &cobra.Command{
		Use: "apply -f FILE", Short: "Create or update a service from JSON ({\"name\": …, \"image\": …})", Args: cobra.NoArgs,
		Annotations: op("applyService"),
		RunE: withClient(func(cmd *cobra.Command, c *client.Client, _ []string) error {
			var r io.Reader = a.in
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				r = f
			}
			var doc struct {
				Name string `json:"name"`
				client.ServiceSpec
			}
			if err := json.NewDecoder(r).Decode(&doc); err != nil {
				return fmt.Errorf("parse service: %w", err)
			}
			if doc.Name == "" {
				return errors.New(`the file needs a "name"`)
			}
			v, err := c.ApplyService(ctx(cmd), s.project, s.env, doc.Name, doc.ServiceSpec)
			if err != nil {
				return err
			}
			return printSvc(v)
		}),
	}
	apply.Flags().StringVarP(&file, "file", "f", "", "service JSON file, or - for stdin")
	_ = apply.MarkFlagRequired("file")

	domains := &cobra.Command{Use: "domains", Short: "Custom domains of a service"}
	var port string
	add := &cobra.Command{
		Use: "add SERVICE HOST", Short: "Route a custom domain to a service", Args: cobra.ExactArgs(2),
		Annotations: op("addServiceDomain"),
		RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
			chk, err := c.AddServiceDomain(ctx(cmd), s.project, s.env, args[0], args[1], port)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Routing %s to %s.\n", args[1], args[0])
			printCheck(a, chk)
			return nil
		}),
	}
	add.Flags().StringVar(&port, "port", "", "http port name (default: the first)")
	domains.AddCommand(add,
		&cobra.Command{
			Use: "list SERVICE", Aliases: []string{"ls"}, Short: "List a service's custom domains", Args: cobra.ExactArgs(1),
			Annotations: op("listServiceDomains"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				ds, err := c.ListServiceDomains(ctx(cmd), s.project, s.env, args[0])
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(ds))
				for _, d := range ds {
					dns := "ok"
					if !d.DNS.Ready {
						dns = "waiting for " + orDash(d.DNS.Record)
					}
					rows = append(rows, []string{d.Host, d.Port, dns})
				}
				return a.printer().table(ds, []string{"HOST", "PORT", "DNS"}, rows)
			}),
		},
		&cobra.Command{
			Use: "remove SERVICE HOST", Aliases: []string{"rm"}, Short: "Stop routing a custom domain", Args: cobra.ExactArgs(2),
			Annotations: op("removeServiceDomain"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				if err := c.RemoveServiceDomain(ctx(cmd), s.project, s.env, args[0], args[1]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Removed %s from %s\n", args[1], args[0])
				return nil
			}),
		},
		&cobra.Command{
			Use: "check HOST", Short: "Check that a hostname points at this platform", Args: cobra.ExactArgs(1),
			Annotations: op("checkDomain"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				chk, err := c.CheckDomain(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				printCheck(a, chk)
				return nil
			},
		},
	)
	svc.AddCommand(list, run, apply, domains,
		&cobra.Command{
			Use: "scale NAME=COUNT", Short: "Set the desired task count", Args: cobra.ExactArgs(1),
			Annotations: op("scaleService"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				name, n, ok := strings.Cut(args[0], "=")
				count, err := strconv.Atoi(n)
				if !ok || err != nil {
					return errors.New("use NAME=COUNT, e.g. api=3")
				}
				v, err := c.ScaleService(ctx(cmd), s.project, s.env, name, count)
				if err != nil {
					return err
				}
				return printSvc(v)
			}),
		},
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Stop all tasks and delete a service", Args: cobra.ExactArgs(1),
			Annotations: op("deleteService"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				if err := c.DeleteService(ctx(cmd), s.project, s.env, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleting service %s/%s/%s\n", s.project, s.env, args[0])
				return nil
			}),
		},
		&cobra.Command{
			Use: "tasks NAME", Aliases: []string{"ps"}, Short: "Tasks of a service", Args: cobra.ExactArgs(1),
			Annotations: op("listServiceTasks"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				ts, err := c.ServiceTasks(ctx(cmd), s.project, s.env, args[0])
				if err != nil {
					return err
				}
				return a.printTasks(ts)
			}),
		},
		&cobra.Command{
			Use: "revisions NAME", Aliases: []string{"history"}, Short: "Revisions of a service", Args: cobra.ExactArgs(1),
			Annotations: op("listServiceRevisions"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				rs, err := c.ServiceRevisions(ctx(cmd), s.project, s.env, args[0])
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(rs))
				for _, r := range rs {
					cur := ""
					if r.Current {
						cur = "*"
					}
					rows = append(rows, []string{fmt.Sprint(r.Revision) + cur, r.Spec.Image, age(r.CreatedAt)})
				}
				return a.printer().table(rs, []string{"REVISION", "IMAGE", "CREATED"}, rows)
			}),
		},
		&cobra.Command{
			Use: "deployments NAME", Aliases: []string{"rollouts"}, Short: "Rollouts of a service, newest first", Args: cobra.ExactArgs(1),
			Annotations: op("listServiceDeployments"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				ds, err := c.ServiceDeployments(ctx(cmd), s.project, s.env, args[0])
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(ds))
				for _, d := range ds {
					rows = append(rows, []string{fmt.Sprintf("%d → %d", d.FromRevision, d.ToRevision), d.Status, fmt.Sprint(d.FailedTasks), age(d.StartedAt), orDash(d.Message)})
				}
				return a.printer().table(ds, []string{"REVISIONS", "STATUS", "FAILED", "STARTED", "MESSAGE"}, rows)
			}),
		},
		&cobra.Command{
			Use: "rollback NAME REVISION", Short: "Roll out an earlier revision again", Args: cobra.ExactArgs(2),
			Annotations: op("rollbackService"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				rev, err := strconv.Atoi(args[1])
				if err != nil {
					return errors.New("REVISION must be a number")
				}
				v, err := c.RollbackService(ctx(cmd), s.project, s.env, args[0], rev)
				if err != nil {
					return err
				}
				return printSvc(v)
			}),
		},
	)
	return svc
}

func (a *app) printTasks(ts []client.Task) error {
	rows := make([][]string, 0, len(ts))
	for _, t := range ts {
		state := t.State
		if t.Desired == "stopped" && (t.State == "running" || t.State == "starting") {
			state += " (stopping)"
		}
		started := "-"
		if t.StartedAt != nil {
			started = age(*t.StartedAt)
		}
		rows = append(rows, []string{t.ID, t.Project + "/" + t.Environment + "/" + t.Service, fmt.Sprint(t.Revision), orDash(t.Node), state, orDash(t.IP), started, orDash(t.Error)})
	}
	return a.printer().table(ts, []string{"TASK", "SERVICE", "REV", "NODE", "STATE", "IP", "STARTED", "ERROR"}, rows)
}

func (a *app) tasksCmd() *cobra.Command {
	t := &cobra.Command{Use: "tasks", Aliases: []string{"task"}, Short: "Running containers across all nodes"}
	t.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls", "get"}, Short: "List active tasks", Args: cobra.NoArgs,
			Annotations: op("listTasks"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				ts, err := c.ListTasks(ctx(cmd))
				if err != nil {
					return err
				}
				return a.printTasks(ts)
			},
		},
		&cobra.Command{
			Use: "restart ID", Short: "Stop a task; the service starts a replacement", Args: cobra.ExactArgs(1),
			Annotations: op("restartTask"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.RestartTask(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Restarting %s\n", args[0])
				return nil
			},
		},
	)
	return t
}

func printCheck(a *app, chk client.DomainCheck) {
	if a.output == "json" {
		_ = a.printer().json(chk)
		return
	}
	if chk.Ready {
		fmt.Fprintf(a.out, "DNS ok: %s resolves to %s. The certificate is requested automatically.\n", chk.Host, chk.Expected)
		return
	}
	now := "nothing"
	if len(chk.Addresses) > 0 {
		now = strings.Join(chk.Addresses, ", ")
	}
	fmt.Fprintf(a.out, "Create this DNS record: %s\n(%s resolves to %s now)\n", orDash(chk.Record), chk.Host, now)
}
