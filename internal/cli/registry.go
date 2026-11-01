package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// splitImageRef splits REPOSITORY:TAG.
func splitImageRef(ref string) (repo, tag string, err error) {
	i := strings.LastIndexByte(ref, ':')
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("use REPOSITORY:TAG")
	}
	return ref[:i], ref[i+1:], nil
}

func (a *app) registryCmd() *cobra.Command {
	r := &cobra.Command{Use: "registry", Aliases: []string{"reg"}, Short: "Private image registry (§5.9)"}
	var refresh bool
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
				h := info.Host
				if info.SelfSigned {
					fmt.Fprintf(a.out, "The registry's certificate is self-signed (a private network). Trust it once on each machine that pushes:\n\n%s\n\n", info.Trust)
				}
				fmt.Fprintf(a.out, "Registry: %s\n\n  %s\n  docker tag my-app %s/<project>/<name>:<tag>\n  docker push %s/<project>/<name>:<tag>\n\nUse it in a service as \"@registry/<project>/<name>:<tag>\".\n", h, info.Login, h, h)
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
					rows = append(rows, []string{x.Name, fmt.Sprint(x.Tags), lc, ago(x.LastPushedAt), ago(x.LastPulledAt), fmt.Sprint(x.Pulls)})
				}
				return a.printer().table(rs, []string{"REPOSITORY", "TAGS", "LIFECYCLE", "PUSHED", "PULLED", "PULLS"}, rows)
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
					var users []string
					for _, u := range x.InUseBy {
						users = append(users, u.Project+"/"+u.Environment+"/"+u.Service)
					}
					inUse := "-"
					if len(users) > 0 {
						inUse = strings.Join(users, ",")
					}
					rows = append(rows, []string{x.Tag, d, bytesIEC(uint64(x.SizeBytes)), strings.Join(x.Platforms, ","), created, ago(x.LastPulledAt), fmt.Sprint(x.Pulls), inUse})
				}
				return a.printer().table(is, []string{"TAG", "DIGEST", "SIZE", "PLATFORMS", "CREATED", "PULLED", "PULLS", "IN USE BY"}, rows)
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
				force, _ := cmd.Flags().GetBool("force")
				if err := c.DeleteImage(ctx(cmd), args[0][:i], args[0][i+1:], force); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted %s\n", args[0])
				return nil
			},
		},
	)
	r.AddCommand(
		&cobra.Command{
			Use: "usage", Aliases: []string{"du"}, Short: "Where the registry's storage goes", Args: cobra.NoArgs,
			Annotations: op("getRegistryUsage"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				u, err := c.RegistryUsage(ctx(cmd), refresh)
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(u.Repositories))
				for _, x := range u.Repositories {
					rows = append(rows, []string{x.Name, fmt.Sprint(x.Images), bytesIEC(uint64(x.Bytes)), bytesIEC(uint64(x.UniqueBytes)), bytesIEC(uint64(x.SharedBytes)), bytesIEC(uint64(x.CacheBytes))})
				}
				if err := a.printer().table(u, []string{"REPOSITORY", "IMAGES", "SIZE", "UNIQUE", "SHARED", "BUILD CACHE"}, rows); err != nil {
					return err
				}
				if a.output != "json" {
					fmt.Fprintf(a.out, "\nTagged layers: %s (build cache %s)", bytesIEC(uint64(u.ReferencedBytes)), bytesIEC(uint64(u.CacheBytes)))
					if u.StorageBytes != nil {
						fmt.Fprintf(a.out, "; storage %s, %s of it reachable from no tag and freed by the next cleanup", bytesIEC(uint64(*u.StorageBytes)), bytesIEC(uint64(*u.UnreferencedBytes)))
					}
					fmt.Fprintln(a.out)
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "inspect REPOSITORY:TAG", Short: "Layers, platforms and settings of an image", Args: cobra.ExactArgs(1),
			Annotations: op("getImageDetails"),
			RunE: func(cmd *cobra.Command, args []string) error {
				repo, tag, err := splitImageRef(args[0])
				if err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				d, err := c.ImageDetails(ctx(cmd), repo, tag)
				if err != nil {
					return err
				}
				if a.output == "json" {
					return a.printer().json(d)
				}
				fmt.Fprintf(a.out, "%s:%s  %s\n%s, %s (%s across every platform)\n", repo, tag, d.Digest, d.Platform, bytesIEC(uint64(d.SizeBytes)), bytesIEC(uint64(d.TotalBytes)))
				if len(d.Entrypoint)+len(d.Cmd) > 0 {
					fmt.Fprintf(a.out, "entrypoint %v  cmd %v\n", d.Entrypoint, d.Cmd)
				}
				rows := make([][]string, 0, len(d.Layers))
				for i, l := range d.Layers {
					cmdline := l.Command
					if len(cmdline) > 80 {
						cmdline = cmdline[:77] + "..."
					}
					rows = append(rows, []string{fmt.Sprint(i + 1), bytesIEC(uint64(l.Size)), cmdline})
				}
				return a.printer().table(nil, []string{"LAYER", "SIZE", "STEP"}, rows)
			},
		},
		&cobra.Command{
			Use: "tag REPOSITORY:TAG NEWTAG", Short: "Give an image another tag (nothing is copied)", Args: cobra.ExactArgs(2),
			Annotations: op("tagImage"),
			RunE: func(cmd *cobra.Command, args []string) error {
				repo, tag, err := splitImageRef(args[0])
				if err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.TagImage(ctx(cmd), repo, tag, args[1]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s:%s now points at %s:%s\n", repo, args[1], repo, tag)
				return nil
			},
		},
	)
	for _, c := range r.Commands() {
		if c.Name() == "delete" {
			c.Flags().Bool("force", false, "delete although a service runs the image")
		}
		if c.Name() == "usage" {
			c.Flags().BoolVar(&refresh, "refresh", false, "measure again instead of using the result kept for a minute")
		}
	}
	var limit int
	evCmd := &cobra.Command{
		Use: "events [REPOSITORY]", Aliases: []string{"activity"}, Short: "Recent pushes, pulls and deletes", Args: cobra.MaximumNArgs(1),
		Annotations: op("listRegistryEvents"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			repo := ""
			if len(args) == 1 {
				repo = args[0]
			}
			evs, err := c.ListRegistryEvents(ctx(cmd), repo, limit)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(evs))
			for _, e := range evs {
				ref := e.Repository
				if e.Tag != "" {
					ref += ":" + e.Tag
				} else if e.Digest != "" {
					ref += "@" + e.Digest[:min(len(e.Digest), 19)]
				}
				rows = append(rows, []string{age(e.At), strings.ToUpper(e.Action), ref, e.Actor, e.Addr})
			}
			return a.printer().table(evs, []string{"WHEN", "ACTION", "IMAGE", "BY", "FROM"}, rows)
		},
	}
	evCmd.Flags().IntVar(&limit, "limit", 50, "how many events to show (max 1000)")
	r.AddCommand(evCmd, a.lifecycleCmd(), a.gcCmd(), a.storageCmd(), a.upstreamsCmd())
	return r
}
