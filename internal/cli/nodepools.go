package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Node pools, cloud providers and edge nodes (§6.5, §8.5).

type poolNodeView struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	Reserved  float64 `json:"reservedPercent"`
	Tasks     int     `json:"tasks"`
	Protected bool    `json:"scaleInProtected"`
	Draining  bool    `json:"draining"`
	Address   string  `json:"address"`
}

type nodePoolView struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Role     string          `json:"role"`
	Provider string          `json:"provider"`
	Spec     json.RawMessage `json:"spec"`
	Min      int             `json:"min"`
	Max      int             `json:"max"`
	Nodes    []poolNodeView  `json:"nodes"`
	Servers  []struct {
		Name    string `json:"name"`
		State   string `json:"state"`
		Message string `json:"message"`
	} `json:"servers"`
	CPU    [2]float64 `json:"cpu"`
	Memory [2]int     `json:"memory"`
}

func (a *app) nodePoolsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pools", Aliases: []string{"node-pools", "nodepools"}, Short: "Node pools and cluster autoscaling (§6.5)"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "Pools with their nodes, pending servers and capacity", Args: cobra.NoArgs,
		Annotations: op("listNodePools"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ps, err := listOf[nodePoolView](a, cmd, "/api/v1/node-pools")
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, p := range ps {
				kind := "manual"
				if p.Provider != "" {
					kind = p.Provider
				}
				var names []string
				for _, n := range p.Nodes {
					names = append(names, fmt.Sprintf("%s(%s,%.0f%%)", n.Name, n.Status, n.Reserved))
				}
				for _, s := range p.Servers {
					names = append(names, s.Name+"("+s.State+")")
				}
				size := "-"
				if p.ID != "" {
					size = fmt.Sprintf("%d–%d", p.Min, p.Max)
				}
				rows = append(rows, []string{p.Name, p.Role, kind, size, fmt.Sprintf("%.2g/%.2g", p.CPU[0], p.CPU[1]), joinOrDash(names)})
			}
			return a.printer().table(ps, []string{"POOL", "ROLE", "PROVIDER", "NODES MIN–MAX", "CPU RESERVED", "MEMBERS"}, rows)
		},
	})
	var file string
	apply := &cobra.Command{
		Use: "apply -f FILE", Short: "Create or update a pool from JSON (matched by name)", Args: cobra.NoArgs,
		Annotations: op("createNodePool", "updateNodePool"),
		Example: `  echo '{"name":"workers","provider":"hcloud","min":1,"max":5,
         "spec":{"region":"fsn1","type":"cx32","image":"ubuntu-24.04","nodeCpu":4,"nodeMemoryMiB":8192,"autoscale":true}}' | synctl pools apply -f -
  echo '{"name":"edge","role":"edge"}' | synctl pools apply -f -`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := a.readInput(file)
			if err != nil {
				return err
			}
			var req map[string]any
			if err := json.Unmarshal(b, &req); err != nil {
				return fmt.Errorf("parse pool: %w", err)
			}
			name, _ := req["name"].(string)
			ps, err := listOf[nodePoolView](a, cmd, "/api/v1/node-pools")
			if err != nil {
				return err
			}
			for _, p := range ps {
				if p.Name == name && p.ID != "" {
					if err := a.do(cmd, "PUT", "/api/v1/node-pools/"+url.PathEscape(name), req, nil); err != nil {
						return err
					}
					fmt.Fprintln(a.out, "Updated node pool "+name)
					return nil
				}
			}
			if err := a.do(cmd, "POST", "/api/v1/node-pools", req, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Created node pool "+name)
			return nil
		},
	}
	apply.Flags().StringVarP(&file, "file", "f", "", "pool JSON, or - for stdin")
	_ = apply.MarkFlagRequired("file")
	cmd.AddCommand(apply,
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete an empty pool", Args: cobra.ExactArgs(1),
			Annotations: op("deleteNodePool"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.do(cmd, "DELETE", "/api/v1/node-pools/"+url.PathEscape(args[0]), nil, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Deleted node pool "+args[0])
				return nil
			},
		},
		&cobra.Command{
			Use: "history NAME", Short: "Scaling decisions with their reasons", Args: cobra.ExactArgs(1),
			Annotations: op("listNodePoolEvents"),
			RunE: func(cmd *cobra.Command, args []string) error {
				es, err := listOf[struct {
					At      time.Time `json:"at"`
					Kind    string    `json:"kind"`
					Message string    `json:"message"`
				}](a, cmd, "/api/v1/node-pools/"+url.PathEscape(args[0])+"/events")
				if err != nil {
					return err
				}
				rows := [][]string{}
				for _, e := range es {
					rows = append(rows, []string{e.At.Local().Format(time.DateTime), e.Kind, e.Message})
				}
				return a.printer().table(es, []string{"TIME", "KIND", "MESSAGE"}, rows)
			},
		},
		&cobra.Command{
			Use: "join-command NAME", Short: "The one-line join for a manual pool (24-hour token)", Args: cobra.ExactArgs(1),
			Annotations: op("createPoolJoinCommand"),
			RunE: func(cmd *cobra.Command, args []string) error {
				var out map[string]any
				if err := a.do(cmd, "POST", "/api/v1/node-pools/"+url.PathEscape(args[0])+"/join-command", nil, &out); err != nil {
					return err
				}
				fmt.Fprintln(a.out, out["command"])
				return nil
			},
		},
	)
	var protected bool
	move := &cobra.Command{
		Use: "move NODE POOL", Short: "Move a node into a pool (\"default\" for none)", Args: cobra.ExactArgs(2),
		Annotations: op("setNodePool"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.nodeID(cmd, args[0])
			if err != nil {
				return err
			}
			if err := a.do(cmd, "PUT", "/api/v1/nodes/"+id+"/pool", map[string]any{"pool": args[1], "scaleInProtected": protected}, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Node %s is in pool %s\n", args[0], args[1])
			return nil
		},
	}
	move.Flags().BoolVar(&protected, "protect", false, "never remove the node when scaling in")
	cmd.AddCommand(move)

	providers := &cobra.Command{Use: "providers", Short: "Cloud provider accounts for provider-backed pools"}
	var ptype, token, purl string
	add := &cobra.Command{
		Use: "add NAME --type hetzner|digitalocean|webhook", Short: "Add a provider (the token is read from --token or SYNCLOUD_PROVIDER_TOKEN)", Args: cobra.ExactArgs(1),
		Annotations: op("createCloudProvider"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("SYNCLOUD_PROVIDER_TOKEN")
			}
			if err := a.do(cmd, "POST", "/api/v1/cloud-providers", map[string]any{"name": args[0], "type": ptype, "config": map[string]string{"token": token, "url": purl}}, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Added provider "+args[0])
			return nil
		},
	}
	add.Flags().StringVar(&ptype, "type", "", "hetzner, digitalocean or webhook")
	add.Flags().StringVar(&token, "token", "", "API token")
	add.Flags().StringVar(&purl, "url", "", "webhook URL (or an API base URL override)")
	providers.AddCommand(add,
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List providers", Args: cobra.NoArgs,
			Annotations: op("listCloudProviders"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				ps, err := listOf[map[string]any](a, cmd, "/api/v1/cloud-providers")
				if err != nil {
					return err
				}
				rows := [][]string{}
				for _, p := range ps {
					rows = append(rows, []string{fmt.Sprint(p["name"]), fmt.Sprint(p["type"]), fmt.Sprint(p["summary"])})
				}
				return a.printer().table(ps, []string{"NAME", "TYPE", "ACCOUNT"}, rows)
			},
		},
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a provider", Args: cobra.ExactArgs(1),
			Annotations: op("deleteCloudProvider"),
			RunE: func(cmd *cobra.Command, args []string) error {
				ps, err := listOf[map[string]any](a, cmd, "/api/v1/cloud-providers")
				if err != nil {
					return err
				}
				for _, p := range ps {
					if p["name"] == args[0] || p["id"] == args[0] {
						if err := a.do(cmd, "DELETE", "/api/v1/cloud-providers/"+fmt.Sprint(p["id"]), nil, nil); err != nil {
							return err
						}
						fmt.Fprintln(a.out, "Deleted provider "+args[0])
						return nil
					}
				}
				return fmt.Errorf("no provider %q", args[0])
			},
		},
	)
	cmd.AddCommand(providers)

	cmd.AddCommand(&cobra.Command{
		Use: "edges", Short: "Edge nodes' Traefik replicas and health (point public DNS at their addresses)", Args: cobra.NoArgs,
		Annotations: op("listEdges"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			es, err := listOf[map[string]any](a, cmd, "/api/v1/edges")
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, e := range es {
				rows = append(rows, []string{fmt.Sprint(e["node"]), fmt.Sprint(e["address"]), fmt.Sprint(e["state"]), strings.TrimSpace(fmt.Sprint(e["error"]))})
			}
			return a.printer().table(es, []string{"NODE", "ADDRESS", "STATE", "ERROR"}, rows)
		},
	})
	return cmd
}

// nodeID resolves a node name or ID.
func (a *app) nodeID(cmd *cobra.Command, ref string) (string, error) {
	c, err := a.client()
	if err != nil {
		return "", err
	}
	ns, err := c.ListNodes(ctx(cmd))
	if err != nil {
		return "", err
	}
	for _, n := range ns {
		if n.ID == ref || n.Name == ref {
			return n.ID, nil
		}
	}
	return "", fmt.Errorf("no node %q", ref)
}

