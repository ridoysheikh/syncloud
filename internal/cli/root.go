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
	"time"

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
	if p.Expiration != nil && time.Now().After(*p.Expiration) {
		return nil, fmt.Errorf("the credentials of this profile expired at %s: run `synctl login` again", p.Expiration.Local().Format(time.DateTime))
	}
	return client.New(p.Endpoint, client.Credentials{AccessKeyID: p.AccessKeyID, SecretAccessKey: p.SecretAccessKey, Token: p.Token, SessionToken: p.SessionToken})
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
		a.databasesCmd(),
		a.systemCmd(),
		a.domainCmd(),
		a.certsCmd(),
		a.backupsCmd(),
		a.networkCmd(),
		a.firewallCmd(),
		a.securityGroupsCmd(),
		a.stsCmd(),
		a.nodePoolsCmd(),
		a.s3Cmd(),
		a.auditCmd(),
		a.loginCmd(),
		a.quotaCmd(),
		a.usageCmd(),
		a.middlewaresCmd(),
		a.traefikCmd(),
		a.integrationsCmd(),
		a.projectsCmd(),
		a.envsCmd(),
		a.servicesCmd(),
		a.tasksCmd(),
		a.logsCmd(),
		a.execCmd(),
		a.jobsCmd(),
		a.runCmd(),
		a.runsCmd(),
		a.healthCmd(),
		a.registryCmd(),
		a.buildsCmd(),
		a.metricsCmd(),
		a.trafficCmd(),
		a.autoscaleCmd(),
		a.alertsCmd(),
		a.requestsCmd(),
	)
	return root
}

// op annotates a command with the operation(s) it wraps (comma-separated).
func op(ids ...string) map[string]string {
	return map[string]string{opAnnotation: strings.Join(ids, ",")}
}

func ctx(cmd *cobra.Command) context.Context { return cmd.Context() }

// OperationCommands maps each API operation to the synctl command that
// wraps it ("synctl services scale NAME REPLICAS"), for the API docs.
func OperationCommands() map[string]string {
	out := map[string]string{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if ids := c.Annotations[opAnnotation]; ids != "" {
			use := c.CommandPath()
			if args := strings.TrimPrefix(c.Use, c.Name()); args != "" {
				use += args
			}
			for _, id := range strings.Split(ids, ",") {
				if _, ok := out[id]; !ok {
					out[id] = use
				}
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(NewRoot(strings.NewReader(""), io.Discard, io.Discard))
	return out
}
