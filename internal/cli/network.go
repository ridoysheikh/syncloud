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
	return n
}
