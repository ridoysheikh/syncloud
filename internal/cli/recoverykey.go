package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) recoveryKeyCmd() *cobra.Command {
	rk := &cobra.Command{Use: "recovery-key", Short: "The recovery key that opens backups (root user only)"}
	rk.AddCommand(&cobra.Command{
		Use: "show", Short: "Show the recovery key kept on this instance", Args: cobra.NoArgs,
		Annotations: op("getRecoveryKey"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			k, err := c.GetRecoveryKey(ctx(cmd))
			if err != nil {
				return err
			}
			if !k.Stored {
				return fmt.Errorf("this install's recovery key was never kept (it was created before keys were kept); create a new one with: synctl backups recovery-key create")
			}
			fmt.Fprintln(a.out, k.RecoveryKey)
			return nil
		},
	}, &cobra.Command{
		Use:   "create",
		Short: "Create and keep a recovery key on an install whose key was never kept",
		Long: "Replaces a recovery key that was never kept with a new one that is, so 'show' works.\n" +
			"Nothing is re-encrypted: the key only wraps the master key. The backups in S3 are re-keyed so the\n" +
			"new key opens them all, and a fresh backup runs.",
		Args:        cobra.NoArgs,
		Annotations: op("createRecoveryKey"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			k, err := c.CreateRecoveryKey(ctx(cmd))
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "New recovery key:\n\n    %s\n\n", k.RecoveryKey)
			if k.StoreError != "" {
				fmt.Fprintln(a.errOut, "It is in use but could not be kept, so save it now:", k.StoreError)
			} else {
				fmt.Fprintln(a.out, "It is kept on this instance (synctl backups recovery-key show). Keep a copy offline too: it is lost with the controller.")
			}
			switch {
			case k.Rekey != nil:
				fmt.Fprintf(a.out, "Re-keyed %d stored backups: the new key opens them.\n", k.Rekey.Rekeyed)
				if len(k.Rekey.Skipped) > 0 {
					fmt.Fprintf(a.out, "Left as they were (made with another master key): %s\n", strings.Join(k.Rekey.Skipped, ", "))
				}
				for _, f := range k.Rekey.Failed {
					fmt.Fprintln(a.errOut, "Could not re-key:", f)
				}
			case k.RekeyError != "":
				fmt.Fprintln(a.errOut, "Backups were not re-keyed:", k.RekeyError)
			}
			if k.Backup != nil {
				fmt.Fprintf(a.out, "Took a fresh backup: %s\n", k.Backup.Name)
			} else if k.BackupError != "" {
				fmt.Fprintln(a.errOut, "The fresh backup failed:", k.BackupError)
			}
			return nil
		},
	})
	return rk
}
