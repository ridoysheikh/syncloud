package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Service starts controller upgrades for the API and reports their state.
type Service struct {
	DataDir   string
	DBPath    string
	Downloads string
	Source    Source
	Channel   string
	Current   string
	Settle    time.Duration
	Snapshot  func(ctx context.Context, path string) error

	mu       sync.Mutex
	latest   string
	latestAt time.Time
	latestE  error
}

// RunInfoPath is where the running controller recorded how it was started.
func (s *Service) RunInfoPath() string { return filepath.Join(s.DataDir, RunInfoFile) }

// Latest is the newest version on the channel (cached for 10 minutes).
func (s *Service) Latest(ctx context.Context, refresh bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if refresh || time.Since(s.latestAt) > 10*time.Minute {
		s.latest, s.latestE = s.Source.Latest(ctx, s.Channel)
		s.latestAt = time.Now()
	}
	return s.latest, s.latestE
}

// State is the last upgrade (ok false when none ran).
func (s *Service) State() (State, bool) {
	st, err := ReadState(s.DataDir)
	return st, err == nil
}

// Begin upgrades to version ("" = the channel's latest).
func (s *Service) Begin(ctx context.Context, version string) (State, error) {
	if version == "" {
		v, err := s.Latest(ctx, true)
		if err != nil {
			return State{}, fmt.Errorf("release channel %s: %w", s.Channel, err)
		}
		version = v
	}
	if !ValidVersion(version) {
		return State{}, fmt.Errorf("invalid version %q", version)
	}
	ri, err := ReadRunInfo(s.RunInfoPath())
	if errors.Is(err, os.ErrNotExist) {
		return State{}, errors.New("the controller did not record how it was started; restart it once and try again")
	} else if err != nil {
		return State{}, err
	}
	// Downloads and the guard outlive the request.
	return Begin(context.WithoutCancel(ctx), Options{
		DataDir: s.DataDir, DBPath: s.DBPath, Version: version, Current: s.Current, Source: s.Source,
		HealthURL: ri.HealthURL, Downloads: s.Downloads, Snapshot: s.Snapshot, Run: ri, Settle: s.Settle,
	})
}

// Start checks what can be checked at once and runs Begin in the background
// (downloads can take a while); progress is in State.
func (s *Service) Start(ctx context.Context, version string) error {
	if last, ok := s.State(); ok && !Terminal(last.Phase) && time.Since(last.UpdatedAt) < 15*time.Minute {
		return fmt.Errorf("an upgrade to %s is already in progress (%s)", last.To, last.Phase)
	}
	if version == "" {
		v, err := s.Latest(ctx, true)
		if err != nil {
			return fmt.Errorf("release channel %s: %w", s.Channel, err)
		}
		version = v
	}
	if version == s.Current {
		return fmt.Errorf("already running %s", s.Current)
	}
	if !ValidVersion(version) {
		return fmt.Errorf("invalid version %q", version)
	}
	if _, err := os.Stat(s.RunInfoPath()); err != nil {
		return errors.New("the controller did not record how it was started; restart it once and try again")
	}
	go func() { _, _ = s.Begin(context.WithoutCancel(ctx), version) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := s.State(); ok && st.To == version && time.Since(st.StartedAt) < time.Minute {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
