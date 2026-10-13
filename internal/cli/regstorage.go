package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ridoysheikh/syncloud/internal/client"
)

func storageWhere(t client.RegistryStorageTarget, endpointName string) string {
	if t.Driver != "s3" {
		return "controller disk"
	}
	ep := endpointName
	if ep == "" {
		ep = t.Endpoint
	}
	where := "s3://" + t.Bucket
	if t.Prefix != "" {
		where += "/" + t.Prefix
	}
	return where + " (" + ep + ")"
}

func switchRows(s *client.RegistryStorageSwitch) [][]string {
	p := s.Progress
	return [][]string{{
		s.State, orDash(s.Step), fmt.Sprintf("%d/%d", p.ImagesDone, p.Images), bytesIEC(uint64(p.Bytes)), age(s.StartedAt), orDash(s.Message),
	}}
}

var switchHeaders = []string{"STATE", "STEP", "IMAGES", "COPIED", "STARTED", "MESSAGE"}

func (a *app) storageCmd() *cobra.Command {
	s := &cobra.Command{Use: "storage", Short: "Where the registry keeps images: controller disk or S3 (Phase 19)"}
	get := &cobra.Command{
		Use: "get", Short: "Show the registry's storage and the last switch", Args: cobra.NoArgs,
		Annotations: op("getStorage"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			st, err := c.RegistryStorage(ctx(cmd))
			if err != nil {
				return err
			}
			rows := [][]string{{"Storage", storageWhere(st.RegistryStorageTarget, st.EndpointName)}}
			if sw := st.Switch; sw != nil {
				rows = append(rows, []string{"Last switch", fmt.Sprintf("%s → %s: %s (%s)", storageWhere(sw.From, ""), storageWhere(sw.To, ""), sw.State, age(sw.StartedAt))})
				if sw.Message != "" {
					rows = append(rows, []string{"Message", sw.Message})
				}
				if sw.Kept != "" {
					rows = append(rows, []string{"Old images kept at", sw.Kept})
				}
				if sw.Freed > 0 {
					rows = append(rows, []string{"Disk freed", bytesIEC(uint64(sw.Freed))})
				}
			}
			return a.printer().table(st, []string{"FIELD", "VALUE"}, rows)
		},
	}
	var toDisk, wait bool
	var t client.RegistryStorageTarget
	set := &cobra.Command{
		Use:   "set (--disk | --s3 ENDPOINT --bucket BUCKET [--prefix PREFIX])",
		Short: "Switch storage: images are copied first (pushes pause), then the controller disk is freed",
		Long: "Copies every image to the new storage through a temporary registry, then switches the registry over.\n" +
			"Pushes are refused while images are copied; pulls keep working. Leaving the controller disk empties it;\n" +
			"leaving S3 keeps the old prefix (delete it from the bucket browser). On S3, nodes download layers from\n" +
			"the bucket directly, so every node must reach the endpoint.",
		Args:        cobra.NoArgs,
		Annotations: op("setStorage"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case toDisk && t.Endpoint != "":
				return errors.New("use either --disk or --s3")
			case toDisk:
				t = client.RegistryStorageTarget{Driver: "disk"}
			case t.Endpoint != "":
				t.Driver = "s3"
			default:
				return errors.New("use --disk or --s3 ENDPOINT --bucket BUCKET")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if _, err := c.SetRegistryStorage(ctx(cmd), t); err != nil {
				return err
			}
			if !wait {
				fmt.Fprintln(a.out, "Switch started. Follow it with: synctl registry storage get")
				return nil
			}
			last := ""
			for {
				select {
				case <-ctx(cmd).Done():
					return ctx(cmd).Err()
				case <-time.After(2 * time.Second):
				}
				st, err := c.RegistryStorage(ctx(cmd))
				if err != nil {
					return err
				}
				sw := st.Switch
				if sw == nil {
					continue
				}
				if sw.State == "running" {
					line := fmt.Sprintf("%s: %d/%d images, %s copied", sw.Step, sw.Progress.ImagesDone, sw.Progress.Images, bytesIEC(uint64(sw.Progress.Bytes)))
					if line != last {
						fmt.Fprintln(a.errOut, line)
						last = line
					}
					continue
				}
				if err := a.printer().table(sw, switchHeaders, switchRows(sw)); err != nil {
					return err
				}
				if sw.State != "succeeded" {
					return fmt.Errorf("switch failed: %s", sw.Message)
				}
				if sw.Kept != "" {
					fmt.Fprintln(a.errOut, "The old images were kept at", sw.Kept)
				}
				return nil
			}
		},
	}
	set.Flags().BoolVar(&toDisk, "disk", false, "keep images on the controller's disk")
	set.Flags().StringVar(&t.Endpoint, "s3", "", "S3 endpoint (name or ID) to keep images in")
	set.Flags().StringVar(&t.Bucket, "bucket", "", "bucket (must exist)")
	set.Flags().StringVar(&t.Prefix, "prefix", "", "key prefix inside the bucket (default: the bucket root)")
	set.Flags().BoolVarP(&wait, "wait", "w", false, "wait for the switch to finish")
	s.AddCommand(get, set)
	return s
}
