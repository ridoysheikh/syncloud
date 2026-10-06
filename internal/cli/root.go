// Package cli implements synctl, the SynCloud command line (§7.1).
//
// Every API operation must have a synctl command (parity, §5.1). Commands that
// wrap an operation carry its operationId in Annotations[opAnnotation]; a test
// checks them against the OpenAPI spec.
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
	"syncloud/internal/version"
)

const opAnnotation = "operationId"

type app struct {
	in       io.Reader
	out      io.Writer
	errOut   io.Writer
	profile  string
	endpoint string
	output   string
}

func (a *app) printer() printer { return printer{w: a.out, format: a.output} }

// client builds an API client from the resolved profile.
func (a *app) client() (*client.Client, error) {
	p, err := resolveProfile(a.profile, a.endpoint)
	if err != nil {
		return nil, err
	}
	return client.New(p.Endpoint, client.Credentials{AccessKeyID: p.AccessKeyID, SecretAccessKey: p.SecretAccessKey, Token: p.Token})
}

// NewRoot returns the synctl root command.
func NewRoot(in io.Reader, out, errOut io.Writer) *cobra.Command {
	a := &app{in: in, out: out, errOut: errOut}
	root := &cobra.Command{
		Use:           "synctl",
		Short:         "Control a SynCloud cluster",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.output != "table" && a.output != "json" {
				return fmt.Errorf("--output must be table or json")
			}
			return nil
		},
	}
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().StringVar(&a.profile, "profile", "", "credentials profile (default \"default\", or $"+EnvProfile+")")
	root.PersistentFlags().StringVar(&a.endpoint, "endpoint", "", "controller URL (overrides the profile)")
	root.PersistentFlags().StringVarP(&a.output, "output", "o", "table", "output format: table or json")

	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print the synctl version",
			Run: func(cmd *cobra.Command, _ []string) {
				fmt.Fprintf(a.out, "synctl %s (%s)\n", version.Version, version.Commit)
			},
		},
		a.configureCmd(),
		a.statusCmd(),
		a.whoamiCmd(),
		a.apiCmd(),
		a.iamCmd(),
		a.nodesCmd(),
		a.systemCmd(),
		a.domainCmd(),
		a.certsCmd(),
		a.backupsCmd(),
		a.networkCmd(),
		a.firewallCmd(),
		a.projectsCmd(),
		a.envsCmd(),
		a.servicesCmd(),
		a.tasksCmd(),
		a.logsCmd(),
		a.execCmd(),
		a.jobsCmd(),
		a.runCmd(),
		a.runsCmd(),
	)
	return root
}

// op annotates a command with the operation(s) it wraps (comma-separated).
func op(ids ...string) map[string]string {
	return map[string]string{opAnnotation: strings.Join(ids, ",")}
}

func ctx(cmd *cobra.Command) context.Context { return cmd.Context() }
