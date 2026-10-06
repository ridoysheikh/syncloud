package cli

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

// Controller and agent upgrades (§5.0.1).

type upgradeView struct {
	Current     string `json:"current"`
	Channel     string `json:"channel"`
	Latest      string `json:"latest"`
	LatestError string `json:"latestError"`
	Available   bool   `json:"available"`
	State       *struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Phase   string `json:"phase"`
		Message string `json:"message"`
		Steps   []struct {
			At   time.Time `json:"at"`
			Text string    `json:"text"`
		} `json:"steps"`
	} `json:"state"`
}

type agentUpgradeView struct {
	Target string `json:"target"`
	Nodes  []struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Arch      string `json:"arch"`
		Connected bool   `json:"connected"`
		Outdated  bool   `json:"outdated"`
	} `json:"nodes"`
	Rollout *struct {
		State   string `json:"state"`
		Message string `json:"message"`
		Nodes   []struct {
			Node  string `json:"node"`
			From  string `json:"from"`
			State string `json:"state"`
			Error string `json:"error"`
		} `json:"nodes"`
	} `json:"rollout"`
}

func terminalPhase(p string) bool { return p == "done" || p == "rolled-back" || p == "failed" }

func (a *app) systemUpgradeCmds() []*cobra.Command {
	health := &cobra.Command{
		Use: "health", Short: "Is the controller ready (database, system tasks)?", Args: cobra.NoArgs,
		Annotations: op("getHealth"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var h struct {
				Version  string   `json:"version"`
				Ready    bool     `json:"ready"`
				Problems []string `json:"problems"`
			}
			err := a.do(cmd, "GET", "/api/v1/system/health", nil, &h)
			if h.Version == "" && err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(h)
			}
			state := "ready"
			if !h.Ready {
				state = "NOT READY"
			}
			fmt.Fprintf(a.out, "%s (%s)\n", state, h.Version)
			for _, p := range h.Problems {
				fmt.Fprintln(a.out, "  -", p)
			}
			return nil
		},
	}
	var to string
	var wait bool
	upgrade := &cobra.Command{
		Use: "upgrade [--version V]", Short: "Show versions, or upgrade the controller (with automatic rollback)", Args: cobra.NoArgs,
		Annotations: op("getUpgrade", "upgradeController"),
		Example: `  synctl system upgrade --check        # versions and the last upgrade
  synctl system upgrade                # to the newest release on the channel
  synctl system upgrade --version 1.4.2`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			check, _ := cmd.Flags().GetBool("check")
			var u upgradeView
			if check {
				if err := a.do(cmd, "GET", "/api/v1/system/upgrade?refresh=1", nil, &u); err != nil {
					return err
				}
				if a.output == "json" {
					return a.printer().json(u)
				}
				fmt.Fprintf(a.out, "Running %s; channel %s: %s\n", u.Current, u.Channel, orDash(u.Latest+u.LatestError))
				if u.State != nil {
					fmt.Fprintf(a.out, "Last upgrade %s → %s: %s %s\n", u.State.From, u.State.To, u.State.Phase, u.State.Message)
				}
				return nil
			}
			if err := a.do(cmd, "POST", "/api/v1/system/upgrade", map[string]string{"version": to}, &u); err != nil {
				return err
			}
			if !wait {
				return a.printer().json(u)
			}
			seen := 0
			for {
				if u.State != nil {
					for _, s := range u.State.Steps[min(seen, len(u.State.Steps)):] {
						fmt.Fprintf(a.out, "  %s  %s\n", s.At.Local().Format("15:04:05"), s.Text)
					}
					seen = len(u.State.Steps)
					if terminalPhase(u.State.Phase) {
						if u.State.Phase != "done" {
							return errors.New(u.State.Message)
						}
						return nil
					}
				}
				time.Sleep(2 * time.Second)
				// The controller restarts during the upgrade: retry until it answers.
				_ = a.do(cmd, "GET", "/api/v1/system/upgrade", nil, &u)
			}
		},
	}
	upgrade.Flags().StringVar(&to, "version", "", "version to install (default: newest on the channel)")
	upgrade.Flags().Bool("check", false, "only show the running and available versions")
	upgrade.Flags().BoolVar(&wait, "wait", true, "follow the upgrade until it finishes")
	return []*cobra.Command{health, upgrade}
}

func (a *app) agentUpgradeCmds() []*cobra.Command {
	agents := &cobra.Command{
		Use: "agents", Short: "Agent version on each node and the last rollout", Args: cobra.NoArgs,
		Annotations: op("getAgentUpgrade"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var v agentUpgradeView
			if err := a.do(cmd, "GET", "/api/v1/nodes/agent-upgrade", nil, &v); err != nil {
				return err
			}
			return a.printAgents(v)
		},
	}
	var force, wait bool
	up := &cobra.Command{
		Use: "upgrade-agents [NODE…]", Short: "Upgrade agents node by node to the controller's version", Args: cobra.ArbitraryArgs,
		Annotations: op("upgradeAgents"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var v agentUpgradeView
			if err := a.do(cmd, "POST", "/api/v1/nodes/agent-upgrade", map[string]any{"nodes": args, "force": force}, &v); err != nil {
				return err
			}
			for wait && v.Rollout != nil && v.Rollout.State == "running" {
				time.Sleep(2 * time.Second)
				if err := a.do(cmd, "GET", "/api/v1/nodes/agent-upgrade", nil, &v); err != nil {
					return err
				}
			}
			if err := a.printAgents(v); err != nil {
				return err
			}
			if v.Rollout != nil && v.Rollout.State == "failed" {
				return errors.New(v.Rollout.Message)
			}
			return nil
		},
	}
	up.Flags().BoolVar(&force, "force", false, "also re-send to nodes already at the target version")
	up.Flags().BoolVar(&wait, "wait", true, "wait for the rollout to finish")
	rejoin := &cobra.Command{
		Use: "rejoin-command NODE", Short: "A single-use command that re-joins a reinstalled or restored node (keeps its ID and tasks)", Args: cobra.ExactArgs(1),
		Annotations: op("createRejoinToken"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out map[string]any
			if err := a.do(cmd, "POST", "/api/v1/nodes/"+url.PathEscape(args[0])+"/rejoin-token", nil, &out); err != nil {
				return err
			}
			fmt.Fprintln(a.out, out["command"])
			return nil
		},
	}
	return []*cobra.Command{agents, up, rejoin}
}

func (a *app) printAgents(v agentUpgradeView) error {
	if a.output == "json" {
		return a.printer().json(v)
	}
	status := map[string]string{}
	if v.Rollout != nil {
		for _, n := range v.Rollout.Nodes {
			status[n.Node] = n.State
			if n.Error != "" {
				status[n.Node] += ": " + n.Error
			}
		}
	}
	rows := [][]string{}
	for _, n := range v.Nodes {
		up := "current"
		if n.Outdated {
			up = "→ " + v.Target
		}
		rows = append(rows, []string{n.Name, orDash(n.Version), n.Arch, fmt.Sprint(n.Connected), up, orDash(status[n.Name])})
	}
	if err := a.printer().table(v, []string{"NODE", "AGENT", "ARCH", "CONNECTED", "UPGRADE", "LAST ROLLOUT"}, rows); err != nil {
		return err
	}
	if v.Rollout != nil && v.Rollout.Message != "" {
		fmt.Fprintf(a.out, "Rollout %s: %s\n", v.Rollout.State, v.Rollout.Message)
	}
	return nil
}
