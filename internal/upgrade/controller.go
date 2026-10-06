package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Phases of a controller upgrade.
const (
	PhasePreparing   = "preparing"
	PhaseStopping    = "stopping"
	PhaseStarting    = "starting"
	PhaseVerifying   = "verifying"
	PhaseDone        = "done"
	PhaseRollingBack = "rolling-back"
	PhaseRolledBack  = "rolled-back"
	PhaseFailed      = "failed" // rollback did not bring the old controller back
)

// Terminal reports whether no guard works on a state in phase p.
func Terminal(p string) bool {
	return p == PhaseDone || p == PhaseRolledBack || p == PhaseFailed || p == ""
}

// RunInfoFile is where the controller records how it was started.
const RunInfoFile = "controller-run.json"

// Step is one line of an upgrade's progress.
type Step struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// State of the current (or last) controller upgrade, in <data>/upgrade/state.json.
type State struct {
	ID         string     `json:"id"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	Phase      string     `json:"phase"`
	Message    string     `json:"message,omitempty"`
	Steps      []Step     `json:"steps"`
	StartedAt  time.Time  `json:"startedAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	Dir       string  `json:"dir"`
	Exe       string  `json:"exe"`
	Prev      string  `json:"prev"`
	New       string  `json:"new"`
	Snapshot  string  `json:"snapshot"`
	DBPath    string  `json:"dbPath"`
	HealthURL string  `json:"healthUrl"`
	Downloads string  `json:"downloads,omitempty"`
	Files     Files   `json:"files"`
	Run       RunInfo `json:"run"`
	Settle    int     `json:"settleSeconds"`
	Timeout   int     `json:"timeoutSeconds"`
}

func stateFile(dataDir string) string { return filepath.Join(dataDir, "upgrade", "state.json") }

// ReadState returns the last upgrade's state (os.ErrNotExist when none ran).
func ReadState(dataDir string) (State, error) {
	var s State
	err := readJSON(stateFile(dataDir), &s)
	return s, err
}

func (s *State) save(dataDir string) error {
	s.UpdatedAt = time.Now().UTC()
	return writeJSON(stateFile(dataDir), s)
}

func (s *State) step(dataDir, phase, format string, args ...any) {
	if phase != "" {
		s.Phase = phase
	}
	s.Steps = append(s.Steps, Step{At: time.Now().UTC(), Text: fmt.Sprintf(format, args...)})
	if err := s.save(dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "upgrade: save state:", err)
	}
	fmt.Fprintf(os.Stderr, "%s upgrade %s: %s\n", time.Now().UTC().Format(time.RFC3339), s.Phase, s.Steps[len(s.Steps)-1].Text)
}

// Options of a controller upgrade.
type Options struct {
	DataDir   string
	DBPath    string
	Version   string // target
	Current   string // running version
	Source    Source
	HealthURL string
	Downloads string // agents and synctl served to workers ("" = none)
	// Snapshot writes a consistent copy of the database to path.
	Snapshot func(ctx context.Context, path string) error
	// Run is how the running controller was started.
	Run     RunInfo
	Settle  time.Duration // healthy for this long before it counts (default 2m)
	Timeout time.Duration // to become healthy (default 5m)
}

