package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ridoysheikh/syncloud/internal/client"
)

func (a *app) settingsCmd() *cobra.Command {
	s := &cobra.Command{Use: "settings", Short: "Workspace settings: branding and email"}
	s.AddCommand(a.brandingCmd(), a.smtpCmd())
	return s
}

func (a *app) printBranding(b client.Branding) error {
	logo := "default"
	if b.HasLogo {
		logo = "custom"
	}
	return a.printer().table(b, []string{"FIELD", "VALUE"}, [][]string{{"Name", b.Name}, {"Title", b.Title}, {"Logo", logo}})
}

func (a *app) brandingCmd() *cobra.Command {
	b := &cobra.Command{Use: "branding", Short: "Workspace name, browser title and logo"}
	get := &cobra.Command{
		Use: "get", Short: "Show the branding", Args: cobra.NoArgs,
		Annotations: op("getBranding"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			br, err := c.GetBranding(ctx(cmd))
			if err != nil {
				return err
			}
			return a.printBranding(br)
		},
	}
	var name, title, logo string
	var removeLogo bool
	set := &cobra.Command{
		Use:   "set [--name NAME] [--title TITLE] [--logo FILE | --remove-logo]",
		Short: "Change the branding (an empty --name or --title goes back to the default)",
		Long: "The logo is a PNG, JPEG or WebP image up to 512 KB. It is made square (256×256), and the\n" +
			"browser favicon is made from it.",
		Args:        cobra.NoArgs,
		Annotations: op("setBranding", "setBrandingLogo", "deleteBrandingLogo"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			if !f.Changed("name") && !f.Changed("title") && logo == "" && !removeLogo {
				return errors.New("give --name, --title, --logo or --remove-logo")
			}
			if logo != "" && removeLogo {
				return errors.New("use either --logo or --remove-logo")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			br, err := c.GetBranding(ctx(cmd))
			if err != nil {
				return err
			}
			if f.Changed("name") || f.Changed("title") {
				n, t := br.Name, br.Title
				if t == n {
					t = "" // the title followed the name; keep it that way
				}
				if f.Changed("name") {
					n = name
				}
				if f.Changed("title") {
					t = title
				}
				if br, err = c.SetBranding(ctx(cmd), n, t); err != nil {
					return err
				}
			}
			switch {
			case logo != "":
				data, err := os.ReadFile(logo)
				if err != nil {
					return err
				}
				if br, err = c.SetBrandingLogo(ctx(cmd), data); err != nil {
					return err
				}
			case removeLogo:
				if br, err = c.DeleteBrandingLogo(ctx(cmd)); err != nil {
					return err
				}
			}
			fmt.Fprintln(a.errOut, "Branding updated.")
			return a.printBranding(br)
		},
	}
	set.Flags().StringVar(&name, "name", "", "workspace name (at most 40 characters)")
	set.Flags().StringVar(&title, "title", "", "browser tab title (default: the name)")
	set.Flags().StringVar(&logo, "logo", "", "logo image file (PNG, JPEG or WebP)")
	set.Flags().BoolVar(&removeLogo, "remove-logo", false, "go back to the default logo")
	b.AddCommand(get, set)
	return b
}

func (a *app) smtpCmd() *cobra.Command {
	sm := &cobra.Command{Use: "smtp", Aliases: []string{"email"}, Short: "The SMTP server alert emails are sent through"}
	sm.AddCommand(&cobra.Command{
		Use: "get", Short: "Show the SMTP server (never the password)", Args: cobra.NoArgs, Annotations: op("getSMTP"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			v, err := c.GetSMTP(ctx(cmd))
			if err != nil {
				return err
			}
			if !v.Configured {
				fmt.Fprintln(a.out, "Email is not set up: synctl settings smtp set --host … --from …")
				return nil
			}
			pw := "-"
			if v.HasPassword {
				pw = "set"
			}
			return a.printer().table(v, []string{"FIELD", "VALUE"}, [][]string{
				{"Server", fmt.Sprintf("%s:%d (%s)", v.Host, v.Port, v.Security)}, {"Username", orDash(v.Username)}, {"Password", pw},
				{"From", v.From}, {"From name", orDash(v.FromName)},
			})
		},
	})
	var in client.SMTP
	set := &cobra.Command{
		Use:   "set --host HOST --from ADDRESS [flags]",
		Short: "Set the SMTP server (the password is read from $SYNCLOUD_SMTP_PASSWORD when not given)",
		Example: `  SYNCLOUD_SMTP_PASSWORD=… synctl settings smtp set --host smtp.example.com --username alerts \
      --from alerts@example.com --from-name "Acme Cloud"
  synctl settings smtp set --host smtp.example.com --security tls --port 465 --from alerts@example.com`,
		Args: cobra.NoArgs, Annotations: op("setSMTP"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if in.Password == "" {
				in.Password = os.Getenv("SYNCLOUD_SMTP_PASSWORD")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			v, err := c.SetSMTP(ctx(cmd), in)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Email goes through %s:%d (%s) from %s. Try it: synctl settings smtp test you@example.com\n", v.Host, v.Port, v.Security, v.From)
			return nil
		},
	}
	f := set.Flags()
	f.StringVar(&in.Host, "host", "", "SMTP server name or address")
	f.IntVar(&in.Port, "port", 0, "port (default 587 for starttls, 465 for tls, 25 for none)")
	f.StringVar(&in.Security, "security", "starttls", "starttls, tls or none")
	f.StringVar(&in.Username, "username", "", "sign-in user")
	f.StringVar(&in.Password, "password", "", "sign-in password (prefer $SYNCLOUD_SMTP_PASSWORD; empty keeps the stored one)")
	f.StringVar(&in.From, "from", "", "sender address")
	f.StringVar(&in.FromName, "from-name", "", "sender name (default: the workspace name)")
	sm.AddCommand(set, &cobra.Command{
		Use: "test ADDRESS", Short: "Send a test email", Args: cobra.ExactArgs(1), Annotations: op("testSMTP"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sendErr, err := c.TestSMTP(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			if sendErr != "" {
				return errors.New("the email was not sent: " + sendErr)
			}
			fmt.Fprintln(a.out, "Sent. Check", args[0])
			return nil
		},
	}, &cobra.Command{
		Use: "disable", Short: "Turn email off (channels with their own server keep working)", Args: cobra.NoArgs, Annotations: op("deleteSMTP"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.DeleteSMTP(ctx(cmd)); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Email is off.")
			return nil
		},
	})
	return sm
}
