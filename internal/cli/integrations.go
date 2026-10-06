package cli

import (
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func (a *app) integrationsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "integrations", Aliases: []string{"integration"}, Short: "Connections to outside services: Git providers (§5.8)"}
	git := &cobra.Command{Use: "git", Short: "Git provider connections: GitHub App, GitHub, GitLab, Gitea/Forgejo",
		Long: "A connection lets services pick a repository by name (synctl builds connect SERVICE --connection NAME --repo OWNER/NAME).\n" +
			"SynCloud then mints clone tokens, creates the push webhook on the repository and reports build statuses on commits."}
	git.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List Git connections", Args: cobra.NoArgs,
		Annotations: op("listGitConnections"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			cs, err := c.ListGitConnections(ctx(cmd))
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(cs))
			for _, x := range cs {
				rows = append(rows, []string{x.Name, x.Kind, x.WebURL, x.Account, fmt.Sprint(len(x.Services))})
			}
			return a.printer().table(cs, []string{"NAME", "KIND", "SERVER", "ACCOUNT", "SERVICES"}, rows)
		},
	})
	var kind, server string
	add := &cobra.Command{
		Use: "add NAME --kind github|gitlab|gitea", Short: "Connect a provider with an access token (read from $SYNCLOUD_GIT_TOKEN or stdin)", Args: cobra.ExactArgs(1),
		Annotations: op("createGitConnection"),
		Long: "Token scopes: GitHub fine-grained token with Contents (read), Metadata (read), Commit statuses (write) and Webhooks (write);\n" +
			"GitLab token with the api scope; Gitea/Forgejo token with repository (read and write) and user (read).",
		Example: `  echo glpat-… | synctl integrations git add gitlab --kind gitlab
  SYNCLOUD_GIT_TOKEN=… synctl integrations git add forge --kind gitea --url https://git.example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			token := os.Getenv("SYNCLOUD_GIT_TOKEN")
			if token == "" {
				b, err := a.readInput("-")
				if err != nil {
					return err
				}
				token = strings.TrimSpace(string(b))
			}
			if token == "" {
				return errors.New("no token: set $SYNCLOUD_GIT_TOKEN or pipe it on stdin")
			}
			x, err := c.CreateGitConnection(ctx(cmd), kind, args[0], server, token)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Connected %s (%s) as %s.\n", x.Name, x.WebURL, x.Account)
			return nil
		},
	}
	add.Flags().StringVar(&kind, "kind", "", "github, gitlab or gitea")
	add.Flags().StringVar(&server, "url", "", "server URL (default github.com / gitlab.com; required for Gitea)")
	_ = add.MarkFlagRequired("kind")

	var org, ghURL, outFile string
	app := &cobra.Command{
		Use: "github-app [NAME]", Short: "Create a GitHub App for this cluster (finished in a browser signed in to the dashboard)", Args: cobra.MaximumNArgs(1),
		Annotations: op("createGitHubAppManifest"),
		Long: "Writes a small HTML page that sends the app's manifest to GitHub. Open it in a browser where you are signed in to\n" +
			"the SynCloud dashboard: GitHub asks you to confirm, SynCloud saves the app, and you pick the repositories to install it on.\n" +
			"The dashboard's Integrations page does the same in one click.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			name := "github"
			if len(args) == 1 {
				name = args[0]
			}
			m, err := c.CreateGitHubAppManifest(ctx(cmd), name, org, ghURL)
			if err != nil {
				return err
			}
			page := `<!doctype html><meta charset="utf-8"><title>Create GitHub App</title>` +
				`<form id="f" method="post" action="` + html.EscapeString(m.PostURL) + `"><input type="hidden" name="manifest" value="` + html.EscapeString(m.Manifest) + `">` +
				`<button>Continue to GitHub</button></form><script>document.getElementById("f").submit()</script>`
			if outFile == "" {
				outFile = filepath.Join(os.TempDir(), "syncloud-github-app-"+m.State[:8]+".html")
			}
			if err := os.WriteFile(outFile, []byte(page), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Open file://%s in a browser signed in to the dashboard within 10 minutes.\n", outFile)
			return nil
		},
	}
	app.Flags().StringVar(&org, "org", "", "create the app under this GitHub organization")
	app.Flags().StringVar(&ghURL, "github-url", "", "GitHub Enterprise server (default https://github.com)")
	app.Flags().StringVarP(&outFile, "output-file", "f", "", "where to write the HTML page")

	git.AddCommand(add, app, &cobra.Command{
		Use: "get NAME", Short: "Show a connection, its services and (GitHub App) installations", Args: cobra.ExactArgs(1),
		Annotations: op("getGitConnection"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			x, err := c.GetGitConnection(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(x)
			}
			fmt.Fprintf(a.out, "%s: %s at %s as %s\n", x.Name, x.Kind, x.WebURL, x.Account)
			if x.Problem != "" {
				fmt.Fprintf(a.out, "  problem:       %s\n", x.Problem)
			}
			if x.Kind == "github-app" {
				fmt.Fprintf(a.out, "  installed on:  %s\n  install more:  %s\n", joinOrDash(x.Installations), x.InstallURL)
			}
			fmt.Fprintf(a.out, "  services:      %s\n", joinOrDash(x.Services))
			return nil
		},
	}, &cobra.Command{
		Use: "remove NAME", Aliases: []string{"rm"}, Short: "Remove a connection no service builds from", Args: cobra.ExactArgs(1),
		Annotations: op("deleteGitConnection"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.DeleteGitConnection(ctx(cmd), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Removed connection %s\n", args[0])
			return nil
		},
	}, &cobra.Command{
		Use: "repos NAME [FILTER]", Short: "Repositories the connection can read", Args: cobra.RangeArgs(1, 2),
		Annotations: op("listGitRepos"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q := ""
			if len(args) == 2 {
				q = args[1]
			}
			rs, err := c.ListGitRepos(ctx(cmd), args[0], q)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(rs))
			for _, r := range rs {
				vis := "public"
				if r.Private {
					vis = "private"
				}
				rows = append(rows, []string{r.FullName, r.DefaultBranch, vis})
			}
			return a.printer().table(rs, []string{"REPOSITORY", "DEFAULT BRANCH", "VISIBILITY"}, rows)
		},
	}, &cobra.Command{
		Use: "branches NAME OWNER/REPO", Short: "Branches of a repository", Args: cobra.ExactArgs(2),
		Annotations: op("listGitBranches"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			bs, err := c.ListGitBranches(ctx(cmd), args[0], args[1])
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(bs))
			for _, b := range bs {
				rows = append(rows, []string{b})
			}
			return a.printer().table(bs, []string{"BRANCH"}, rows)
		},
	})
	cmd.AddCommand(git, a.gitServerCmd())
	return cmd
}

func (a *app) gitServerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "git-server", Short: "The built-in Git server (Forgejo) at git.<base-domain>", Args: cobra.NoArgs,
		Annotations: op("getGitServer"),
		Long: "When on, SynCloud runs Forgejo on the controller node, creates its administrator and connects it as \"git\":\n" +
			"push code there and build services from it like from GitHub. Turning it off keeps its repositories for later.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			g, err := c.GetGitServer(ctx(cmd))
			if err != nil {
				return err
			}
			return a.printGitServer(g)
		},
	}
	toggle := func(on bool) *cobra.Command {
		use, short := "enable", "Run the built-in Git server and connect it as \"git\""
		if !on {
			use, short = "disable", "Stop the built-in Git server (repositories are kept)"
		}
		return &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, Annotations: op("setGitServer"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				g, err := c.SetGitServer(ctx(cmd), on)
				if err != nil {
					return err
				}
				return a.printGitServer(g)
			}}
	}
	cmd.AddCommand(toggle(true), toggle(false), &cobra.Command{
		Use: "credentials", Short: "Print the Git server administrator's sign-in", Args: cobra.NoArgs,
		Annotations: op("getGitServerCredentials"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			cr, err := c.GitServerCredentials(ctx(cmd))
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "url:      %s\nusername: %s\npassword: %s\n", cr["url"], cr["username"], cr["password"])
			return nil
		},
	})
	return cmd
}

func (a *app) printGitServer(g client.GitServer) error {
	if a.output == "json" {
		return a.printer().json(g)
	}
	if !g.Enabled {
		fmt.Fprintln(a.out, "The built-in Git server is off (synctl integrations git-server enable).")
		return nil
	}
	fmt.Fprintf(a.out, "%s at %s (%s)\n  connection: %s, admin: %s\n", g.State, g.URL, g.Image, g.Connection, g.AdminUser)
	if g.Problem != "" {
		fmt.Fprintf(a.out, "  %s\n", g.Problem)
	}
	return nil
}
