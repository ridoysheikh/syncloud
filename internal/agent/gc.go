package agent

import (
	"context"
	"log/slog"
	"os"
	"time"

	"syncloud/internal/agent/docker"
	"syncloud/internal/agent/sysinfo"
	"syncloud/internal/upgrade"
)

// diskPressure is the disk use above which unused images are pruned.
const diskPressure = 0.85

// collectGarbage prunes images no container uses (older than a day) while
// the disk is above diskPressure, and removes a leftover staged upgrade.
func collectGarbage(ctx context.Context, d *docker.Client, dataDir string, log *slog.Logger) {
	sampler := sysinfo.NewSampler("/")
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, pending := upgrade.ReadAgentPending(dataDir); !pending {
			_ = os.Remove(upgrade.AgentStagingFile(dataDir))
		}
		m := sampler.Sample()
		if m.DiskTotalBytes == 0 || float64(m.DiskUsedBytes)/float64(m.DiskTotalBytes) < diskPressure {
			continue
		}
		n, err := d.PruneImages(ctx, 24*time.Hour)
		if err != nil {
			log.Warn("prune images", "err", err)
			continue
		}
		log.Info("disk above 85%: pruned unused images", "reclaimed_mib", n>>20)
	}
}
