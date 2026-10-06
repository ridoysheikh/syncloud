package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) iamCmd() *cobra.Command {
	iam := &cobra.Command{Use: "iam", Short: "Identity and access management"}
	iam.AddCommand(a.iamAdminCmds()...)

	keys := &cobra.Command{Use: "access-keys", Aliases: []string{"keys"}, Short: "Your access keys (max 2, for rotation)"}
	keys.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List your access keys", Args: cobra.NoArgs,
			Annotations: op("listAccessKeys"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				ks, err := c.ListAccessKeys(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(ks))
				for _, k := range ks {
					rows = append(rows, []string{k.ID, orDash(k.Description), age(k.CreatedAt), ago(k.LastUsedAt), orDash(k.LastUsedIP)})
				}
				return a.printer().table(ks, []string{"ID", "DESCRIPTION", "CREATED", "LAST USED", "LAST IP"}, rows)
			},
		},
		a.createAccessKeyCmd(),
		&cobra.Command{
			Use: "delete ID", Aliases: []string{"rm"}, Short: "Delete an access key", Args: cobra.ExactArgs(1),
			Annotations: op("deleteAccessKey"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteAccessKey(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted access key %s\n", args[0])
				return nil
			},
		},
	)

	tokens := &cobra.Command{Use: "tokens", Short: "Your personal access tokens"}
	tokens.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List your tokens", Args: cobra.NoArgs,
			Annotations: op("listTokens"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				ts, err := c.ListTokens(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(ts))
				for _, t := range ts {
					exp := "never"
					if t.ExpiresAt != nil {
						exp = t.ExpiresAt.Local().Format("2006-01-02")
					}
					rows = append(rows, []string{t.ID, t.Name, age(t.CreatedAt), exp, ago(t.LastUsedAt)})
				}
				return a.printer().table(ts, []string{"ID", "NAME", "CREATED", "EXPIRES", "LAST USED"}, rows)
			},
		},
		a.createTokenCmd(),
		&cobra.Command{
			Use: "delete ID", Aliases: []string{"rm"}, Short: "Delete a token", Args: cobra.ExactArgs(1),
			Annotations: op("deleteToken"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteToken(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted token %s\n", args[0])
				return nil
			},
		},
	)

	iam.AddCommand(keys, tokens)
	return iam
}

func (a *app) createAccessKeyCmd() *cobra.Command {
	var desc string
	cmd := &cobra.Command{
		Use: "create", Short: "Create an access key (the secret is shown once)", Args: cobra.NoArgs,
		Annotations: op("createAccessKey"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			k, err := c.CreateAccessKey(ctx(cmd), desc)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(k)
			}
			fmt.Fprintf(a.out, "Access key ID:      %s\nSecret access key:  %s\n\nStore the secret now; it cannot be shown again.\n", k.ID, k.SecretAccessKey)
			return nil
		},
	}
	cmd.Flags().StringVar(&desc, "description", "", "what this key is for")
	return cmd
}

func (a *app) createTokenCmd() *cobra.Command {
	var name string
	var days int
	cmd := &cobra.Command{
		Use: "create", Short: "Create a personal access token (shown once)", Args: cobra.NoArgs,
		Annotations: op("createToken"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			t, err := c.CreateToken(ctx(cmd), name, days)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(t)
			}
			fmt.Fprintf(a.out, "Token: %s\n\nStore it now; it cannot be shown again.\n", t.Token)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "token name (required)")
	cmd.Flags().IntVar(&days, "expires-in-days", 90, "lifetime in days (0 = never expires)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
