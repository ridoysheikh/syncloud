package cli

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// PostgreSQL configuration (§13c2): parameters, add-ons and replication.
// Changes go through the database's spec; the controller applies them
// through Patroni and restarts members one at a time when a setting needs
// it.

// editPgSpec reads a PostgreSQL database's spec, lets edit change its
// postgres section and saves it.
func (a *app) editPgSpec(cmd *cobra.Command, name string, edit func(pg map[string]any) error) error {
	var cur struct {
		Spec map[string]any `json:"spec"`
	}
	if err := a.do(cmd, "GET", dbItem(name), nil, &cur); err != nil {
		return err
	}
	pg, _ := cur.Spec["postgres"].(map[string]any)
	if pg == nil {
		return errors.New(name + " is not a PostgreSQL database")
	}
	if err := edit(pg); err != nil {
		return err
	}
	return a.do(cmd, "PUT", dbItem(name), map[string]any{"spec": cur.Spec}, nil)
}

func pgConfigCommands(a *app) []*cobra.Command {
	// ── parameters ──
	var changedOnly bool
	settings := &cobra.Command{
		Use: "settings NAME", Short: "List the PostgreSQL parameters you can change, with their live values", Args: cobra.ExactArgs(1),
		Annotations: op("listPgSettings"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Items []struct {
					Name       string `json:"name"`
					Group      string `json:"group"`
					Restart    bool   `json:"restart"`
					Value      string `json:"value"`
					Configured string `json:"configured"`
					Pending    bool   `json:"pendingRestart"`
					Available  bool   `json:"available"`
				} `json:"items"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/pg/settings", nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, s := range out.Items {
				if !s.Available || (changedOnly && s.Configured == "") {
					continue
				}
				apply := "reload"
				if s.Restart {
					apply = "restart"
				}
				note := ""
				if s.Pending {
					note = "restart pending"
				}
				rows = append(rows, []string{s.Group, s.Name, s.Value, orDash(s.Configured), apply, note})
			}
			return a.printer().table(out.Items, []string{"GROUP", "PARAMETER", "VALUE", "SET", "APPLIES BY", ""}, rows)
		},
	}
	settings.Flags().BoolVar(&changedOnly, "changed", false, "only the parameters this database sets")

	config := &cobra.Command{Use: "config", Short: "Set or unset PostgreSQL parameters (see synctl db settings)"}
	config.AddCommand(&cobra.Command{
		Use: "set NAME KEY=VALUE...", Short: "Set parameters (reloaded at once, or applied by restarting members one at a time)", Args: cobra.MinimumNArgs(2),
		Example: `  synctl db config set orders work_mem=64MB statement_timeout=30s
  synctl db config set orders shared_buffers=1GB   # members restart one at a time`,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := a.editPgSpec(cmd, args[0], func(pg map[string]any) error {
				ps, _ := pg["parameters"].(map[string]any)
				if ps == nil {
					ps = map[string]any{}
				}
				for _, kv := range args[1:] {
					k, v, ok := strings.Cut(kv, "=")
					if !ok || k == "" {
						return fmt.Errorf("%q is not KEY=VALUE", kv)
					}
					ps[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
				pg["parameters"] = ps
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Parameters of %s saved; see them apply with: synctl db settings %s --changed\n", args[0], args[0])
			return nil
		},
	})
	config.AddCommand(&cobra.Command{
		Use: "unset NAME KEY...", Short: "Return parameters to the platform's defaults", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := a.editPgSpec(cmd, args[0], func(pg map[string]any) error {
				ps, _ := pg["parameters"].(map[string]any)
				for _, k := range args[1:] {
					if _, ok := ps[k]; !ok {
						return fmt.Errorf("%s does not set %s", args[0], k)
					}
					delete(ps, k)
				}
				pg["parameters"] = ps
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Unset %s on %s\n", strings.Join(args[1:], ", "), args[0])
			return nil
		},
	})

	// ── add-ons ──
	addons := &cobra.Command{
		Use: "addons NAME", Short: "List the optional extensions (add-ons) and which are enabled", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var cur struct {
				Spec struct {
					Postgres *struct {
						Extensions []string `json:"extensions"`
					} `json:"postgres"`
				} `json:"spec"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0]), nil, &cur); err != nil {
				return err
			}
			if cur.Spec.Postgres == nil {
				return errors.New(args[0] + " is not a PostgreSQL database")
			}
			var out struct {
				Addons []struct {
					Name        string `json:"name"`
					Title       string `json:"title"`
					Description string `json:"description"`
					Preload     string `json:"preload"`
				} `json:"addons"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/pg/settings", nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, x := range out.Addons {
				on := "off"
				if cur.Spec.Postgres.Extensions == nil || slices.Contains(cur.Spec.Postgres.Extensions, x.Name) {
					on = "enabled"
				}
				rows = append(rows, []string{x.Name, on, x.Description})
			}
			return a.printer().table(out.Addons, []string{"ADD-ON", "STATE", "WHAT"}, rows)
		},
	}
	addon := &cobra.Command{Use: "addon", Short: "Enable or disable an optional extension on a cluster"}
	for _, verb := range []string{"enable", "disable"} {
		addon.AddCommand(&cobra.Command{
			Use:   verb + " NAME ADDON...",
			Short: map[string]string{"enable": "Enable add-ons (those with a library restart the members one at a time)", "disable": "Disable add-ons (refused while installed in a database)"}[verb],
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				err := a.editPgSpec(cmd, args[0], func(pg map[string]any) error {
					var cur []string
					raw, present := pg["extensions"].([]any)
					for _, x := range raw {
						cur = append(cur, fmt.Sprint(x))
					}
					if !present && pg["extensions"] == nil {
						return errors.New(args[0] + " predates optional add-ons and has all of them enabled")
					}
					for _, x := range args[1:] {
						if verb == "enable" && !slices.Contains(cur, x) {
							cur = append(cur, x)
						}
						if verb == "disable" {
							cur = slices.DeleteFunc(cur, func(s string) bool { return s == x })
						}
					}
					sort.Strings(cur)
					pg["extensions"] = append([]string{}, cur...)
					return nil
				})
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s %s on %s (members restart one at a time when a library changes)\n", map[string]string{"enable": "Enabled", "disable": "Disabled"}[verb], strings.Join(args[1:], ", "), args[0])
				return nil
			},
		})
	}

	// ── replication ──
	replication := &cobra.Command{
		Use: "replication NAME", Short: "Show replication: replicas, lag, slots and the settings", Args: cobra.ExactArgs(1),
		Annotations: op("getPgReplication"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Settings struct {
					Mode             string `json:"mode"`
					SyncReplicas     int    `json:"syncReplicas"`
					MaxLagOnFailover int    `json:"maxLagOnFailover"`
					FailoverTTL      int    `json:"failoverTtl"`
					Slots            bool   `json:"slots"`
				} `json:"settings"`
				Replicas []struct {
					Name        string  `json:"name"`
					State       string  `json:"state"`
					SyncState   string  `json:"syncState"`
					LagBytes    int64   `json:"lagBytes"`
					ReplayLagMs float64 `json:"replayLagMs"`
				} `json:"replicas"`
				Slots []struct {
					Name      string `json:"name"`
					Active    bool   `json:"active"`
					WalStatus string `json:"walStatus"`
					HeldBytes int64  `json:"heldBytes"`
				} `json:"slots"`
				CurrentLSN string `json:"currentLsn"`
				Timeline   int    `json:"timeline"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/pg/replication", nil, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			s := out.Settings
			fmt.Fprintf(a.out, "Mode:      %s", s.Mode)
			if s.Mode != "async" {
				fmt.Fprintf(a.out, " (%d replica(s) confirm each commit)", s.SyncReplicas)
			}
			fmt.Fprintf(a.out, "\nFailover:  leader lease %ds; replicas more than %d MiB behind are not promoted\n", s.FailoverTTL, s.MaxLagOnFailover)
			fmt.Fprintf(a.out, "Primary:   timeline %d at %s\n\n", out.Timeline, out.CurrentLSN)
			rows := [][]string{}
			for _, r := range out.Replicas {
				rows = append(rows, []string{r.Name, r.State, r.SyncState, humanBytes(r.LagBytes), fmt.Sprintf("%.0f ms", r.ReplayLagMs)})
			}
			if err := a.printer().table(out.Replicas, []string{"REPLICA", "STATE", "SYNC", "BEHIND", "REPLAY LAG"}, rows); err != nil {
				return err
			}
			if len(out.Slots) > 0 {
				fmt.Fprintln(a.out)
				rows = [][]string{}
				for _, x := range out.Slots {
					rows = append(rows, []string{x.Name, fmt.Sprint(x.Active), x.WalStatus, humanBytes(x.HeldBytes)})
				}
				return a.printer().table(out.Slots, []string{"SLOT", "ACTIVE", "WAL", "HOLDS"}, rows)
			}
			return nil
		},
	}
	var rs struct {
		mode                      string
		sync, lag, ttl, keep, cap int
		slots, feedback           bool
	}
	replSet := &cobra.Command{
		Use: "set NAME", Short: "Change how members replicate and fail over", Args: cobra.ExactArgs(1),
		Example: `  synctl db replication set orders --mode sync
  synctl db replication set orders --mode strict --sync-replicas 2
  synctl db replication set orders --failover-ttl 20 --max-lag 16`,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := a.editPgSpec(cmd, args[0], func(pg map[string]any) error {
				r, _ := pg["replication"].(map[string]any)
				if r == nil {
					r = map[string]any{}
				}
				f := cmd.Flags()
				set := func(flag, key string, v any) {
					if f.Changed(flag) {
						r[key] = v
					}
				}
				set("mode", "mode", rs.mode)
				set("sync-replicas", "syncReplicas", rs.sync)
				set("max-lag", "maxLagOnFailover", rs.lag)
				set("failover-ttl", "failoverTtl", rs.ttl)
				set("wal-keep", "walKeepSize", rs.keep)
				set("slot-wal-cap", "maxSlotWalKeepSize", rs.cap)
				set("slots", "slots", rs.slots)
				set("hot-standby-feedback", "hotStandbyFeedback", rs.feedback)
				pg["replication"] = r
				delete(pg, "synchronous") // follows the mode
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Replication of %s saved; it applies without restarting members\n", args[0])
			return nil
		},
	}
	f := replSet.Flags()
	f.StringVar(&rs.mode, "mode", "async", "async, sync (commits wait for replicas) or strict (writes stop without one)")
	f.IntVar(&rs.sync, "sync-replicas", 1, "replicas that confirm each commit")
	f.IntVar(&rs.lag, "max-lag", 1, "MiB: replicas further behind are not promoted")
	f.IntVar(&rs.ttl, "failover-ttl", 30, "seconds: the leader lease (how fast a failed leader is replaced)")
	f.IntVar(&rs.keep, "wal-keep", 256, "MiB of WAL kept for replicas without slots")
	f.IntVar(&rs.cap, "slot-wal-cap", 0, "MiB of WAL a slot may hold (0 = unlimited)")
	f.BoolVar(&rs.slots, "slots", true, "replication slots keep the WAL replicas still need")
	f.BoolVar(&rs.feedback, "hot-standby-feedback", false, "keep rows replica queries still read from being vacuumed")
	replication.AddCommand(replSet)

	return []*cobra.Command{settings, config, addons, addon, replication}
}
