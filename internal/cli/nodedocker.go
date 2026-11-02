package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// nodeDockerCmd is `synctl nodes docker`: what Docker holds on a node, and
// cleaning it.
func (a *app) nodeDockerCmd() *cobra.Command {
	d := &cobra.Command{Use: "docker", Short: "Docker disk usage of a node, and cleaning it"}
	d.AddCommand(
		&cobra.Command{
			Use: "usage NODE", Aliases: []string{"du"}, Short: "What Docker holds on a node, by app, with what each clean would free", Args: cobra.ExactArgs(1),
			Annotations: op("getNodeDocker"),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := a.nodeID(cmd, args[0])
				if err != nil {
					return err
				}
				c, err := a.client()
				if err != nil {
					return err
				}
				u, err := c.NodeDocker(ctx(cmd), id)
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(u.Apps))
				for _, x := range u.Apps {
					rows = append(rows, []string{x.Name, x.Kind, fmt.Sprintf("%d/%d", x.Running, x.Containers),
						bytesIEC(uint64(x.ImageBytes)), bytesIEC(uint64(x.VolumeBytes)), bytesIEC(uint64(x.ContainerBytes)), bytesIEC(uint64(x.Bytes))})
				}
				if err := a.printer().table(u, []string{"APP", "KIND", "RUNNING", "IMAGES", "VOLUMES", "CONTAINERS", "TOTAL"}, rows); err != nil {
					return err
				}
				if a.output == "json" {
					return nil
				}
				t, r := u.Totals, u.Reclaimable
				fmt.Fprintf(a.out, "\nDisk %s: %s used of %s. Docker holds %s: images %s, volumes %s, containers %s, logs %s, build cache %s; %s is not Docker's.\n",
					u.DiskPath, bytesIEC(uint64(t.DiskUsedBytes)), bytesIEC(uint64(t.DiskTotalBytes)), bytesIEC(uint64(t.DockerBytes)),
					bytesIEC(uint64(t.ImageBytes)), bytesIEC(uint64(t.VolumeBytes)), bytesIEC(uint64(t.ContainerBytes)), bytesIEC(uint64(t.LogBytes)),
					bytesIEC(uint64(t.BuildCacheBytes)), bytesIEC(uint64(t.OtherBytes)))
				fmt.Fprintf(a.out, "A clean would free: untagged images %s, every unused image %s, stopped containers %s, anonymous volumes %s, build cache %s.\n",
					bytesIEC(uint64(r.DanglingImages)), bytesIEC(uint64(r.UnusedImages)), bytesIEC(uint64(r.Containers)),
					bytesIEC(uint64(r.AnonymousVolumes)), bytesIEC(uint64(r.BuildCache)))
				return nil
			},
		},
	)
	var all bool
	prune := &cobra.Command{
		Use: "prune NODE KIND [ID...]", Aliases: []string{"clean"}, Args: cobra.MinimumNArgs(2),
		Short: "Remove unused Docker objects (KIND: images, containers, volumes, buildcache); with IDs, remove exactly those",
		Long: "Without IDs the whole kind is pruned: untagged images (--all: every unused image), stopped containers outside SynCloud,\n" +
			"anonymous unused volumes, and build cache. SynCloud's own containers and named volumes are only removed by naming them.\n" +
			"Docker refuses anything in use.",
		Annotations: op("cleanNodeDocker"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.nodeID(cmd, args[0])
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.CleanNodeDocker(ctx(cmd), id, strings.ToLower(args[1]), args[2:], all)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(res)
			}
			fmt.Fprintf(a.out, "Removed %d, reclaimed %s\n", res.Removed, bytesIEC(uint64(res.ReclaimedBytes)))
			for _, e := range res.Errors {
				fmt.Fprintf(a.errOut, "not removed: %s\n", e)
			}
			if len(res.Errors) > 0 {
				return fmt.Errorf("%d objects could not be removed", len(res.Errors))
			}
			return nil
		},
	}
	prune.Flags().BoolVar(&all, "all", false, "images: every unused image, not only untagged ones; buildcache: everything not in use")
	d.AddCommand(prune)
	return d
}
