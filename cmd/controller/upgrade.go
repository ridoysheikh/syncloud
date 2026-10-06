package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"syncloud/internal/uninstall"
	"syncloud/internal/upgrade"
	"syncloud/internal/version"
)

// snapshotDB writes a consistent copy of the database (VACUUM INTO works
// while the controller is running).
func snapshotDB(dbPath string) func(ctx context.Context, path string) error {
	return func(ctx context.Context, path string) error {
		db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(10000)")
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.ExecContext(ctx, `VACUUM INTO ?`, path)
		return err
	}
}

// upgradeCmd upgrades the running controller on this host (§5.0.1).
func upgradeCmd(args []string) error {
	fs := flag.NewFlagSet("syncloud-controller upgrade", flag.ContinueOnError)
	dataDir := fs.String("data-dir", envOr("SYNCLOUD_DATA_DIR", "/var/lib/syncloud"), "controller data directory")
	to := fs.String("version", "", "version to install (default: the newest on the channel)")
	releaseURL := fs.String("release-url", envOr("SYNCLOUD_RELEASE_URL", "https://get.syncloud.dev/releases"), "release location (https://, http:// or file://)")
	channel := fs.String("channel", envOr("SYNCLOUD_RELEASE_CHANNEL", "stable"), "release channel")
	downloads := fs.String("downloads-dir", envOr("SYNCLOUD_DOWNLOADS_DIR", "/usr/local/lib/syncloud/downloads"), "where worker binaries are served from")
	settle := fs.Duration("settle", 2*time.Minute, "how long the new controller must stay healthy")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long the new controller has to become healthy")
	wait := fs.Bool("wait", true, "wait for the upgrade to finish")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ri, err := upgrade.ReadRunInfo(filepath.Join(*dataDir, upgrade.RunInfoFile))
	if err != nil {
		return fmt.Errorf("is the controller running? %w", err)
	}
	src := upgrade.Source{Base: *releaseURL}
	if *to == "" {
		if *to, err = src.Latest(context.Background(), *channel); err != nil {
			return err
		}
	}
	current := version.Version
	if h, err := upgrade.CheckHealth(context.Background(), ri.HealthURL); err == nil {
		current = h.Version
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := upgrade.Begin(ctx, upgrade.Options{
		DataDir: *dataDir, DBPath: filepath.Join(*dataDir, "syncloud.db"), Version: *to, Current: current, Source: src,
		HealthURL: ri.HealthURL, Downloads: *downloads, Snapshot: snapshotDB(filepath.Join(*dataDir, "syncloud.db")),
		Run: ri, Settle: *settle, Timeout: *timeout,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Upgrading %s → %s (guard log: %s)\n", st.From, st.To, filepath.Join(st.Dir, "guard.log"))
	if !*wait {
		return nil
	}
	seen := len(st.Steps)
	for {
		time.Sleep(time.Second)
		s, err := upgrade.ReadState(*dataDir)
		if err != nil {
			return err
		}
		for _, step := range s.Steps[min(seen, len(s.Steps)):] {
			fmt.Printf("  %s  %s\n", step.At.Local().Format("15:04:05"), step.Text)
		}
		seen = len(s.Steps)
		if upgrade.Terminal(s.Phase) {
			if s.Phase != upgrade.PhaseDone {
				return errors.New(s.Message)
			}
			return nil
		}
	}
}

// upgradeGuard is started by an upgrade as a copy of the old binary.
func upgradeGuard(args []string) error {
	fs := flag.NewFlagSet("syncloud-controller upgrade-guard", flag.ContinueOnError)
	dataDir := fs.String("data-dir", envOr("SYNCLOUD_DATA_DIR", "/var/lib/syncloud"), "controller data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return upgrade.Guard(context.Background(), *dataDir)
}

// uninstallCmd removes SynCloud from this host (§5.0.1).
func uninstallCmd(args []string) error {
	fs := flag.NewFlagSet("syncloud-controller uninstall", flag.ContinueOnError)
	dataDir := fs.String("data-dir", envOr("SYNCLOUD_DATA_DIR", "/var/lib/syncloud"), "controller data directory")
	agentDir := fs.String("agent-data-dir", envOr("SYNCLOUD_AGENT_DATA_DIR", "/var/lib/syncloud-agent"), "local agent data directory")
	purge := fs.Bool("purge", false, "also delete all data: the database, keys, backups kept locally, and the platform volumes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Println("Uninstalling SynCloud")
	return uninstall.Run(context.Background(), uninstall.Options{
		Units:    []string{"syncloud-agent.service", "syncloud-controller.service"},
		DataDirs: []string{*dataDir, *agentDir, "/etc/syncloud"},
		Volumes:  []string{"syncloud-registry", "syncloud-victoriametrics", "syncloud-victorialogs", "syncloud-buildkit"},
		Purge:    *purge, Out: os.Stdout,
	})
}
