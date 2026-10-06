// Package upgrade replaces the controller and agent binaries in place with
// automatic rollback (§5.0.1). The process that is replaced cannot watch its
// own replacement, so a detached guard (a copy of the old binary) stops it,
// swaps the binary, starts the new one, checks it and restores the old binary
// (and, for the controller, the database snapshot) when it is not healthy.
package upgrade

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RunInfo is how a running controller or agent was started, written at start
// so a guard can stop and start it again the same way.
type RunInfo struct {
	PID  int      `json:"pid"`
	Exe  string   `json:"exe"`
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Dir  string   `json:"dir"`
	// Stdout is the file standard output and error went to ("" if not a file).
	Stdout string `json:"stdout,omitempty"`
	// Unit is the systemd unit running the process ("" when not under systemd).
	Unit string `json:"unit,omitempty"`
	// HealthURL is the controller's /api/v1/system/health (controllers only).
	HealthURL string `json:"healthUrl,omitempty"`
}

// CurrentRunInfo describes this process.
func CurrentRunInfo() (RunInfo, error) {
	exe, err := os.Executable()
	if err != nil {
		return RunInfo{}, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir, _ := os.Getwd()
	ri := RunInfo{PID: os.Getpid(), Exe: exe, Argv: os.Args, Env: os.Environ(), Dir: dir, Unit: systemdUnit()}
	if out, err := os.Readlink("/proc/self/fd/1"); err == nil && filepath.IsAbs(out) && !strings.HasPrefix(out, "/dev/") {
		ri.Stdout = out
	}
	return ri, nil
}

// systemdUnit is the .service this process runs in, from its cgroup.
func systemdUnit() string {
	if os.Getenv("INVOCATION_ID") == "" {
		return ""
	}
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, part := range strings.Split(sc.Text(), "/") {
			if strings.HasSuffix(part, ".service") {
				return part
			}
		}
	}
	return ""
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// WriteRunInfo records this process in path.
func WriteRunInfo(path, healthURL string) error {
	ri, err := CurrentRunInfo()
	if err != nil {
		return err
	}
	ri.HealthURL = healthURL
	return writeJSON(path, ri)
}

// ReadRunInfo reads a RunInfo written by WriteRunInfo.
func ReadRunInfo(path string) (RunInfo, error) {
	var ri RunInfo
	err := readJSON(path, &ri)
	return ri, err
}

// alive reports whether pid runs (zombies count as gone).
func alive(pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] != 'Z'
	}
	return true
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stop stops the process ri describes and waits for it to exit.
func Stop(ri RunInfo, timeout time.Duration) error {
	if ri.Unit != "" {
		return systemctl("stop", ri.Unit)
	}
	if !alive(ri.PID) {
		return nil
	}
	_ = syscall.Kill(ri.PID, syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for alive(ri.PID) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(ri.PID, syscall.SIGKILL)
			time.Sleep(time.Second)
			if alive(ri.PID) {
				return fmt.Errorf("process %d did not stop", ri.PID)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// Start starts the process ri describes again (detached) and returns its PID.
func Start(ri RunInfo, fallbackLog string) (int, error) {
	if ri.Unit != "" {
		return 0, systemctl("start", ri.Unit)
	}
	logPath := ri.Stdout
	if logPath == "" {
		logPath = fallbackLog
	}
	out, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	cmd := exec.Command(ri.Exe, ri.Argv[1:]...)
	cmd.Args[0] = ri.Argv[0]
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = ri.Env, ri.Dir, out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }() // reap it if the guard outlives it
	return pid, nil
}

// Spawn starts a detached helper (the guard) that survives the caller: as a
// transient systemd unit when the caller runs under systemd (so stopping the
// caller's unit does not kill it), else in its own session.
func Spawn(name, logPath string, underSystemd bool, argv ...string) error {
	if underSystemd {
		args := append([]string{"--unit=" + name, "--collect", "--quiet", "--property=StandardOutput=append:" + logPath,
			"--property=StandardError=append:" + logPath}, argv...)
		out, err := exec.Command("systemd-run", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// copyFile copies src to dst (mode applied), via a temporary file and rename.
func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".syncloud-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Install replaces dst with src atomically (rename over a running binary is
// safe on Linux: the old inode lives until the process exits).
func Install(src, dst string) error {
	if err := copyFile(src, dst, 0o755); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot replace %s: %w", dst, err)
		}
		return err
	}
	return nil
}
