package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func (a *app) backupsCmd() *cobra.Command {
	b := &cobra.Command{Use: "backups", Aliases: []string{"backup"}, Short: "Encrypted controller backups to S3"}

	cfg := &cobra.Command{Use: "config", Short: "Backup destination and schedule"}
	var in client.BackupConfig
	set := &cobra.Command{
		Use: "set", Short: "Set the S3 destination (the bucket is checked first)", Args: cobra.NoArgs,
		Annotations: op("setBackupConfig"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if in.SecretAccessKey == "" {
				in.SecretAccessKey = os.Getenv("SYNCLOUD_BACKUP_SECRET_ACCESS_KEY")
			}
			s, err := c.SetBackupConfig(ctx(cmd), in)
			if err != nil {
				return err
			}
			return a.printBackupSettings(s)
		},
	}
	f := set.Flags()
	f.StringVar(&in.Endpoint, "endpoint", "", "S3 endpoint URL, e.g. https://s3.eu-central-1.amazonaws.com")
	f.StringVar(&in.Region, "region", "", "S3 region")
	f.StringVar(&in.Bucket, "bucket", "", "bucket name")
	f.StringVar(&in.Prefix, "prefix", "", "key prefix, e.g. syncloud/prod")
	f.StringVar(&in.AccessKeyID, "access-key-id", "", "S3 access key ID")
	f.StringVar(&in.SecretAccessKey, "secret-access-key", "", "S3 secret (or $SYNCLOUD_BACKUP_SECRET_ACCESS_KEY; empty keeps the stored one)")
	f.IntVar(&in.IntervalMinutes, "interval", 0, "minutes between backups (default 60)")
	f.IntVar(&in.Retain, "retain", 0, "backups to keep (default 48)")

	cfg.AddCommand(
		&cobra.Command{
			Use: "get", Short: "Show the backup destination and last run", Args: cobra.NoArgs,
			Annotations: op("getBackupConfig"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				s, err := c.GetBackupConfig(ctx(cmd))
				if err != nil {
					return err
				}
				return a.printBackupSettings(s)
			},
		},
		set,
		&cobra.Command{
			Use: "disable", Short: "Turn backups off (stored backups are kept)", Args: cobra.NoArgs,
			Annotations: op("disableBackups"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DisableBackups(ctx(cmd)); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Backups disabled")
				return nil
			},
		},
	)

	var outFile string
	download := &cobra.Command{
		Use: "download", Short: "Download a fresh encrypted backup bundle", Args: cobra.NoArgs,
		Annotations: op("downloadBackup"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			status, body, err := c.Raw(ctx(cmd), "GET", "/api/v1/backups/download", nil)
			if err != nil {
				return err
			}
			if status != 200 {
				return fmt.Errorf("download failed: HTTP %d: %s", status, body)
			}
			if outFile == "" {
				outFile = "syncloud-" + time.Now().UTC().Format("20060102T150405Z") + ".synbak"
			}
			if err := os.WriteFile(outFile, body, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Saved %s (%s). Restore with: syncloud-controller restore --file %s --recovery-key …\n", outFile, bytesIEC(uint64(len(body))), outFile)
			return nil
		},
	}
	download.Flags().StringVarP(&outFile, "output-file", "f", "", "file to write (default syncloud-<time>.synbak)")

	b.AddCommand(
		cfg,
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List backups in S3, newest first", Args: cobra.NoArgs,
			Annotations: op("listBackups"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				bs, err := c.ListBackups(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(bs))
				for _, o := range bs {
					rows = append(rows, []string{o.Name, bytesIEC(uint64(o.Size)), age(o.CreatedAt)})
				}
				return a.printer().table(bs, []string{"NAME", "SIZE", "CREATED"}, rows)
			},
		},
		&cobra.Command{
			Use: "run", Short: "Take a backup now", Args: cobra.NoArgs,
			Annotations: op("runBackup"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				o, err := c.RunBackup(ctx(cmd))
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Uploaded %s (%s)\n", o.Name, bytesIEC(uint64(o.Size)))
				return nil
			},
		},
		download,
	)
	return b
}

func (a *app) printBackupSettings(s client.BackupSettings) error {
	if a.output == "json" {
		return a.printer().json(s)
	}
	if !s.Configured {
		fmt.Fprintln(a.out, "Backups are off. Configure them with: synctl backups config set --endpoint … --bucket … --access-key-id …")
		return nil
	}
	c := s.Config
	last := "never"
	if s.Status.LastSuccessAt != nil {
		last = age(*s.Status.LastSuccessAt) + " (" + s.Status.LastObject + ")"
	}
	for _, l := range [][2]string{
		{"Endpoint", c.Endpoint}, {"Bucket", c.Bucket}, {"Prefix", orDash(c.Prefix)}, {"Region", orDash(c.Region)},
		{"Interval", fmt.Sprintf("%d min", c.IntervalMinutes)}, {"Retain", fmt.Sprint(c.Retain)},
		{"Last success", last}, {"Last error", orDash(s.Status.LastError)},
	} {
		fmt.Fprintf(a.out, "%-13s %s\n", l[0]+":", l[1])
	}
	return nil
}
