package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func (a *app) readInput(file string) ([]byte, error) {
	if file == "-" {
		return io.ReadAll(a.in)
	}
	return os.ReadFile(file)
}

func (a *app) middlewaresCmd() *cobra.Command {
	var s scope
	cmd := &cobra.Command{Use: "middlewares", Aliases: []string{"middleware", "mw"}, Short: "Traefik middleware presets attached to services (§5.7)",
		Long: "Types: ip-allowlist, rate-limit, basic-auth, redirect-www, cors, security-headers, circuit-breaker, compress, retry.\n" +
			"Every HTTP route of an attached service applies them in that order, then retries (2 attempts unless a retry preset is attached)."}
	a.scopeFlags(cmd, &s)
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List middlewares (all projects without --project)", Args: cobra.NoArgs,
		Annotations: op("listAllMiddlewares", "listMiddlewares"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var ms []client.Middleware
			if s.project != "" {
				ms, err = c.ListMiddlewares(ctx(cmd), s.project)
			} else {
				ms, err = c.ListAllMiddlewares(ctx(cmd))
			}
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(ms))
			for _, m := range ms {
				rows = append(rows, []string{m.Project, m.Name, m.Type, string(m.Config), joinOrDash(m.Services)})
			}
			return a.printer().table(ms, []string{"PROJECT", "NAME", "TYPE", "CONFIG", "SERVICES"}, rows)
		},
	})
	var file string
	apply := &cobra.Command{
		Use: "apply -f FILE", Short: "Create or update a middleware from JSON (matched by name)", Args: cobra.NoArgs,
		Annotations: op("createMiddleware", "updateMiddleware"),
		Example: `  echo '{"name":"limit","type":"rate-limit","config":{"average":20},"services":["production/web"]}' | synctl mw apply -p shop -f -
  echo '{"name":"staff","type":"basic-auth","config":{"users":[{"username":"ann","password":"…"}]},"services":["production/admin"]}' | synctl mw apply -p shop -f -`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := s.need(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			b, err := a.readInput(file)
			if err != nil {
				return err
			}
			var m client.Middleware
			if err := json.Unmarshal(b, &m); err != nil {
				return fmt.Errorf("parse middleware: %w", err)
			}
			ms, err := c.ListMiddlewares(ctx(cmd), s.project)
			if err != nil {
				return err
			}
			verb := "Created"
			for _, x := range ms {
				if x.Name == m.Name {
					verb = "Updated"
				}
			}
			if verb == "Updated" {
				_, err = c.UpdateMiddleware(ctx(cmd), s.project, m.Name, m)
			} else {
				_, err = c.CreateMiddleware(ctx(cmd), s.project, m)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s middleware %s/%s (%s). Traefik applies it within seconds.\n", verb, s.project, m.Name, m.Type)
			return nil
		},
	}
	apply.Flags().StringVarP(&file, "file", "f", "", "middleware JSON file, or - for stdin")
	_ = apply.MarkFlagRequired("file")
	cmd.AddCommand(apply, &cobra.Command{
		Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a middleware", Args: cobra.ExactArgs(1),
		Annotations: op("deleteMiddleware"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := s.need(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.DeleteMiddleware(ctx(cmd), s.project, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Deleted middleware %s/%s\n", s.project, args[0])
			return nil
		},
	})
	return cmd
}

func (a *app) traefikCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "traefik", Short: "Traefik routing configuration (§5.7)"}
	cmd.AddCommand(&cobra.Command{
		Use: "config", Short: "The dynamic configuration Traefik is served (certificates redacted), as JSON", Args: cobra.NoArgs,
		Annotations: op("getTraefikConfig"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			cfg, err := c.TraefikConfig(ctx(cmd))
			if err != nil {
				return err
			}
			return a.printer().json(cfg)
		},
	})
	custom := &cobra.Command{Use: "custom", Short: "Custom (advanced) configuration merged into the generated one"}
	custom.AddCommand(&cobra.Command{
		Use: "get", Short: "Print the custom configuration (YAML)", Args: cobra.NoArgs,
		Annotations: op("getTraefikCustom"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			y, err := c.TraefikCustom(ctx(cmd))
			if err != nil {
				return err
			}
			fmt.Fprint(a.out, y)
			if y != "" && !strings.HasSuffix(y, "\n") {
				fmt.Fprintln(a.out)
			}
			return nil
		},
	})
	var file string
	var dry bool
	apply := &cobra.Command{
		Use: "apply -f FILE", Short: "Validate and apply custom configuration (an empty file removes it)", Args: cobra.NoArgs,
		Annotations: op("putTraefikCustom", "validateTraefikCustom"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			b, err := a.readInput(file)
			if err != nil {
				return err
			}
			if dry {
				if err := c.ValidateTraefikCustom(ctx(cmd), string(b)); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Valid.")
				return nil
			}
			if err := c.PutTraefikCustom(ctx(cmd), string(b)); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Applied. Traefik picks it up within 2 seconds.")
			return nil
		},
	}
	apply.Flags().StringVarP(&file, "file", "f", "", "YAML file, or - for stdin")
	apply.Flags().BoolVar(&dry, "dry-run", false, "only validate")
	_ = apply.MarkFlagRequired("file")
	custom.AddCommand(apply)
	cmd.AddCommand(custom)
	return cmd
}
