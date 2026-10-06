package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

// readRules reads {"rules": [...]} or a bare rules array from a file or stdin.
func (a *app) readRules(file string) ([]client.LifecycleRule, error) {
	var r io.Reader = a.in
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Rules []client.LifecycleRule `json:"rules"`
	}
	if json.Unmarshal(b, &doc) == nil && doc.Rules != nil {
		return doc.Rules, nil
	}
	var rules []client.LifecycleRule
	if err := json.Unmarshal(b, &rules); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	return rules, nil
}

// ruleFlags builds rules from -f or the one-rule shortcut flags.
type ruleFlags struct {
	file          string
	prefix        string
	keepLast      int
	olderThanDays int
}

func (f *ruleFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.file, "file", "f", "", `rules JSON ({"rules": [...]}), or - for stdin`)
	cmd.Flags().StringVar(&f.prefix, "prefix", "", "one-rule shortcut: tag prefix (default every tag)")
	cmd.Flags().IntVar(&f.keepLast, "keep-last", 0, "one-rule shortcut: keep the newest N images")
	cmd.Flags().IntVar(&f.olderThanDays, "older-than", 0, "one-rule shortcut: expire images older than N days")
}

func (f *ruleFlags) rules(a *app) ([]client.LifecycleRule, error) {
	if f.file != "" {
		return a.readRules(f.file)
	}
	if f.keepLast == 0 && f.olderThanDays == 0 {
		return nil, nil
	}
	return []client.LifecycleRule{{Priority: 1, TagPrefix: f.prefix, KeepLast: f.keepLast, OlderThanDays: f.olderThanDays}}, nil
}

func ruleRows(rules []client.LifecycleRule) [][]string {
	rows := make([][]string, 0, len(rules))
	for _, r := range rules {
		sel := r.TagPrefix + "*"
		action := fmt.Sprintf("keep newest %d", r.KeepLast)
		if r.OlderThanDays > 0 {
			action = fmt.Sprintf("expire after %d days", r.OlderThanDays)
		}
		rows = append(rows, []string{fmt.Sprint(r.Priority), sel, action, orDash(r.Description)})
	}
	return rows
}

