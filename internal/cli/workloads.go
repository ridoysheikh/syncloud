package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ridoysheikh/syncloud/internal/client"
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
	var depEnv, depService, depStatus string
	deployments := &cobra.Command{
		Use: "deployments PROJECT", Short: "Deployments of every service in a project, newest first", Args: cobra.ExactArgs(1),
		Annotations: op("listProjectDeployments"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ds, err := c.ProjectDeployments(ctx(cmd), args[0], depEnv, depService, depStatus)
			if err != nil {
				return err
			}
			return a.printDeployments(ds, true)
		},
	}
	deployments.Flags().StringVarP(&depEnv, "environment", "e", "", "only this environment")
	deployments.Flags().StringVar(&depService, "service", "", "only this service")
	deployments.Flags().StringVar(&depStatus, "status", "", "only this status (in_progress, succeeded, failed, rolled_back, cancelled, …)")
	var updDesc string
	var updWindow int
	update := &cobra.Command{
		Use: "update PROJECT", Short: "Change a project's description or rollback window", Args: cobra.ExactArgs(1),
		Annotations: op("updateProject"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var desc *string
			var window *int
			if cmd.Flags().Changed("description") {
				desc = &updDesc
			}
			if cmd.Flags().Changed("rollback-window") {
				window = &updWindow
			}
			if desc == nil && window == nil {
				return errors.New("nothing to change: give --description or --rollback-window")
			}
			pr, err := c.UpdateProject(ctx(cmd), args[0], desc, window)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Updated project %s (rollback window: %d revisions)\n", pr.Name, pr.RollbackWindow)
			return nil
		},
	}
	update.Flags().StringVar(&updDesc, "description", "", "description")
	update.Flags().IntVar(&updWindow, "rollback-window", 10, "revisions whose images registry cleanup keeps (1–50)")
	var addrEnv string
	addresses := &cobra.Command{
		Use: "addresses PROJECT", Aliases: []string{"domains"}, Short: "Every address of a project's services: generated, custom domains and public ports", Args: cobra.ExactArgs(1),
		Annotations: op("listProjectAddresses"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			as, err := c.ProjectAddresses(ctx(cmd), args[0], addrEnv)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(as))
			for _, x := range as {
				state := "-"
				if x.DNS != nil && !x.DNS.Ready {
					state = "waiting for DNS"
				} else if x.Certificate != nil {
					state = "certificate " + x.Certificate.Status
				}
				target := x.Service + ":" + x.Port
				if x.RedirectTo != "" {
					target = "→ " + x.RedirectTo
				}
				rows = append(rows, []string{x.Kind, x.Address, target, x.Protocol, state})
			}
			return a.printer().table(as, []string{"KIND", "ADDRESS", "TARGET", "PROTOCOL", "STATE"}, rows)
		},
	}
	addresses.Flags().StringVarP(&addrEnv, "environment", "e", "production", "environment")
	var everything bool
	deleteProjectCmd := &cobra.Command{
		Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a project (empty, or --everything)", Args: cobra.ExactArgs(1),
		Annotations: op("deleteProject"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			pending, err := c.DeleteProject(ctx(cmd), args[0], everything)
			if err != nil {
				return err
			}
			if pending {
				fmt.Fprintf(a.out, "Deleting project %s: its services stop first, then it goes\n", args[0])
			} else {
				fmt.Fprintf(a.out, "Deleted project %s\n", args[0])
			}
			return nil
		},
	}
	deleteProjectCmd.Flags().BoolVar(&everything, "everything", false, "delete its services too (databases must be deleted first)")
	p.AddCommand(
		addresses,
		update,
		deployments,
		a.projectNodesCmd(),
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
		deleteProjectCmd,
	)
	return p
}

