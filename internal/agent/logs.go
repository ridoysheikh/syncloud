package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ridoysheikh/syncloud/internal/agent/docker"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
)

const (
	logBuffer   = 20000 // lines held while the controller is unreachable
	logPosFile  = "logpos.json"
	maxBatch    = 500
	maxBatchLen = 256 << 10
)

// LogShipper follows the output of every managed container and queues it for
// the controller (§9.2). Positions are saved so a restarted agent resumes
// where it stopped instead of re-sending or skipping lines.
type LogShipper struct {
	docker  *docker.Client
	log     *slog.Logger
	dataDir string
	out     chan *agentv1.LogLine
	dropped atomic.Uint64

	mu      sync.Mutex
	tailing map[string]bool      // container ID
	pos     map[string]time.Time // container ID -> last shipped line time
}

func NewLogShipper(d *docker.Client, dataDir string, log *slog.Logger) *LogShipper {
	s := &LogShipper{docker: d, log: log, dataDir: dataDir, out: make(chan *agentv1.LogLine, logBuffer), tailing: map[string]bool{}, pos: map[string]time.Time{}}
	if b, err := os.ReadFile(filepath.Join(dataDir, logPosFile)); err == nil {
		_ = json.Unmarshal(b, &s.pos)
	}
	return s
}

// Run tails running containers and new ones as they start.
func (s *LogShipper) Run(ctx context.Context) {
	go s.savePositions(ctx)
	for ctx.Err() == nil {
		if list, err := s.docker.List(ctx, LabelManaged+"=true"); err == nil {
			for _, c := range list {
				if c.State == "running" {
					s.follow(ctx, c.ID, c.Labels[LabelTaskID])
				}
			}
		}
		evc, errc := s.docker.Events(ctx, LabelManaged+"=true")
		for e := range evc {
			if e.Action == "start" {
				s.follow(ctx, e.Actor.ID, e.Actor.Attributes[LabelTaskID])
			}
		}
		<-errc
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *LogShipper) follow(ctx context.Context, containerID, taskID string) {
	if taskID == "" {
		return
	}
	s.mu.Lock()
	if s.tailing[containerID] {
		s.mu.Unlock()
		return
	}
	s.tailing[containerID] = true
	since := s.pos[containerID]
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.tailing, containerID)
			s.mu.Unlock()
		}()
		err := s.docker.Logs(ctx, containerID, since, func(e docker.LogEntry) {
			if !e.Time.After(since) {
				return // Docker's "since" is inclusive at second precision
			}
			l := &agentv1.LogLine{TaskId: taskID, TimeUnixNano: e.Time.UnixNano(), Stream: e.Stream, Line: e.Line}
			select {
			case s.out <- l:
			default:
				// Buffer full (controller unreachable for long): drop the oldest line.
				select {
				case <-s.out:
					s.dropped.Add(1)
				default:
				}
				select {
				case s.out <- l:
				default:
					s.dropped.Add(1)
				}
			}
			s.mu.Lock()
			s.pos[containerID] = e.Time
			s.mu.Unlock()
		})
		if err != nil && ctx.Err() == nil && !docker.IsNotFound(err) {
			s.log.Debug("log stream ended", "container", containerID[:12], "err", err)
		}
	}()
}

// savePositions writes positions every few seconds and forgets containers
// that no longer exist.
func (s *LogShipper) savePositions(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		live := map[string]bool{}
		if list, err := s.docker.List(ctx, LabelManaged+"=true"); err == nil {
			for _, c := range list {
				live[c.ID] = true
			}
		} else {
			continue
		}
		s.mu.Lock()
		for id := range s.pos {
			if !live[id] {
				delete(s.pos, id)
			}
		}
		b, _ := json.Marshal(s.pos)
		s.mu.Unlock()
		tmp := filepath.Join(s.dataDir, logPosFile+".tmp")
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, filepath.Join(s.dataDir, logPosFile))
		}
	}
}

// batch collects queued lines after first into one message.
func (s *LogShipper) batch(first *agentv1.LogLine) *agentv1.ConnectRequest {
	b := &agentv1.LogBatch{Lines: []*agentv1.LogLine{first}, Dropped: s.dropped.Swap(0)}
	size := len(first.Line)
	for len(b.Lines) < maxBatch && size < maxBatchLen {
		select {
		case l := <-s.out:
			b.Lines = append(b.Lines, l)
			size += len(l.Line)
		default:
			return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Logs{Logs: b}}
		}
	}
	return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Logs{Logs: b}}
}
