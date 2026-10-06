// Command syncloud-agent runs on every node (§6).
//
//	syncloud-agent join --controller URL --token SYN-JOIN-… [--name NAME]
//	syncloud-agent run
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"syncloud/internal/agent"
	"syncloud/internal/uninstall"
	"syncloud/internal/upgrade"
	"syncloud/internal/version"
)

const usage = `usage:
  syncloud-agent join --controller URL --token TOKEN [--name NAME] [--data-dir DIR]
  syncloud-agent run [--data-dir DIR]
  syncloud-agent set-controller --gateway HOST:PORT [--controller URL] [--data-dir DIR]
  syncloud-agent uninstall [--purge] [--data-dir DIR]
  syncloud-agent version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "syncloud-agent:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dataDir := fs.String("data-dir", envOr("SYNCLOUD_AGENT_DATA_DIR", "/var/lib/syncloud-agent"), "agent state directory")

	switch args[0] {
	case "join":
		controller := fs.String("controller", "", "controller URL, e.g. https://203-0-113-10.sslip.io")
		token := fs.String("token", "", "join token")
		tokenFile := fs.String("token-file", "", "read the join token from a file")
		name := fs.String("name", "", "node name (default: hostname)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenFile != "" {
			b, err := os.ReadFile(*tokenFile)
			if err != nil {
				return err
			}
			*token = strings.TrimSpace(string(b))
		}
		if *controller == "" || *token == "" {
			return errors.New("--controller and --token (or --token-file) are required")
		}
		if *name == "" {
			*name = defaultName()
		}
		st, err := agent.Join(ctx, *dataDir, *controller, *token, *name)
		if err != nil {
			return err
		}
		fmt.Printf("Joined as node %s (%s). Start the agent with: syncloud-agent run\n", st.Name, st.NodeID)
		return nil

	case "run":
		network := fs.String("network", envOr("SYNCLOUD_AGENT_NETWORK", "auto"), "join the private network: on, off, or auto (on when running as root)")
		advertise := fs.String("advertise-address", os.Getenv("SYNCLOUD_ADVERTISE_ADDRESS"), "address other nodes use to reach this node's WireGuard port (default: as seen by the controller)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		opts := agent.Options{AdvertiseAddress: *advertise}
		switch *network {
		case "on":
			opts.Network = true
		case "off":
		case "auto":
			opts.Network = os.Geteuid() == 0
		default:
			return errors.New("--network must be on, off or auto")
		}
		log := slog.New(slog.NewTextHandler(os.Stderr, nil))
		return agent.Run(ctx, *dataDir, log, opts)

	case "set-controller":
		gateway := fs.String("gateway", "", "the controller's agent gateway, e.g. 203.0.113.20:7443")
		controller := fs.String("controller", "", "the controller URL (optional)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		st, err := agent.SetController(*dataDir, *gateway, *controller)
		if err != nil {
			return err
		}
		fmt.Printf("Node %s now connects to %s. Restart the agent (systemctl restart syncloud-agent).\n", st.Name, st.Gateway)
		return nil

	case "upgrade-guard":
		// Started by the agent itself before it replaces its binary (§5.0.1).
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return upgrade.AgentGuard(ctx, *dataDir)

	case "uninstall":
		purge := fs.Bool("purge", false, "also delete the agent's data directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return uninstall.Run(ctx, uninstall.Options{Units: []string{"syncloud-agent.service"}, DataDirs: []string{*dataDir}, Purge: *purge, Out: os.Stdout})

	case "version":
		fmt.Printf("%s (%s)\n", version.Version, version.Commit)
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// defaultName turns the hostname into a valid node name (a DNS label).
func defaultName() string {
	h, _ := os.Hostname()
	h = strings.Trim(notLabel.ReplaceAllString(strings.ToLower(strings.Split(h, ".")[0]), "-"), "-")
	if len(h) > 63 {
		h = strings.TrimRight(h[:63], "-")
	}
	if h == "" {
		h = "node"
	}
	return h
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
