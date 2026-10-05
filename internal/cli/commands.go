package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"syncloud/internal/client"
)

func (a *app) configureCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "configure",
		Short: "Save an endpoint and credentials to a profile in ~/.syncloud/credentials",
		Long: `Prompts for the controller URL and an access key (create one in the dashboard
under IAM → Access keys, or with "synctl iam access-keys create"). Leave a
prompt empty to keep the current value.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name := a.profile
			if name == "" {
				name = "default"
			}
			f, err := loadCredentials()
			if err != nil {
				return err
			}
			p := f.Profiles[name]
			r := bufio.NewReader(a.in)
			if p.Endpoint, err = a.prompt(r, "Endpoint (e.g. https://203-0-113-10.sslip.io)", p.Endpoint, false); err != nil {
				return err
			}
			if p.AccessKeyID, err = a.prompt(r, "Access key ID", p.AccessKeyID, false); err != nil {
				return err
			}
			if p.SecretAccessKey, err = a.prompt(r, "Secret access key", p.SecretAccessKey, true); err != nil {
				return err
			}
			if _, err := client.New(p.Endpoint, client.Credentials{}); err != nil {
				return err
			}
			f.Profiles[name] = p
			path, err := saveCredentials(f)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Saved profile %q to %s\n", name, path)
			return nil
		},
	}
}

// prompt reads one line. Secrets are read without echo when stdin is a terminal.
func (a *app) prompt(r *bufio.Reader, label, current string, secret bool) (string, error) {
	shown := current
	if secret && current != "" {
		shown = "****" + current[max(0, len(current)-4):]
	}
	if shown != "" {
		fmt.Fprintf(a.errOut, "%s [%s]: ", label, shown)
	} else {
		fmt.Fprintf(a.errOut, "%s: ", label)
	}
	var line string
	if f, ok := a.in.(*os.File); ok && secret && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.errOut)
		if err != nil {
			return "", err
		}
		line = string(b)
	} else {
		s, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		line = s
	}
	if line = strings.TrimSpace(line); line == "" {
		return current, nil
	}
	return line, nil
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "status",
		Short:       "Show controller version and setup state",
		Args:        cobra.NoArgs,
		Annotations: op("getSystemStatus"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			s, err := c.SystemStatus(ctx(cmd))
			if err != nil {
				return err
			}
			domain := s.BaseDomain
			return a.printer().table(s, []string{"VERSION", "SETUP REQUIRED", "BASE DOMAIN"},
				[][]string{{s.Version, fmt.Sprint(s.SetupRequired), orDash(domain)}})
		},
	}
}

func (a *app) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "whoami",
		Short:       "Show the authenticated user",
		Args:        cobra.NoArgs,
		Annotations: op("whoami"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			u, err := c.Whoami(ctx(cmd))
			if err != nil {
				return err
			}
			return a.printer().table(u, []string{"ID", "EMAIL", "NAME", "ROOT"},
				[][]string{{u.ID, u.Email, u.Name, fmt.Sprint(u.IsRoot)}})
		},
	}
}

func (a *app) apiCmd() *cobra.Command {
	var data string
	cmd := &cobra.Command{
		Use:   "api METHOD PATH",
		Short: "Send a raw, signed API request (escape hatch for any endpoint)",
		Example: `  synctl api GET /api/v1/auth/me
  synctl api POST /api/v1/iam/tokens -d '{"name":"ci","expiresInDays":30}'
  synctl api POST /api/v1/iam/tokens -d @body.json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var body []byte
			if strings.HasPrefix(data, "@") {
				if body, err = os.ReadFile(data[1:]); err != nil {
					return err
				}
			} else if data != "" {
				body = []byte(data)
			}
			if len(body) > 0 && !json.Valid(body) {
				return fmt.Errorf("--data is not valid JSON")
			}
			path := args[1]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			status, out, err := c.Raw(ctx(cmd), strings.ToUpper(args[0]), path, body)
			if err != nil {
				return err
			}
			if len(out) > 0 {
				var pretty any
				if json.Unmarshal(out, &pretty) == nil {
					_ = a.printer().json(pretty)
				} else {
					a.out.Write(out)
				}
			}
			if status >= 400 {
				return fmt.Errorf("HTTP %d", status)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "JSON request body, or @file")
	return cmd
}
