package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func (a *app) upstreamsCmd() *cobra.Command {
	u := &cobra.Command{Use: "upstreams", Aliases: []string{"upstream"}, Short: "Credentials for pulling from Docker Hub, GHCR and other registries (§5.9)"}
	var username string
	set := &cobra.Command{
		Use: "set HOST -u USERNAME", Short: "Store the credential for a registry host (password from stdin or $SYNCLOUD_UPSTREAM_PASSWORD)",
		Args: cobra.ExactArgs(1), Annotations: op("putUpstreamCredential"),
		Example: "  echo \"$DOCKERHUB_TOKEN\" | synctl registry upstreams set docker.io -u acme\n" +
			"  SYNCLOUD_UPSTREAM_PASSWORD=ghp_… synctl registry upstreams set ghcr.io -u acme-bot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if username == "" {
				return errors.New("--username is required")
			}
			pw := os.Getenv("SYNCLOUD_UPSTREAM_PASSWORD")
			if pw == "" {
				line, err := bufio.NewReader(a.in).ReadString('\n')
				if err != nil && line == "" {
					return errors.New("give the password on stdin or in $SYNCLOUD_UPSTREAM_PASSWORD")
				}
				pw = strings.TrimRight(line, "\r\n")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			x, err := c.PutUpstreamCredential(ctx(cmd), args[0], username, pw)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Nodes and builds now pull from %s as %s\n", x.Host, x.Username)
			return nil
		},
	}
	set.Flags().StringVarP(&username, "username", "u", "", "registry user name")
	u.AddCommand(set,
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List stored credentials (passwords are never shown)", Args: cobra.NoArgs,
			Annotations: op("listUpstreamCredentials"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				cs, err := c.ListUpstreamCredentials(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(cs))
				for _, x := range cs {
					rows = append(rows, []string{x.Host, x.Username, age(x.UpdatedAt)})
				}
				return a.printer().table(cs, []string{"HOST", "USERNAME", "UPDATED"}, rows)
			},
		},
		&cobra.Command{
			Use: "delete HOST", Aliases: []string{"rm"}, Short: "Delete the credential for a registry host", Args: cobra.ExactArgs(1),
			Annotations: op("deleteUpstreamCredential"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				cs, err := c.ListUpstreamCredentials(ctx(cmd))
				if err != nil {
					return err
				}
				var found *client.UpstreamCredential
				for i := range cs {
					if strings.EqualFold(cs[i].Host, args[0]) || cs[i].ID == args[0] {
						found = &cs[i]
					}
				}
				if found == nil {
					return fmt.Errorf("no credential for %s", args[0])
				}
				if err := c.DeleteUpstreamCredential(ctx(cmd), found.ID); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted the credential for %s\n", found.Host)
				return nil
			},
		},
	)
	return u
}
