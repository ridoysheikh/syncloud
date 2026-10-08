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
	cmd.AddCommand(custom, a.traefikSettingsCmd())
	return cmd
}

func (a *app) traefikSettingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "settings", Short: "Global Traefik settings for every replica (timeouts, trusted proxies, TLS, defaults)", Args: cobra.NoArgs,
		Annotations: op("getTraefikSettings"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			v, err := c.TraefikSettings(ctx(cmd))
			if err != nil {
				return err
			}
			return a.printSettings(v)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use: "set KEY=VALUE...", Short: "Change settings; static ones (timeouts, proxies, log level, HTTP/3) restart Traefik", Args: cobra.MinimumNArgs(1),
		Annotations: op("updateTraefikSettings"),
		Example: `  synctl traefik settings set minTls=1.3 hstsSeconds=31536000 compress=true
  synctl traefik settings set trustedIPs=173.245.48.0/20,103.21.244.0/22 readTimeout=120s
  synctl traefik settings set redirectHttps=false`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			v, err := c.TraefikSettings(ctx(cmd))
			if err != nil {
				return err
			}
			set, _ := v["settings"].(map[string]any)
			if set == nil {
				return errors.New("unexpected response")
			}
			for _, kv := range args {
				k, val, ok := strings.Cut(kv, "=")
				cur, known := set[k]
				if !ok || !known {
					return fmt.Errorf("%q: use KEY=VALUE with one of: %s", kv, strings.Join(sortedMapKeys(set), ", "))
				}
				switch cur.(type) {
				case bool:
					b, err := strconv.ParseBool(val)
					if err != nil {
						return fmt.Errorf("%s must be true or false", k)
					}
					set[k] = b
				case float64:
					n, err := strconv.Atoi(val)
					if err != nil {
						return fmt.Errorf("%s must be a whole number", k)
					}
					set[k] = n
				case []any:
					list := []string{}
					for _, x := range strings.Split(val, ",") {
						if x = strings.TrimSpace(x); x != "" {
							list = append(list, x)
						}
					}
					set[k] = list
				default:
					set[k] = val
				}
			}
			out, err := c.UpdateTraefikSettings(ctx(cmd), set)
			if err != nil {
				return err
			}
			if r, _ := out["restarted"].(bool); r {
				fmt.Fprintln(a.out, "Saved. Static settings changed: every Traefik replica restarts now.")
			} else {
				fmt.Fprintln(a.out, "Saved. Traefik applies it within 2 seconds.")
			}
			return a.printSettings(out)
		},
	})
	return cmd
}

func (a *app) printSettings(v map[string]any) error {
	set, _ := v["settings"].(map[string]any)
	keys := sortedMapKeys(set)
	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		val := set[k]
		if l, ok := val.([]any); ok {
			parts := make([]string, 0, len(l))
			for _, x := range l {
				parts = append(parts, fmt.Sprint(x))
			}
			val = joinOrDash(parts)
		}
		rows = append(rows, []string{k, fmt.Sprint(val)})
	}
	return a.printer().table(v, []string{"SETTING", "VALUE"}, rows)
}

func sortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