func (a *app) envsCmd() *cobra.Command {
	var s scope
	e := &cobra.Command{Use: "envs", Aliases: []string{"env", "environments"}, Short: "Environments of a project"}
	a.scopeFlags(e, &s)
	var cloneFrom, lockReason string
	var cloneStart, envEverything bool
	// policyCmd changes an environment's deploy policy; with no fixed
	// input, its second argument is on or off (auto-deploy).
	policyCmd := func(use, short string, in func(*cobra.Command) client.EnvironmentPolicy) *cobra.Command {
		nargs := cobra.ExactArgs(1)
		if in == nil {
			nargs = cobra.ExactArgs(2)
		}
		return &cobra.Command{
			Use: use, Short: short, Args: nargs,
			Annotations: op("setEnvironmentPolicy"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := s.need(); err != nil {
					return err
				}
				var pol client.EnvironmentPolicy
				if in != nil {
					pol = in(cmd)
				} else {
					on := args[1] == "on"
					if !on && args[1] != "off" {
						return errors.New("say on or off")
					}
					pol.AutoDeploy = &on
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				x, err := c.SetEnvironmentPolicy(ctx(cmd), s.project, args[0], pol)
				if err != nil {
					return err
				}
				state := "deploys allowed"
				if x.Lock != nil {
					state = "deploys locked: " + x.Lock.Reason
				}
				fmt.Fprintf(a.out, "%s/%s: %s; builds deploy themselves: %v\n", s.project, x.Name, state, x.AutoDeploy)
				return nil
			},
		}
	}
	// update edits the shared variables of --env and reports the rollout.
	update := func(cmd *cobra.Command, f func(map[string]string) error) error {
		if err := s.need(); err != nil {
			return err
		}
		c, err := a.client()
		if err != nil {
			return err
		}
		vars, err := c.SharedVariables(ctx(cmd), s.project, s.env)
		if err != nil {
			return err
		}
		if vars == nil {
			vars = map[string]string{}
		}
		if err := f(vars); err != nil {
			return err
		}
		redeployed, err := c.SetSharedVariables(ctx(cmd), s.project, s.env, vars)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Updated shared variables of %s/%s", s.project, s.env)
		if len(redeployed) > 0 {
			fmt.Fprintf(a.out, "; redeploying %s", strings.Join(redeployed, ", "))
		}
		fmt.Fprintln(a.out)
		return nil
	}
	e.AddCommand(
		&cobra.Command{
			Use: "vars", Aliases: []string{"variables"}, Short: "Shared variables every service in --env inherits", Args: cobra.NoArgs,
			Annotations: op("getSharedVariables"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := s.need(); err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				vars, err := c.SharedVariables(ctx(cmd), s.project, s.env)
				if err != nil {
					return err
				}
				keys := make([]string, 0, len(vars))
				for k := range vars {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				rows := make([][]string, 0, len(keys))
				for _, k := range keys {
					rows = append(rows, []string{k, vars[k]})
				}
				return a.printer().table(vars, []string{"NAME", "VALUE"}, rows)
			},
		},
		&cobra.Command{
			Use: "set KEY=VALUE...", Short: "Set shared variables (services in --env redeploy)", Args: cobra.MinimumNArgs(1),
			Annotations: op("setSharedVariables"),
			Example:     "  synctl envs set -p shop -e production DATABASE_URL=postgres://db:5432/shop LOG_LEVEL=info",
			RunE: func(cmd *cobra.Command, args []string) error {
				return update(cmd, func(vars map[string]string) error {
					for _, kv := range args {
						k, v, ok := strings.Cut(kv, "=")
						if !ok || k == "" {
							return fmt.Errorf("use KEY=VALUE, got %q", kv)
						}
						vars[k] = v
					}
					return nil
				})
			},
		},
		&cobra.Command{
			Use: "unset KEY...", Short: "Remove shared variables", Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return update(cmd, func(vars map[string]string) error {
					for _, k := range args {
						if _, ok := vars[k]; !ok {
							return fmt.Errorf("no shared variable %s", k)
						}
						delete(vars, k)
					}
					return nil
				})
			},
		},
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
					auto, lock := "on", "-"
					if !x.AutoDeploy {
						auto = "off"
					}
					if x.Lock != nil {
						lock = x.Lock.Reason + " (" + x.Lock.By + ", " + age(x.Lock.At) + ")"
					}
					if x.Deleting {
						lock = "deleting"
					}
					rows = append(rows, []string{x.Name, auto, lock, age(x.CreatedAt)})
				}
				return a.printer().table(es, []string{"NAME", "AUTO-DEPLOY", "LOCKED", "CREATED"}, rows)
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
				if cloneFrom != "" {
					svcs, jobs, err := c.CloneEnvironment(ctx(cmd), s.project, args[0], cloneFrom, cloneStart)
					if err != nil {
						return err
					}
					fmt.Fprintf(a.out, "Created environment %s/%s from %s: %d services (%s), %d jobs\n", s.project, args[0], cloneFrom,
						len(svcs), map[bool]string{true: "started", false: "at 0 tasks"}[cloneStart], len(jobs))
					return nil
				}
				if _, err := c.CreateEnvironment(ctx(cmd), s.project, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Created environment %s/%s\n", s.project, args[0])
				return nil
			},
		},
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete an environment (empty, or --everything)", Args: cobra.ExactArgs(1),
			Annotations: op("deleteEnvironment"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := s.need(); err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				pending, err := c.DeleteEnvironment(ctx(cmd), s.project, args[0], envEverything)
				if err != nil {
					return err
				}
				if pending {
					fmt.Fprintf(a.out, "Deleting environment %s/%s: its services stop first, then it goes\n", s.project, args[0])
				} else {
					fmt.Fprintf(a.out, "Deleted environment %s/%s\n", s.project, args[0])
				}
				return nil
			},
		},
		policyCmd("lock NAME --reason TEXT", "Lock deploys to an environment (rollbacks and cancels still work)", func(cmd *cobra.Command) client.EnvironmentPolicy {
			t := true
			return client.EnvironmentPolicy{Locked: &t, Reason: &lockReason}
		}),
		policyCmd("unlock NAME", "Allow deploys to an environment again", func(*cobra.Command) client.EnvironmentPolicy {
			f := false
			return client.EnvironmentPolicy{Locked: &f}
		}),
		policyCmd("auto-deploy NAME on|off", "Whether builds deploy themselves in an environment", nil),
	)
	for _, c := range e.Commands() {
		switch c.Name() {
		case "create":
			c.Flags().StringVar(&cloneFrom, "from", "", "copy another environment: shared variables, services, jobs and security groups")
			c.Flags().BoolVar(&cloneStart, "start", false, "with --from: start the copied services (default: 0 tasks)")
		case "delete":
			c.Flags().BoolVar(&envEverything, "everything", false, "delete its services too (databases must be deleted first)")
		case "lock":
			c.Flags().StringVar(&lockReason, "reason", "", "why deploys are locked (shown to whoever tries)")
			_ = c.MarkFlagRequired("reason")
		}
	}
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
	var runHooks, skipHooks bool
	rollbackCmd := &cobra.Command{
		Use: "rollback NAME REVISION", Short: "Roll out an earlier revision again (pre-deploy jobs only with --run-hooks)", Args: cobra.ExactArgs(2),
		Annotations: op("rollbackService"),
		RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
			rev, err := strconv.Atoi(args[1])
			if err != nil {
				return errors.New("REVISION must be a number")
			}
			v, err := c.RollbackService(ctx(cmd), s.project, s.env, args[0], rev, runHooks)
			if err != nil {
				return err
			}
			return printSvc(v)
		}),
	}
	rollbackCmd.Flags().BoolVar(&runHooks, "run-hooks", false, "run the pre-deploy jobs with the old revision (e.g. down-migrations)")
	redeployCmd := &cobra.Command{
		Use: "redeploy NAME", Short: "Restart every task with a rolling deployment of the current spec", Args: cobra.ExactArgs(1),
		Annotations: op("redeployService"),
		RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
			v, err := c.RedeployService(ctx(cmd), s.project, s.env, args[0], !skipHooks)
			if err != nil {
				return err
			}
			return printSvc(v)
		}),
	}
	redeployCmd.Flags().BoolVar(&skipHooks, "skip-hooks", false, "don't run the pre-deploy jobs")

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
	var reserveCPU, reserveMem bool
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
			if reserveCPU {
				spec.Resources.CPUMode = "reserved"
			}
			if reserveMem {
				spec.Resources.MemoryMode = "reserved"
			}
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
	run.Flags().Float64Var(&cpu, "cpu", 0, "CPU cores per task, shared unless --reserve-cpu (default 0.1)")
	run.Flags().IntVar(&mem, "memory", 0, "memory per task in MiB, shared unless --reserve-memory (default 128)")
	run.Flags().BoolVar(&reserveCPU, "reserve-cpu", false, "set the CPU aside on the node instead of sharing it")
	run.Flags().BoolVar(&reserveMem, "reserve-memory", false, "set the memory aside on the node instead of sharing it")

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
	printRouting := func(rs []client.PortRoute) error {
		rows := make([][]string, 0, len(rs))
		for _, r := range rs {
			reach := "-"
			switch {
			case r.Protocol == "http" && r.Host != "":
				reach = r.Host
			case r.Protocol == "http":
				reach = "generated address off"
			case r.Public:
				reach = r.Address
				if len(r.Allow) > 0 {
					reach += " (from " + strings.Join(r.Allow, ", ") + ")"
				}
			default:
				reach = "private"
			}
			rows = append(rows, []string{r.Port, fmt.Sprint(r.Container), r.Protocol, reach})
		}
		return a.printer().table(rs, []string{"PORT", "CONTAINER", "PROTOCOL", "REACHED AT"}, rows)
	}
	var exPublic, exPrivate, exNoGenerated, exGenerated bool
	var exLabel string
	var exAllow []string
	expose := &cobra.Command{
		Use: "expose SERVICE PORT", Short: "Set how a port is reached: --public/--private (tcp, udp), --label or --no-generated (http)", Args: cobra.ExactArgs(2),
		Annotations: op("setServiceRouting"),
		RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
			in := client.RoutingInput{}
			f := cmd.Flags()
			if exPublic || exPrivate {
				v := exPublic
				in.Public = &v
			}
			if exNoGenerated || exGenerated {
				v := exGenerated
				in.Generated = &v
			}
			if f.Changed("label") {
				in.Label = &exLabel
			}
			if f.Changed("allow") {
				in.Allow = exAllow
			}
			rs, err := c.SetRouting(ctx(cmd), s.project, s.env, args[0], map[string]client.RoutingInput{args[1]: in})
			if err != nil {
				return err
			}
			return printRouting(rs)
		}),
	}
	expose.Flags().BoolVar(&exPublic, "public", false, "tcp/udp: open a public port on the controller and edge nodes")
	expose.Flags().BoolVar(&exPrivate, "private", false, "tcp/udp: close its public port")
	expose.Flags().StringSliceVar(&exAllow, "allow", nil, "tcp/udp: only these client addresses or CIDRs")
	expose.Flags().StringVar(&exLabel, "label", "", "http: serve LABEL.<base domain> instead of the generated name (empty: the generated name)")
	expose.Flags().BoolVar(&exNoGenerated, "no-generated", false, "http: stop serving the generated address (custom domains only)")
	expose.Flags().BoolVar(&exGenerated, "generated", false, "http: serve the generated address again")
	svc.AddCommand(expose, &cobra.Command{
		Use: "routing SERVICE", Aliases: []string{"ports"}, Short: "How each port of a service is reached", Args: cobra.ExactArgs(1),
		Annotations: op("getServiceRouting"),
		RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
			rs, err := c.GetRouting(ctx(cmd), s.project, s.env, args[0])
			if err != nil {
				return err
			}
			return printRouting(rs)
		}),
	})
	var port string
	var domOpts client.DomainOptions
	add := &cobra.Command{
		Use: "add SERVICE HOST", Short: "Route a custom domain (or a path of it) to a service, or redirect it", Args: cobra.ExactArgs(2),
		Annotations: op("addServiceDomain"),
		RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
			chk, err := c.AddServiceDomain(ctx(cmd), s.project, s.env, args[0], args[1], port, domOpts)
			if err != nil {
				return err
			}
			if domOpts.RedirectTo != "" {
				fmt.Fprintf(a.out, "Redirecting %s to %s.\n", args[1], domOpts.RedirectTo)
			} else {
				fmt.Fprintf(a.out, "Routing %s%s to %s.\n", args[1], domOpts.Path, args[0])
			}
			printCheck(a, chk)
			return nil
		}),
	}
	add.Flags().StringVar(&port, "port", "", "http port name (default: the first)")
	add.Flags().StringVar(&domOpts.Path, "path", "", "route only this path prefix, e.g. /api (several services can share a host)")
	add.Flags().BoolVar(&domOpts.StripPrefix, "strip-prefix", false, "remove the path prefix before forwarding")
	add.Flags().StringVar(&domOpts.RedirectTo, "redirect-to", "", "answer with a permanent redirect to this host instead (e.g. www to apex)")
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
					target := d.Port
					if d.RedirectTo != "" {
						target = "→ " + d.RedirectTo
					} else if d.StripPrefix {
						target += " (prefix stripped)"
					}
					rows = append(rows, []string{d.ID, d.Host + d.Path, target, dns})
				}
				return a.printer().table(ds, []string{"ID", "HOST", "TARGET", "DNS"}, rows)
			}),
		},
		&cobra.Command{
			Use: "remove SERVICE HOST|ID", Aliases: []string{"rm"}, Short: "Stop routing a custom domain (every path of a host, or one by ID)", Args: cobra.ExactArgs(2),
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
				return a.printDeployments(ds, false)
			}),
		},
		&cobra.Command{
			Use: "deployment NAME ID", Short: "One deployment: what changed, its timeline and hook runs", Args: cobra.ExactArgs(2),
			Annotations: op("getDeployment"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				d, err := c.GetDeployment(ctx(cmd), s.project, s.env, args[0], args[1])
				if err != nil {
					return err
				}
				return a.printDeployment(d)
			}),
		},
		&cobra.Command{
			Use: "cancel-deployment NAME ID", Short: "Stop a running deployment (it rolls back) or one waiting on its pre-deploy jobs", Args: cobra.ExactArgs(2),
			Annotations: op("cancelDeployment"),
			RunE: withClient(func(cmd *cobra.Command, c *client.Client, args []string) error {
				v, err := c.CancelDeployment(ctx(cmd), s.project, s.env, args[0], args[1])
				if err != nil {
					return err
				}
				return printSvc(v)
			}),
		},
		redeployCmd,
		rollbackCmd,
	)
	return svc
}

