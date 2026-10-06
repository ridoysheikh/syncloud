package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"syncloud/internal/agent/docker"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

const (
	statsEvery      = 10 * time.Second
	maxPendingStats = 5000 // samples kept while the controller is unreachable
)

// TaskStats samples the resource usage of every running task container and
// queues the samples for the next heartbeat (§9.1).
type TaskStats struct {
	d   *docker.Client
	log *slog.Logger

	mu      sync.Mutex
	prev    map[string]docker.Stats // container ID -> last sample, for CPU deltas
	pending []*agentv1.TaskMetrics
}

func NewTaskStats(d *docker.Client, log *slog.Logger) *TaskStats {
	return &TaskStats{d: d, log: log, prev: map[string]docker.Stats{}}
}

func (s *TaskStats) Run(ctx context.Context) {
	t := time.NewTicker(statsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sample(ctx)
		}
	}
}

// Take returns the queued samples and clears the queue.
func (s *TaskStats) Take() []*agentv1.TaskMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = nil
	return out
}

func (s *TaskStats) sample(ctx context.Context) {
	list, err := s.d.List(ctx, LabelManaged+"=true")
	if err != nil {
		return
	}
	type result struct {
		c  docker.ContainerSummary
		st docker.Stats
	}
	results := make(chan result, len(list))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	seen := map[string]bool{}
	for _, c := range list {
		if c.State != "running" || c.Labels["syncloud.service_id"] == "" {
			continue // only service tasks and job runs; not platform containers
		}
		seen[c.ID] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if st, err := s.d.Stats(cctx, c.ID); err == nil {
				results <- result{c, st}
			}
		}()
	}
	wg.Wait()
	close(results)

	s.mu.Lock()
	defer s.mu.Unlock()
	for r := range results {
		prev, ok := s.prev[r.c.ID]
		s.prev[r.c.ID] = r.st
		if !ok {
			continue // CPU usage needs two samples
		}
		m := metricsFrom(r.c.Labels, prev, r.st)
		if len(s.pending) >= maxPendingStats {
			s.pending = s.pending[1:]
		}
		s.pending = append(s.pending, m)
	}
	for id := range s.prev {
		if !seen[id] {
			delete(s.prev, id)
		}
	}
}

// metricsFrom turns two consecutive Docker samples into one TaskMetrics.
func metricsFrom(labels map[string]string, prev, cur docker.Stats) *agentv1.TaskMetrics {
	m := &agentv1.TaskMetrics{
		TaskId: labels[LabelTaskID], ServiceId: labels["syncloud.service_id"],
		Project: labels["syncloud.project"], Environment: labels["syncloud.environment"], Service: labels["syncloud.service"],
		AtUnixMs:         cur.Read.UnixMilli(),
		MemoryLimitBytes: cur.MemoryStats.Limit,
	}
	if m.AtUnixMs <= 0 {
		m.AtUnixMs = time.Now().UnixMilli()
	}
	dCPU := float64(cur.CPUStats.CPUUsage.TotalUsage) - float64(prev.CPUStats.CPUUsage.TotalUsage)
	dSys := float64(cur.CPUStats.SystemUsage) - float64(prev.CPUStats.SystemUsage)
	if dCPU > 0 && dSys > 0 {
		cpus := float64(cur.CPUStats.OnlineCPUs)
		if cpus == 0 {
			cpus = 1
		}
		m.CpuPercent = dCPU / dSys * cpus * 100
	}
	// Like `docker stats`: usage without reclaimable page cache.
	mem := cur.MemoryStats.Usage
	cache := cur.MemoryStats.Stats["inactive_file"] // cgroup v2
	if cache == 0 {
		cache = cur.MemoryStats.Stats["total_inactive_file"] // cgroup v1
	}
	if cache < mem {
		mem -= cache
	}
	m.MemoryBytes = mem
	for _, n := range cur.Networks {
		m.NetRxBytes += n.RxBytes
		m.NetTxBytes += n.TxBytes
	}
	for _, b := range cur.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(b.Op) {
		case "read":
			m.BlockReadBytes += b.Value
		case "write":
			m.BlockWriteBytes += b.Value
		}
	}
	return m
}
