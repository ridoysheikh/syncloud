// Package gc removes what the platform no longer needs (Phase 9): history
// past its retention, and old upgrade directories (binaries and database
// snapshots of finished upgrades).
package gc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"syncloud/internal/store"
	"syncloud/internal/upgrade"
)

// KeepUpgrades is how many upgrade directories stay (newest first).
const KeepUpgrades = 3

// Run collects garbage shortly after start and then every 6 hours.
func Run(ctx context.Context, st *store.Store, dataDir string, r store.Retention, log *slog.Logger) {
	t := time.NewTimer(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		Once(ctx, st, dataDir, r, log)
		t.Reset(6 * time.Hour)
	}
}

// Once runs one collection.
func Once(ctx context.Context, st *store.Store, dataDir string, r store.Retention, log *slog.Logger) {
	res, err := st.GC(ctx, r, time.Now())
	if err != nil {
		log.Warn("gc: history", "err", err)
	}
	attrs := []any{}
	for table, n := range res {
		if n > 0 {
			attrs = append(attrs, table, n)
		}
	}
	if n := pruneUpgrades(dataDir); n > 0 {
		attrs = append(attrs, "upgrade_dirs", n)
	}
	if len(attrs) > 0 {
		log.Info("gc: removed", attrs...)
	}
}

// pruneUpgrades deletes all but the newest KeepUpgrades upgrade directories,
// never the one an upgrade in progress uses.
func pruneUpgrades(dataDir string) int {
	root := filepath.Join(dataDir, "upgrade")
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	active := ""
	if s, err := upgrade.ReadState(dataDir); err == nil && !upgrade.Terminal(s.Phase) {
		active = s.ID
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != active {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs))) // IDs are timestamps
	n := 0
	for _, d := range dirs[min(len(dirs), KeepUpgrades):] {
		if os.RemoveAll(filepath.Join(root, d)) == nil {
			n++
		}
	}
	return n
}