func (a *app) printDeployments(ds []client.Deployment, withService bool) error {
	rows := make([][]string, 0, len(ds))
	for _, d := range ds {
		who := d.ActorName
		if who == "" {
			who = d.Actor
		}
		if d.Commit != nil {
			who = shortSHA(d.Commit.SHA) + " " + d.Commit.Ref
		}
		row := []string{d.ID, fmt.Sprintf("%d → %d", d.FromRevision, d.ToRevision), d.Status, orDash(d.Trigger), orDash(who), age(d.StartedAt), orDash(d.Message)}
		if withService {
			row = append([]string{d.Environment + "/" + d.Service}, row...)
		}
		rows = append(rows, row)
	}
	head := []string{"ID", "REVISIONS", "STATUS", "TRIGGER", "BY", "STARTED", "MESSAGE"}
	if withService {
		head = append([]string{"SERVICE"}, head...)
	}
	return a.printer().table(ds, head, rows)
}

func (a *app) printDeployment(d client.DeploymentDetail) error {
	if a.output == "json" {
		return a.printer().json(d)
	}
	fmt.Fprintf(a.out, "Deployment %s: revision %d → %d, %s (%s)\n", d.ID, d.FromRevision, d.ToRevision, strings.ReplaceAll(d.Status, "_", " "), d.Trigger)
	fmt.Fprintf(a.out, "Image:   %s\n", d.Image)
	if d.Commit != nil {
		fmt.Fprintf(a.out, "Commit:  %s (%s)\n", d.Commit.SHA, d.Commit.Ref)
	}
	if d.Message != "" {
		fmt.Fprintf(a.out, "Message: %s\n", d.Message)
	}
	if len(d.Changes) > 0 {
		fmt.Fprintln(a.out, "\nChanges:")
		for _, ch := range d.Changes {
			fmt.Fprintf(a.out, "  %-24s %s → %s\n", ch.Field, orDash(ch.From), orDash(ch.To))
		}
	}
	fmt.Fprintln(a.out, "\nTimeline:")
	for _, e := range d.Events {
		fmt.Fprintf(a.out, "  %s  %-14s %s\n", e.At.Local().Format("15:04:05"), e.Kind, e.Message)
	}
	if len(d.Runs) > 0 {
		fmt.Fprintln(a.out, "\nHook runs:")
		for _, r := range d.Runs {
			fmt.Fprintf(a.out, "  %s  %-11s %-10s %s %s\n", r.ID, r.Trigger, r.Status, r.Job, r.Message)
		}
	}
	return nil
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
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
