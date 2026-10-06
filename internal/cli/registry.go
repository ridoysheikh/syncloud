package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) registryCmd() *cobra.Command {
	r := &cobra.Command{Use: "registry", Aliases: []string{"reg"}, Short: "Private image registry (§5.9)"}
	r.AddCommand(
		&cobra.Command{
			Use: "info", Short: "Registry host and how to push", Args: cobra.NoArgs,
			Annotations: op("getRegistryInfo"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				info, err := c.RegistryInfo(ctx(cmd))
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.printer().json(info)
				}
				h := info["host"]
				fmt.Fprintf(a.out, "Registry: %s\n\n  %s\n  docker tag my-app %s/<project>/<name>:<tag>\n  docker push %s/<project>/<name>:<tag>\n\nUse it in a service as \"@registry/<project>/<name>:<tag>\".\n", h, info["login"], h, h)
				return nil
			},
		},
		&cobra.Command{
			Use: "repos", Aliases: []string{"repositories", "ls"}, Short: "List repositories", Args: cobra.NoArgs,
			Annotations: op("listRepositories"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				rs, err := c.ListRepositories(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(rs))
				for _, x := range rs {
					lc := "-"
					if x.Lifecycle {
						lc = "active"
					}
					rows = append(rows, []string{x.Name, fmt.Sprint(x.Tags), lc})
				}
				return a.printer().table(rs, []string{"REPOSITORY", "TAGS", "LIFECYCLE"}, rows)
			},
		},
		&cobra.Command{
			Use: "images REPOSITORY", Aliases: []string{"tags"}, Short: "List a repository's images", Args: cobra.ExactArgs(1),
			Annotations: op("listImages"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				is, err := c.ListImages(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(is))
				for _, x := range is {
					created := "-"
					if x.Created != nil {
						created = age(*x.Created)
					}
					d := x.Digest
					if len(d) > 19 {
						d = d[:19]
					}
					rows = append(rows, []string{x.Tag, d, bytesIEC(uint64(x.SizeBytes)), strings.Join(x.Platforms, ","), created})
				}
				return a.printer().table(is, []string{"TAG", "DIGEST", "SIZE", "PLATFORMS", "CREATED"}, rows)
			},
		},
		&cobra.Command{
			Use: "delete REPOSITORY:TAG", Aliases: []string{"rm"}, Short: "Delete an image tag", Args: cobra.ExactArgs(1),
			Annotations: op("deleteImage"),
			RunE: func(cmd *cobra.Command, args []string) error {
				i := strings.LastIndexByte(args[0], ':')
				if i <= 0 {
					return fmt.Errorf("use REPOSITORY:TAG")
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteImage(ctx(cmd), args[0][:i], args[0][i+1:]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted %s\n", args[0])
				return nil
			},
		},
	)
	r.AddCommand(a.lifecycleCmd(), a.gcCmd(), a.upstreamsCmd())
	return r
}
