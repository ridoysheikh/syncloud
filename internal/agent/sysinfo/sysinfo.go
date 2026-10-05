// Package sysinfo reads node facts and metrics for heartbeats (§6.2).
package sysinfo

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"syncloud/internal/nodes"
)

// StaticInfo returns facts that rarely change, sent in Hello.
func StaticInfo(ctx context.Context, diskPath string) nodes.Info {
	host, _ := os.Hostname()
	total, _ := diskUsage(diskPath)
	mem := memInfo()
	return nodes.Info{
		Hostname:      host,
		OS:            osName(),
		Kernel:        kernelRelease(),
		Arch:          runtime.GOARCH,
		CPUCores:      runtime.NumCPU(),
		MemoryBytes:   mem.total,
		DiskBytes:     total,
		DockerVersion: dockerVersion(ctx),
	}
}

// Sampler computes CPU usage between consecutive calls.
type Sampler struct {
	diskPath  string
	prevIdle  uint64
	prevTotal uint64
}

func NewSampler(diskPath string) *Sampler {
	s := &Sampler{diskPath: diskPath}
	s.prevIdle, s.prevTotal = cpuTimes()
	return s
}

func (s *Sampler) Sample() nodes.Metrics {
	idle, total := cpuTimes()
	var cpu float64
	if dt := total - s.prevTotal; total > s.prevTotal {
		cpu = 100 * (1 - float64(idle-s.prevIdle)/float64(dt))
	}
	s.prevIdle, s.prevTotal = idle, total

	mem := memInfo()
	diskTotal, diskUsed := diskUsage(s.diskPath)
	l1, l5, l15 := loadAvg()
	rx, tx := netBytes()
	return nodes.Metrics{
		CPUPercent:       clamp(cpu, 0, 100),
		MemoryUsedBytes:  mem.total - mem.available,
		MemoryTotalBytes: mem.total,
		DiskUsedBytes:    diskUsed,
		DiskTotalBytes:   diskTotal,
		Load1:            l1,
		Load5:            l5,
		Load15:           l15,
		NetRxBytes:       rx,
		NetTxBytes:       tx,
		UptimeSeconds:    uptime(),
	}
}

func clamp(v, lo, hi float64) float64 {
	return max(lo, min(hi, v))
}

// dockerVersion asks the local Docker Engine API; empty when unavailable.
func dockerVersion(ctx context.Context) string {
	c := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
		}},
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://docker/version", nil)
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct{ Version string }
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return ""
	}
	return v.Version
}
