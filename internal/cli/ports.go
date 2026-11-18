package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// The port table (Phase 26).

type portClaim struct {
	Node      string `json:"node"`
	Protocol  string `json:"protocol"`
	Port      int    `json:"port"`
	OwnerKind string `json:"ownerKind"`
	Owner     string `json:"owner"`
	PortName  string `json:"portName"`
	Mode      string `json:"mode"`
}

func (a *app) portsCmd() *cobra.Command {
	pc := &cobra.Command{Use: "ports", Short: "Public TCP/UDP ports on the load balancers: the port table and checks"}
	var node, kind string
	var reserved bool
	pc.AddCommand(&cobra.Command{
		Use: "list", Short: "Every public port on every load-balancer node, or with --reserved the ports that can't be chosen", Args: cobra.NoArgs,
		Annotations: op("listPublicPorts"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				Items     []portClaim `json:"items"`
				Conflicts []portClaim `json:"conflicts"`
				Reserved  []struct {
					Protocol string `json:"protocol"`
					Ports    string `json:"ports"`
					Reason   string `json:"reason"`
				} `json:"reserved"`
			}
			if err := a.do(cmd, "GET", "/api/v1/ports", nil, &out); err != nil {
				return err
			}
			if reserved {
				rows := make([][]string, 0, len(out.Reserved))
				for _, r := range out.Reserved {
					rows = append(rows, []string{r.Ports, r.Protocol, r.Reason})
				}
				return a.printer().table(out.Reserved, []string{"PORTS", "PROTOCOL", "RESERVED FOR"}, rows)
			}
			items := out.Items[:0]
			for _, c := range out.Items {
				if (node == "" || c.Node == node) && (kind == "" || c.OwnerKind == kind) {
					items = append(items, c)
				}
			}
			rows := make([][]string, 0, len(items))
			for _, c := range items {
				rows = append(rows, []string{strconv.Itoa(c.Port), c.Protocol, c.Node, c.OwnerKind, c.Owner, c.PortName, c.Mode})
			}
			if err := a.printer().table(items, []string{"PORT", "PROTOCOL", "NODE", "KIND", "OWNER", "OWNER PORT", "MODE"}, rows); err != nil {
				return err
			}
			if a.output != "json" {
				for _, c := range out.Conflicts {
					fmt.Fprintf(a.out, "warning: %s %d could not be claimed on %s for %s (another owner holds it there)\n", c.Protocol, c.Port, c.Node, c.Owner)
				}
			}
			return nil
		},
	})
	pc.Commands()[0].Flags().StringVar(&node, "node", "", "only this node")
	pc.Commands()[0].Flags().StringVar(&kind, "kind", "", "only service or database ports")
	pc.Commands()[0].Flags().BoolVar(&reserved, "reserved", false, "list the reserved ports instead")
	var project, service, env string
	check := &cobra.Command{
		Use: "check PORT[/tcp|/udp]", Short: "Whether a port can be a public port of a project's service, on every node of its load balancer", Args: cobra.ExactArgs(1),
		Example: `  synctl ports check 25 --project shop
  synctl ports check 19132/udp --project games --service bedrock --env production`,
		Annotations: op("checkProjectPublicPort"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return errors.New("--project is required")
			}
			port, proto, _ := strings.Cut(args[0], "/")
			if proto == "" {
				proto = "tcp"
			}
			q := url.Values{"protocol": {proto}, "port": {port}}
			if service != "" {
				q.Set("service", service)
				q.Set("environment", env)
			}
			var out struct {
				Protocol string `json:"protocol"`
				Port     int    `json:"port"`
				State    string `json:"state"`
				Reason   string `json:"reason"`
				Nodes    []struct {
					Node   string `json:"node"`
					State  string `json:"state"`
					Reason string `json:"reason"`
				} `json:"nodes"`
			}
			if err := a.do(cmd, "GET", "/api/v1/projects/"+url.PathEscape(project)+"/ports/check?"+q.Encode(), nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Nodes))
			for _, n := range out.Nodes {
				rows = append(rows, []string{n.Node, n.State, orDash(n.Reason)})
			}
			if err := a.printer().table(out, []string{"NODE", "STATE", "WHY"}, rows); err != nil {
				return err
			}
			if a.output != "json" {
				fmt.Fprintf(a.out, "%s %d: %s\n", out.Protocol, out.Port, out.State)
			}
			return nil
		},
	}
	check.Flags().StringVarP(&project, "project", "p", os.Getenv("SYNCLOUD_PROJECT"), "the project (or $SYNCLOUD_PROJECT)")
	check.Flags().StringVar(&service, "service", "", "a service whose own ports count as free")
	check.Flags().StringVarP(&env, "env", "e", os.Getenv("SYNCLOUD_ENV"), "the service's environment (or $SYNCLOUD_ENV)")
	pc.AddCommand(check)
	return pc
}
