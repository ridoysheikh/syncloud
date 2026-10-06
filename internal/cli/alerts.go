package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

func (a *app) alertsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "alerts", Aliases: []string{"alert"}, Short: "Alert rules, notification channels and alert history (§9)"}

	active := &cobra.Command{
		Use: "active", Short: "Alerts that are firing or pending", Args: cobra.NoArgs, Annotations: op("listActiveAlerts"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			as, err := c.ActiveAlerts(ctx(cmd))
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(as))
			for _, x := range as {
				rows = append(rows, []string{strings.ToUpper(x.State), x.Severity, x.Rule, x.Label, age(x.Since), x.Message})
			}
			return a.printer().table(as, []string{"STATE", "SEVERITY", "RULE", "INSTANCE", "SINCE", "MESSAGE"}, rows)
		},
	}
	var limit int
	history := &cobra.Command{
		Use: "events", Aliases: []string{"history"}, Short: "Notifications sent, newest first", Args: cobra.NoArgs, Annotations: op("listAlertEvents"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			evs, err := c.AlertEvents(ctx(cmd), limit)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(evs))
			for _, e := range evs {
				rows = append(rows, []string{age(e.At), strings.ToUpper(e.Kind), e.Rule, e.Label, e.Message, orDash(e.Delivery)})
			}
			return a.printer().table(evs, []string{"WHEN", "KIND", "RULE", "INSTANCE", "MESSAGE", "DELIVERY"}, rows)
		},
	}
	history.Flags().IntVar(&limit, "limit", 30, "how many to show")
	cmd.AddCommand(active, history, a.alertRulesCmd(), a.alertChannelsCmd())
	return cmd
}

func (a *app) alertRulesCmd() *cobra.Command {
	rules := &cobra.Command{Use: "rules", Aliases: []string{"rule"}, Short: "Alert rules"}
	find := func(cmd *cobra.Command, c *client.Client, name string) (map[string]any, error) {
		rs, err := c.ListAlertRules(ctx(cmd))
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if r["name"] == name || r["id"] == name {
				return r, nil
			}
		}
		return nil, nil
	}
	var file string
	apply := &cobra.Command{
		Use: "apply -f RULE.json", Short: "Create a rule, or replace the one with the same name",
		Example: `  echo '{"name":"web 5xx","type":"metric","metric":"error_rate","op":">","threshold":5,
         "project":"shop","service":"web","forSeconds":120,"channels":["ops"]}' | synctl alerts rules apply -f -`,
		Args: cobra.NoArgs, Annotations: op("createAlertRule", "updateAlertRule"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var in io.Reader = a.in
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				in = f
			}
			var rule map[string]any
			if err := json.NewDecoder(in).Decode(&rule); err != nil {
				return fmt.Errorf("rule JSON: %w", err)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			// Channels may be given by name.
			if chans, ok := rule["channels"].([]any); ok {
				all, err := c.ListAlertChannels(ctx(cmd))
				if err != nil {
					return err
				}
				for i, x := range chans {
					for _, ch := range all {
						if ch.Name == x {
							chans[i] = ch.ID
						}
					}
				}
			}
			name, _ := rule["name"].(string)
			old, err := find(cmd, c, name)
			if err != nil {
				return err
			}
			id := ""
			if old != nil {
				id, _ = old["id"].(string)
			}
			out, err := c.PutAlertRule(ctx(cmd), id, rule)
			if err != nil {
				return err
			}
			verb := "Created"
			if id != "" {
				verb = "Updated"
			}
			fmt.Fprintf(a.out, "%s rule %s (%s)\n", verb, out["name"], out["id"])
			return nil
		},
	}
	apply.Flags().StringVarP(&file, "file", "f", "", "rule as JSON (- for stdin)")
	_ = apply.MarkFlagRequired("file")
	rules.AddCommand(apply,
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List alert rules", Args: cobra.NoArgs, Annotations: op("listAlertRules"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				rs, err := c.ListAlertRules(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(rs))
				for _, r := range rs {
					cond := fmt.Sprint(r["type"])
					switch r["type"] {
					case "metric":
						cond = fmt.Sprintf("%v %v %v", r["metric"], r["op"], r["threshold"])
					case "promql":
						cond = fmt.Sprintf("(%v) %v %v", r["query"], r["op"], r["threshold"])
					case "log":
						cond = fmt.Sprintf("log %q level=%v %v %v", r["text"], orDash(fmt.Sprint(r["level"])), r["op"], r["threshold"])
					}
					scope := strings.Trim(fmt.Sprintf("%v/%v/%v", orEmpty(r["project"]), orEmpty(r["environment"]), orEmpty(r["service"])), "/")
					on := "on"
					if r["enabled"] != true {
						on = "off"
					}
					rows = append(rows, []string{fmt.Sprint(r["name"]), on, fmt.Sprint(r["severity"]), orDash(scope), cond})
				}
				return a.printer().table(rs, []string{"NAME", "ENABLED", "SEVERITY", "SCOPE", "CONDITION"}, rows)
			},
		},
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete an alert rule", Args: cobra.ExactArgs(1), Annotations: op("deleteAlertRule"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				r, err := find(cmd, c, args[0])
				if err != nil {
					return err
				}
				if r == nil {
					return fmt.Errorf("no rule %s", args[0])
				}
				if err := c.DeleteAlertRule(ctx(cmd), fmt.Sprint(r["id"])); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted rule %s\n", args[0])
				return nil
			},
		},
	)
	return rules
}