// Begin prepares an upgrade (download, verify, snapshot, keep the current
// binary) and hands it to a detached guard, which restarts the controller.
func Begin(ctx context.Context, o Options) (State, error) {
	if last, err := ReadState(o.DataDir); err == nil && !Terminal(last.Phase) && time.Since(last.UpdatedAt) < 15*time.Minute {
		return last, fmt.Errorf("an upgrade to %s is already in progress (%s)", last.To, last.Phase)
	}
	if o.Version == o.Current {
		return State{}, fmt.Errorf("already running %s", o.Current)
	}
	if o.Settle <= 0 {
		o.Settle = 2 * time.Minute
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	now := time.Now().UTC()
	id := now.Format("20060102-150405")
	dir := filepath.Join(o.DataDir, "upgrade", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return State{}, err
	}
	s := State{ID: id, From: o.Current, To: o.Version, StartedAt: now, Dir: dir, Exe: o.Run.Exe, DBPath: o.DBPath,
		HealthURL: o.HealthURL, Downloads: o.Downloads, Run: o.Run, Settle: int(o.Settle.Seconds()), Timeout: int(o.Timeout.Seconds()),
		Prev: filepath.Join(dir, "syncloud-controller.prev"), Snapshot: filepath.Join(dir, "syncloud.db")}
	fail := func(err error) (State, error) {
		t := time.Now().UTC()
		s.FinishedAt, s.Message = &t, err.Error()
		s.step(o.DataDir, PhaseFailed, "not started: %v", err)
		return s, err
	}
	s.step(o.DataDir, PhasePreparing, "upgrade %s → %s", o.Current, o.Version)

	files, err := o.Source.Fetch(ctx, o.Version, filepath.Join(dir, "release"))
	if err != nil {
		return fail(err)
	}
	s.Files = files
	for name, p := range files {
		if strings.HasPrefix(name, "syncloud-controller-") {
			s.New = p
		}
	}
	s.step(o.DataDir, "", "downloaded and verified %d files", len(files))
	if got, err := binaryVersion(ctx, s.New); err != nil {
		return fail(fmt.Errorf("new controller does not run: %w", err))
	} else if got != o.Version {
		return fail(fmt.Errorf("new controller reports version %q, not %q", got, o.Version))
	}
	if err := checkDisk(o.DataDir, o.DBPath, o.Run.Exe); err != nil {
		return fail(err)
	}
	if err := copyFile(o.Run.Exe, s.Prev, 0o755); err != nil {
		return fail(fmt.Errorf("keep current binary: %w", err))
	}
	if err := o.Snapshot(ctx, s.Snapshot); err != nil {
		return fail(fmt.Errorf("database snapshot: %w", err))
	}
	s.step(o.DataDir, "", "database snapshot and current binary kept in %s", dir)
	argv := []string{s.Prev, "upgrade-guard", "--data-dir", o.DataDir}
	if err := Spawn("syncloud-upgrade-"+id, filepath.Join(dir, "guard.log"), o.Run.Unit != "", argv...); err != nil {
		return fail(fmt.Errorf("start guard: %w", err))
	}
	s.step(o.DataDir, PhaseStopping, "guard started; restarting the controller")
	return s, nil
}

func binaryVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", errors.New("no version output")
	}
	return f[0], nil
}

func checkDisk(dataDir string, paths ...string) error {
	var need uint64
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			need += uint64(fi.Size())
		}
	}
	need = need*3 + 64<<20
	var st syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &st); err != nil {
		return nil
	}
	if free := st.Bavail * uint64(st.Bsize); free < need {
		return fmt.Errorf("not enough disk space in %s: %d MiB free, %d MiB needed", dataDir, free>>20, need>>20)
	}
	return nil
}

// Health is what /api/v1/system/health returns.
type Health struct {
	Version  string   `json:"version"`
	Ready    bool     `json:"ready"`
	Problems []string `json:"problems"`
}

