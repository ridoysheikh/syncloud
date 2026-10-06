package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) networkCmd() *cobra.Command {
	n := &cobra.Command{Use: "network", Aliases: []string{"net"}, Short: "Private network (WireGuard mesh, IPAM)"}
	n.AddCommand(&cobra.Command{
		Use: "mesh", Short: "Mesh members, their addresses and peer links", Args: cobra.NoArgs,
		Annotations: op("getMesh"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			m, err := c.GetMesh(ctx(cmd))
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(m.Items))
			for _, x := range m.Items {
				up := 0
				for _, p := range x.Peers {
					if p.LastHandshake != nil && time.Since(*p.LastHandshake) < 3*time.Minute {
						up++
					}
				}
				state := "ok"
				switch {
				case x.PublicKey == "":
					state = "no key (networking off)"
				case x.Error != "":
					state = "error: " + x.Error
				case x.AppliedGen == 0:
					state = "pending"
				}
				rows = append(rows, []string{x.Name, x.Address, x.Subnet, orDash(x.Endpoint), orDash(x.Mode), fmt.Sprintf("%d/%d", up, len(x.Peers)), state})
			}
			return a.printer().table(m, []string{"NODE", "ADDRESS", "SUBNET", "ENDPOINT", "WIREGUARD", "PEERS UP", "STATE"}, rows)
		},
	})
	n.AddCommand(&cobra.Command{
		Use: "ipam", Short: "Address plan: node subnets with the addresses in use, service VIPs, cooling-down addresses", Args: cobra.NoArgs,
		Annotations: op("getIPAM"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			m, err := c.GetIPAM(ctx(cmd))
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(m)
			}
			var rows [][]string
			for _, nd := range m.Nodes {
				for _, x := range nd.Addresses {
					rows = append(rows, []string{x.IP, nd.Node, x.Owner, x.OwnerID, age(x.Since)})
				}
			}
			for _, v := range m.VIPs {
				rows = append(rows, []string{v.VIP, "(VIP)", v.Service, fmt.Sprintf("%d backends", v.Backends), "-"})
			}
			for _, r := range m.Released {
				rows = append(rows, []string{r.Address, "(released)", r.Kind, "reusable " + r.ReusableAt.Local().Format("15:04"), "-"})
			}
			return a.printer().table(m, []string{"ADDRESS", "NODE", "OWNER", "ID", "SINCE"}, rows)
		},
	})
	n.AddCommand(&cobra.Command{
		Use: "ip-history IP", Short: "Which tasks held an address, and when", Args: cobra.ExactArgs(1),
		Annotations: op("getIPHistory"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			hs, err := c.IPHistory(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(hs))
			for _, h := range hs {
				until := "now"
				if h.ReleasedAt != nil {
					until = h.ReleasedAt.Local().Format(time.DateTime)
				}
				rows = append(rows, []string{h.IP, h.Owner, h.OwnerID, h.AssignedAt.Local().Format(time.DateTime), until})
			}
			return a.printer().table(hs, []string{"IP", "OWNER", "ID", "FROM", "UNTIL"}, rows)
		},
	})
	n.AddCommand(&cobra.Command{
		Use: "dns", Short: "Internal DNS records (syncloud.internal) as served on every node", Args: cobra.NoArgs,
		Annotations: op("listDNSRecords"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			rs, err := c.DNSRecords(ctx(cmd))
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(rs))
			for _, r := range rs {
				rows = append(rows, []string{r.Name, r.Kind, joinOrDash(r.IPs)})
			}
			return a.printer().table(rs, []string{"NAME", "KIND", "ADDRESSES"}, rows)
		},
	})
	n.AddCommand(&cobra.Command{
		Use: "lookup NAME", Short: "Resolve a name as a task would (web.production.shop, or any external name)", Args: cobra.ExactArgs(1),
		Annotations: op("lookupDNS"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ans, err := c.DNSLookup(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(ans)
			}
			if !ans.Found {
				return fmt.Errorf("%s: not found (%s)", ans.Name, ans.Source)
			}
			fmt.Fprintf(a.out, "%s (%s)\n", ans.Name, ans.Source)
			for _, ip := range ans.IPs {
				fmt.Fprintln(a.out, "  "+ip)
			}
			return nil
		},
	})
	var rng string
	tp := &cobra.Command{
		Use: "top", Short: "Busiest services and tasks by network throughput (last 5 minutes)", Args: cobra.NoArgs,
		Annotations: op("getNetworkThroughput"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			t, err := c.NetworkThroughput(ctx(cmd), rng)
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, x := range t.TopServices {
				rows = append(rows, []string{"service", x.Key, "-", rate(x.RxBps), rate(x.TxBps)})
			}
			for _, x := range t.TopTasks {
				rows = append(rows, []string{"task", x.Key + " (" + x.Owner + ")", x.Node, rate(x.RxBps), rate(x.TxBps)})
			}
			return a.printer().table(t, []string{"KIND", "NAME", "NODE", "IN", "OUT"}, rows)
		},
	}
	tp.Flags().StringVar(&rng, "range", "1h", "chart range for -o json (15m, 1h, 6h, 24h, 7d)")
	n.AddCommand(tp)
	return n
}

// rate formats bytes per second.
func rate(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.1f kB/s", bps/(1<<10))
	}
	return fmt.Sprintf("%.0f B/s", bps)
}
