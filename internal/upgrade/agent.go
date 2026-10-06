package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// AgentReconnectTimeout is how long a new agent has to reconnect before the
// guard restores the old one.
var AgentReconnectTimeout = 2 * time.Minute

// AgentPending is written before an agent replaces itself and removed by the
// new agent once the controller accepts it.
type AgentPending struct {
	Version  string    `json:"version"`
	From     string    `json:"from"`
	Exe      string    `json:"exe"`
	Prev     string    `json:"prev"`
	Deadline time.Time `json:"deadline"`
	Run      RunInfo   `json:"run"`
}

// AgentResult is left for the restored agent to report.
type AgentResult struct {
	Version string `json:"version"`
	Error   string `json:"error"`
}

func agentDir(dataDir string) string         { return filepath.Join(dataDir, "upgrade") }
func agentPendingFile(dataDir string) string { return filepath.Join(agentDir(dataDir), "pending.json") }
func agentResultFile(dataDir string) string  { return filepath.Join(agentDir(dataDir), "result.json") }

// AgentStagingFile is where an incoming agent binary is written.
func AgentStagingFile(dataDir string) string {
	return filepath.Join(agentDir(dataDir), "syncloud-agent.new")
}

// ReadAgentPending returns the pending upgrade, if any.
func ReadAgentPending(dataDir string) (AgentPending, bool) {
	var p AgentPending
	if err := readJSON(agentPendingFile(dataDir), &p); err != nil {
		return p, false
	}
	return p, true
}

// AgentConnected is called by an agent once the controller accepted it: a
// pending upgrade to this version succeeded. It returns a result to report
// (a rolled-back upgrade), if any.
func AgentConnected(dataDir, running string) *AgentResult {
	if p, ok := ReadAgentPending(dataDir); ok && p.Version == running {
		_ = os.Remove(agentPendingFile(dataDir))
	}
	var r AgentResult
	if err := readJSON(agentResultFile(dataDir), &r); err != nil {
		return nil
	}
	_ = os.Remove(agentResultFile(dataDir))
	return &r
}

// BeginAgent replaces the running agent's binary with staged (already
// verified) after starting a guard; the caller then re-executes itself.
func BeginAgent(dataDir, staged, version, from string) (RunInfo, error) {
	ri, err := CurrentRunInfo()
	if err != nil {
		return ri, err
	}
	if err := os.MkdirAll(agentDir(dataDir), 0o700); err != nil {
		return ri, err
	}
	p := AgentPending{Version: version, From: from, Exe: ri.Exe, Prev: filepath.Join(agentDir(dataDir), "syncloud-agent.prev"),
		Deadline: time.Now().Add(AgentReconnectTimeout).UTC(), Run: ri}
	if err := copyFile(ri.Exe, p.Prev, 0o755); err != nil {
		return ri, fmt.Errorf("keep current binary: %w", err)
	}
	if err := writeJSON(agentPendingFile(dataDir), p); err != nil {
		return ri, err
	}
	argv := []string{p.Prev, "upgrade-guard", "--data-dir", dataDir}
	if err := Spawn(fmt.Sprintf("syncloud-agent-upgrade-%d", time.Now().Unix()), filepath.Join(agentDir(dataDir), "guard.log"), ri.Unit != "", argv...); err != nil {
		_ = os.Remove(agentPendingFile(dataDir))
		return ri, fmt.Errorf("start guard: %w", err)
	}
	if err := Install(staged, ri.Exe); err != nil {
		_ = os.Remove(agentPendingFile(dataDir))
		return ri, err
	}
	return ri, nil
}

// Reexec replaces this process with the (new) binary, keeping its PID,
// arguments and environment. Containers keep running meanwhile.
func Reexec(ri RunInfo) error {
	return syscall.Exec(ri.Exe, ri.Argv, ri.Env)
}

// AgentGuard waits for the new agent to reconnect (it removes the pending
// file) and otherwise restores and restarts the old one.
func AgentGuard(ctx context.Context, dataDir string) error {
	p, ok := ReadAgentPending(dataDir)
	if !ok {
		return errors.New("no agent upgrade pending")
	}
	for {
		if _, ok := ReadAgentPending(dataDir); !ok {
			fmt.Fprintf(os.Stderr, "%s agent upgrade to %s confirmed\n", time.Now().UTC().Format(time.RFC3339), p.Version)
			return nil
		}
		reason := ""
		if p.Run.Unit == "" && !alive(p.Run.PID) {
			reason = "the new agent exited"
		} else if time.Now().After(p.Deadline) {
			reason = fmt.Sprintf("the new agent did not reconnect within %s", AgentReconnectTimeout)
		}
		if reason != "" {
			return rollbackAgent(dataDir, p, reason)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func rollbackAgent(dataDir string, p AgentPending, reason string) error {
	fmt.Fprintf(os.Stderr, "%s agent upgrade to %s: %s; rolling back to %s\n", time.Now().UTC().Format(time.RFC3339), p.Version, reason, p.From)
	_ = Stop(p.Run, 30*time.Second)
	if err := Install(p.Prev, p.Exe); err != nil {
		return fmt.Errorf("restore %s: %w", p.Exe, err)
	}
	_ = os.Remove(agentPendingFile(dataDir))
	_ = writeJSON(agentResultFile(dataDir), AgentResult{Version: p.Version, Error: reason + "; rolled back to " + p.From})
	_, err := Start(p.Run, filepath.Join(agentDir(dataDir), "agent.log"))
	return err
}
