package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) domainCmd() *cobra.Command {
	d := &cobra.Command{Use: "domain", Short: "The platform base domain (dashboard, registry, service hostnames)"}
	show := func(_ *cobra.Command, s any, lines [][2]string) error {
		if a.output != "table" {
			return a.printer().json(s)
		}
		for _, l := range lines {
			fmt.Fprintf(a.out, "%-15s %s\n", l[0]+":", l[1])
		}
		return nil
	}
	d.AddCommand(
		&cobra.Command{
			Use: "get", Aliases: []string{"show"}, Short: "Show the base domain and suggested sslip.io/nip.io domains", Args: cobra.NoArgs,
			Annotations: op("getDomainSettings"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				s, err := c.GetDomainSettings(ctx(cmd))
				if err != nil {
					return err
				}
				ip := s.PublicIP
				if ip == "" {
					ip = "unknown (" + s.PublicIPError + ")"
				}
				acme := "disabled (self-signed certificates)"
				if s.ACME.Enabled {
					acme = s.ACME.DirectoryURL
				}
				return show(cmd, s, [][2]string{
					{"Base domain", orDash(s.BaseDomain)}, {"Dashboard", s.DashboardURL}, {"Registry", s.RegistryHost},
					{"Public IP", ip}, {"Suggestions", joinOrDash(s.Suggestions)}, {"ACME", acme},
				})
			},
		},
		&cobra.Command{
			Use: "set DOMAIN", Short: "Change the base domain (e.g. 203-0-113-10.sslip.io or cloud.example.com)", Args: cobra.ExactArgs(1),
			Annotations: op("setBaseDomain"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				s, err := c.SetBaseDomain(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				for _, w := range s.Warnings {
					fmt.Fprintln(a.errOut, "warning:", w)
				}
				return show(cmd, s, [][2]string{{"Base domain", s.BaseDomain}, {"Dashboard", s.DashboardURL}, {"Registry", s.RegistryHost}})
			},
		},
	)
	return d
}

func (a *app) certsCmd() *cobra.Command {
	cc := &cobra.Command{Use: "certificates", Aliases: []string{"certs", "cert"}, Short: "TLS certificates for platform hostnames"}
	cc.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls", "get"}, Short: "List certificates", Args: cobra.NoArgs,
			Annotations: op("listCertificates"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				cs, err := c.ListCertificates(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(cs))
				for _, x := range cs {
					expires, retry := "-", "-"
					if !x.NotAfter.IsZero() {
						expires = x.NotAfter.Local().Format(time.DateOnly)
					}
					if x.NextAttemptAt != nil && x.NextAttemptAt.After(time.Now()) {
						retry = x.NextAttemptAt.Local().Format(time.Kitchen)
					}
					rows = append(rows, []string{x.Host, x.Status, orDash(x.Issuer), expires, retry, orDash(x.LastError)})
				}
				return a.printer().table(cs, []string{"HOST", "STATUS", "ISSUER", "EXPIRES", "RETRY AT", "LAST ERROR"}, rows)
			},
		},
		&cobra.Command{
			Use: "renew HOST", Short: "Request a certificate now, skipping any retry backoff", Args: cobra.ExactArgs(1),
			Annotations: op("renewCertificate"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.RenewCertificate(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Requested a certificate for %s; follow progress with: synctl certs list\n", args[0])
				return nil
			},
		},
	)
	return cc
}