func orEmpty(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (a *app) alertChannelsCmd() *cobra.Command {
	ch := &cobra.Command{Use: "channels", Aliases: []string{"channel"}, Short: "Notification channels: webhook, Slack, Discord, Telegram, email"}
	find := func(cmd *cobra.Command, c *client.Client, name string) (*client.AlertChannel, error) {
		cs, err := c.ListAlertChannels(ctx(cmd))
		if err != nil {
			return nil, err
		}
		for i := range cs {
			if cs[i].Name == name || cs[i].ID == name {
				return &cs[i], nil
			}
		}
		return nil, fmt.Errorf("no channel %s", name)
	}
	var typ string
	var cfg client.AlertChannelConfig
	add := &cobra.Command{
		Use: "add NAME --type TYPE", Short: "Add a channel, or replace the one with the same name",
		Example: `  synctl alerts channels add ops --type slack --url https://hooks.slack.com/services/…
  synctl alerts channels add oncall --type telegram --bot-token 123:ABC --chat-id -100200300
  SYNCLOUD_SMTP_PASSWORD=… synctl alerts channels add mail --type email --smtp-host smtp.example.com \
      --username alerts --from alerts@example.com --to ops@example.com`,
		Args: cobra.ExactArgs(1), Annotations: op("createAlertChannel", "updateAlertChannel"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if typ == "" {
				return errors.New("--type is required: webhook, slack, discord, telegram or email")
			}
			if cfg.Password == "" {
				cfg.Password = os.Getenv("SYNCLOUD_SMTP_PASSWORD")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id := ""
			if old, err := find(cmd, c, args[0]); err == nil {
				id = old.ID
			}
			out, err := c.PutAlertChannel(ctx(cmd), id, args[0], typ, cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Channel %s: %s → %s\n", out.Name, out.Type, out.Summary)
			return nil
		},
	}
	f := add.Flags()
	f.StringVar(&typ, "type", "", "webhook, slack, discord, telegram or email")
	f.StringVar(&cfg.URL, "url", "", "webhook, Slack or Discord URL")
	f.StringVar(&cfg.BotToken, "bot-token", "", "Telegram bot token")
	f.StringVar(&cfg.ChatID, "chat-id", "", "Telegram chat ID")
	f.StringVar(&cfg.SMTPHost, "smtp-host", "", "email: SMTP server")
	f.IntVar(&cfg.SMTPPort, "smtp-port", 0, "email: SMTP port (default 587; 465 = implicit TLS)")
	f.StringVar(&cfg.Username, "username", "", "email: SMTP user")
	f.StringVar(&cfg.From, "from", "", "email: sender address")
	f.StringSliceVar(&cfg.To, "to", nil, "email: recipients (repeat or comma-separate)")
	ch.AddCommand(add,
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List channels", Args: cobra.NoArgs, Annotations: op("listAlertChannels"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				cs, err := c.ListAlertChannels(ctx(cmd))
				if err != nil {
					return err
				}
				rows := make([][]string, 0, len(cs))
				for _, x := range cs {
					rows = append(rows, []string{x.Name, x.Type, x.Summary})
				}
				return a.printer().table(cs, []string{"NAME", "TYPE", "SENDS TO"}, rows)
			},
		},
		&cobra.Command{
			Use: "test NAME", Short: "Send a test notification", Args: cobra.ExactArgs(1), Annotations: op("testAlertChannel"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				x, err := find(cmd, c, args[0])
				if err != nil {
					return err
				}
				if err := c.TestAlertChannel(ctx(cmd), x.ID); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Test notification delivered to %s\n", x.Name)
				return nil
			},
		},
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a channel no rule uses", Args: cobra.ExactArgs(1), Annotations: op("deleteAlertChannel"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				x, err := find(cmd, c, args[0])
				if err != nil {
					return err
				}
				if err := c.DeleteAlertChannel(ctx(cmd), x.ID); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Deleted channel %s\n", x.Name)
				return nil
			},
		},
	)
	return ch
}