// CheckHealth asks a controller whether it is ready.
func CheckHealth(ctx context.Context, url string) (Health, error) {
	var h Health
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return h, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err := json.Unmarshal(b, &h); err != nil {
		return h, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !h.Ready {
		return h, fmt.Errorf("not ready: %s", strings.Join(h.Problems, "; "))
	}
	return h, nil
}

// waitHealthy waits until the controller at url reports version want and
// ready, and stays so for settle.
func waitHealthy(ctx context.Context, url, want string, pid func() int, settle, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var since time.Time
	var last error
	for {
		h, err := CheckHealth(ctx, url)
		if err == nil && h.Version != want {
			err = fmt.Errorf("version %s is answering, not %s", h.Version, want)
		}
		switch {
		case err == nil && since.IsZero():
			since = time.Now()
		case err == nil && time.Since(since) >= settle:
			return nil
		case err != nil && !since.IsZero():
			return fmt.Errorf("became unhealthy: %w", err)
		case err != nil:
			last = err
		}
		if p := pid(); p > 0 && !alive(p) {
			return errors.New("the controller exited")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy within %s: %v", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Guard runs the upgrade prepared by Begin: stop, swap, start, verify, and
// roll back when the new controller is not healthy.
func Guard(ctx context.Context, dataDir string) error {
	s, err := ReadState(dataDir)
	if err != nil {
		return err
	}
	if Terminal(s.Phase) {
		return fmt.Errorf("no upgrade in progress (%s)", s.Phase)
	}
	ri := s.Run
	pid := func() int {
		if ri.Unit != "" {
			return 0
		}
		return ri.PID
	}
	finish := func(phase, msg string) error {
		t := time.Now().UTC()
		s.FinishedAt, s.Message = &t, msg
		s.step(dataDir, phase, "%s", msg)
		if phase != PhaseDone {
			return errors.New(msg)
		}
		return nil
	}

	s.step(dataDir, PhaseStopping, "stopping controller %s", s.From)
	if err := Stop(ri, time.Minute); err != nil {
		return finish(PhaseFailed, "could not stop the controller: "+err.Error())
	}
	if err := Install(s.New, s.Exe); err != nil {
		reason := "install the new binary: " + err.Error()
		if p, err := Start(ri, filepath.Join(s.Dir, "controller.log")); err == nil {
			ri.PID = p
		}
		return finish(PhaseRolledBack, reason)
	}
	s.step(dataDir, PhaseStarting, "starting controller %s", s.To)
	p, err := Start(ri, filepath.Join(s.Dir, "controller.log"))
	ri.PID = p
	s.Run.PID = p
	if err == nil {
		s.step(dataDir, PhaseVerifying, "waiting for %s to be healthy for %ds", s.To, s.Settle)
		err = waitHealthy(ctx, s.HealthURL, s.To, pid, time.Duration(s.Settle)*time.Second, time.Duration(s.Timeout)*time.Second)
	}
	if err == nil {
		if err := installDownloads(s.Downloads, s.Files); err != nil {
			s.step(dataDir, "", "worker downloads not updated: %v", err)
		}
		return finish(PhaseDone, fmt.Sprintf("upgraded to %s", s.To))
	}

	reason := err.Error()
	s.step(dataDir, PhaseRollingBack, "%s; rolling back to %s", reason, s.From)
	_ = Stop(ri, 30*time.Second)
	if err := Install(s.Prev, s.Exe); err != nil {
		return finish(PhaseFailed, reason+"; restoring the binary failed: "+err.Error())
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(s.DBPath + suffix)
	}
	if err := copyFile(s.Snapshot, s.DBPath, 0o600); err != nil {
		return finish(PhaseFailed, reason+"; restoring the database failed: "+err.Error())
	}
	p, err = Start(ri, filepath.Join(s.Dir, "controller.log"))
	ri.PID = p
	s.Run.PID = p
	if err == nil {
		err = waitHealthy(ctx, s.HealthURL, s.From, pid, 5*time.Second, time.Duration(s.Timeout)*time.Second)
	}
	if err != nil {
		return finish(PhaseFailed, reason+"; the restored controller is not healthy: "+err.Error())
	}
	return finish(PhaseRolledBack, "rolled back to "+s.From+": "+reason)
}

// installDownloads puts a release's agent and synctl binaries where workers
// download them, and rewrites SHA256SUMS.
func installDownloads(dir string, files Files) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, p := range files {
		if strings.HasPrefix(name, "syncloud-controller-") {
			continue
		}
		if err := copyFile(p, filepath.Join(dir, name), 0o755); err != nil {
			return err
		}
	}
	return WriteSums(dir)
}

// WriteSums writes SHA256SUMS for every agent and synctl binary in dir.
func WriteSums(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var lines []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !(strings.HasPrefix(n, "syncloud-agent-linux-") || strings.HasPrefix(n, "synctl-linux-")) {
			continue
		}
		sum, err := FileSHA256(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		lines = append(lines, sum+"  "+n)
	}
	sort.Strings(lines)
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