func (a *app) lifecycleCmd() *cobra.Command {
	l := &cobra.Command{Use: "lifecycle", Short: "Repository lifecycle policies: expire old images (§5.10)"}
	var set, preview ruleFlags
	setCmd := &cobra.Command{
		Use: "set REPOSITORY", Short: "Set a repository's lifecycle policy", Args: cobra.ExactArgs(1),
		Annotations: op("putLifecyclePolicy"),
		Example: "  synctl registry lifecycle set shop/web --prefix sha- --keep-last 10\n" +
			`  echo '{"rules":[{"priority":1,"tagPrefix":"v","keepLast":5},{"priority":2,"olderThanDays":30}]}' | synctl registry lifecycle set shop/web -f -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rules, err := set.rules(a)
			if err != nil {
				return err
			}
			if rules == nil {
				return errors.New("give rules with -f, or --keep-last / --older-than")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			p, err := c.PutLifecyclePolicy(ctx(cmd), args[0], rules)
			if err != nil {
				return err
			}
			return a.printer().table(p, []string{"PRIORITY", "TAGS", "ACTION", "DESCRIPTION"}, ruleRows(p.Rules))
		},
	}
	set.add(setCmd)
	previewCmd := &cobra.Command{
		Use: "preview REPOSITORY", Short: "Dry run: which images a policy would delete now", Args: cobra.ExactArgs(1),
		Annotations: op("previewLifecyclePolicy"),
		RunE: func(cmd *cobra.Command, args []string) error {
			rules, err := preview.rules(a)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if rules == nil { // the saved policy
				p, err := c.GetLifecyclePolicy(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				rules = p.Rules
			}
			ds, err := c.PreviewLifecyclePolicy(ctx(cmd), args[0], rules)
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(ds))
			n := 0
			for _, d := range ds {
				action := "keep"
				if d.Expire {
					action, n = "EXPIRE", n+1
				}
				created := "-"
				if d.Created != nil {
					created = age(*d.Created)
				}
				rows = append(rows, []string{d.Tag, action, created, d.Reason})
			}
			if err := a.printer().table(ds, []string{"TAG", "ACTION", "CREATED", "REASON"}, rows); err != nil {
				return err
			}
			if a.output != "json" {
				fmt.Fprintf(a.out, "\n%d of %d images would be deleted.\n", n, len(ds))
			}
			return nil
		},
	}
	preview.add(previewCmd)
	l.AddCommand(setCmd, previewCmd,
		&cobra.Command{
			Use: "get REPOSITORY", Short: "Show a repository's lifecycle policy", Args: cobra.ExactArgs(1),
			Annotations: op("getLifecyclePolicy"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				p, err := c.GetLifecyclePolicy(ctx(cmd), args[0])
				if err != nil {
					return err
				}
				return a.printer().table(p, []string{"PRIORITY", "TAGS", "ACTION", "DESCRIPTION"}, ruleRows(p.Rules))
			},
		},
		&cobra.Command{
			Use: "delete REPOSITORY", Aliases: []string{"rm"}, Short: "Remove a repository's lifecycle policy", Args: cobra.ExactArgs(1),
			Annotations: op("deleteLifecyclePolicy"),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.DeleteLifecyclePolicy(ctx(cmd), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Removed the lifecycle policy of %s\n", args[0])
				return nil
			},
		},
	)
	return l
}

func gcRows(runs []client.GCRun) [][]string {
	rows := make([][]string, 0, len(runs))
	for _, r := range runs {
		dur := "-"
		if r.FinishedAt != nil {
			dur = r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
		}
		rows = append(rows, []string{r.ID, r.Status, r.Trigger, fmt.Sprint(r.Expired), bytesIEC(uint64(r.ReclaimedBytes)), dur, age(r.StartedAt), orDash(r.Message)})
	}
	return rows
}

var gcHeaders = []string{"RUN", "STATUS", "TRIGGER", "EXPIRED", "RECLAIMED", "DURATION", "STARTED", "MESSAGE"}

func (a *app) gcCmd() *cobra.Command {
	var wait bool
	g := &cobra.Command{Use: "gc", Short: "Registry cleanup: lifecycle policies, then garbage collection"}
	run := &cobra.Command{
		Use: "run", Short: "Clean up now (pushes are refused while it runs)", Args: cobra.NoArgs,
		Annotations: op("startRegistryCleanup"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			r, err := c.StartRegistryCleanup(ctx(cmd))
			if err != nil {
				return err
			}
			if !wait {
				fmt.Fprintf(a.out, "Started %s. See it with: synctl registry gc runs\n", r.ID)
				return nil
			}
			fmt.Fprintf(a.errOut, "Cleanup %s started\n", r.ID)
			for {
				select {
				case <-ctx(cmd).Done():
					return ctx(cmd).Err()
				case <-time.After(2 * time.Second):
				}
				runs, _, err := c.RegistryCleanups(ctx(cmd))
				if err != nil {
					return err
				}
				for _, x := range runs {
					if x.ID != r.ID || x.Status == "running" {
						continue
					}
					if err := a.printer().table(x, gcHeaders, gcRows([]client.GCRun{x})); err != nil {
						return err
					}
					if x.Status != "succeeded" {
						return fmt.Errorf("cleanup failed: %s", x.Message)
					}
					return nil
				}
			}
		},
	}
	run.Flags().BoolVarP(&wait, "wait", "w", false, "wait for the run to finish")
	g.AddCommand(run, &cobra.Command{
		Use: "runs", Short: "Recent cleanup runs", Args: cobra.NoArgs,
		Annotations: op("listRegistryCleanups"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			runs, _, err := c.RegistryCleanups(ctx(cmd))
			if err != nil {
				return err
			}
			return a.printer().table(runs, gcHeaders, gcRows(runs))
		},
	})
	return g
}
