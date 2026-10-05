package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"syncloud/internal/agent/docker"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

const testImage = "busybox:1.37"

// dockerOrSkip returns a client for the local Docker Engine, or skips.
func dockerOrSkip(t *testing.T) *docker.Client {
	t.Helper()
	if os.Getenv("SYNCLOUD_SKIP_DOCKER_TESTS") != "" {
		t.Skip("SYNCLOUD_SKIP_DOCKER_TESTS set")
	}
	if _, err := os.Stat(docker.DefaultSocket); err != nil {
		t.Skip("no Docker socket")
	}
	d := docker.New(docker.DefaultSocket)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := d.Version(ctx); err != nil {
		t.Skipf("Docker not reachable: %v", err)
	}
	return d
}

// waitStatus reads runner statuses until one for taskID reaches want.
func waitStatus(t *testing.T, r *Runner, taskID string, want agentv1.TaskState) *agentv1.TaskStatus {
	t.Helper()
	return waitStatusWhere(t, r, taskID, want, func(*agentv1.TaskStatus) bool { return true })
}

// waitStatusWhere also requires ok(status); earlier statuses (e.g. Docker
// events for a replaced container) are skipped.
func waitStatusWhere(t *testing.T, r *Runner, taskID string, want agentv1.TaskState, ok func(*agentv1.TaskStatus) bool) *agentv1.TaskStatus {
	t.Helper()
	deadline := time.After(90 * time.Second)
	for {
		select {
		case s := <-r.out:
			if s.TaskId == taskID && s.State == want && ok(s) {
				return s
			}
			if s.TaskId == taskID && s.State == agentv1.TaskState_TASK_STATE_FAILED {
				t.Fatalf("task failed: %s", s.Error)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s to be %s", taskID, want)
		}
	}
}

func TestRunnerLifecycle(t *testing.T) {
	d := dockerOrSkip(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRunner(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go r.Watch(ctx)

	taskID := "test-" + time.Now().Format("150405.000000")
	spec := &agentv1.TaskSpec{
		TaskId: taskID, Name: "syncloud-" + taskID, Image: testImage,
		Command: []string{"sleep", "300"}, Env: map[string]string{"A": "1"},
		Restart: agentv1.RestartPolicy_RESTART_POLICY_NO,
	}
	t.Cleanup(func() { r.Stop(context.Background(), taskID, time.Second, true) })

	r.Run(ctx, spec)
	first := waitStatus(t, r, taskID, agentv1.TaskState_TASK_STATE_RUNNING)

	// Same spec: the container is kept.
	r.Run(ctx, spec)
	again := waitStatus(t, r, taskID, agentv1.TaskState_TASK_STATE_RUNNING)
	if again.ContainerId != first.ContainerId {
		t.Fatalf("unchanged spec recreated the container: %s -> %s", first.ContainerId, again.ContainerId)
	}

	// Changed spec: the container is replaced.
	spec.Env["A"] = "2"
	r.Run(ctx, spec)
	newHash := SpecHash(spec)
	replaced := waitStatusWhere(t, r, taskID, agentv1.TaskState_TASK_STATE_RUNNING,
		func(s *agentv1.TaskStatus) bool { return s.SpecHash == newHash })
	if replaced.ContainerId == first.ContainerId {
		t.Fatal("changed spec did not replace the container")
	}

	// It shows up in the snapshot sent on reconnect.
	found := false
	for _, s := range r.Snapshot(ctx) {
		if s.TaskId == taskID && s.State == agentv1.TaskState_TASK_STATE_RUNNING {
			found = true
		}
	}
	if !found {
		t.Fatal("running task missing from snapshot")
	}

	r.Stop(ctx, taskID, time.Second, true)
	waitStatus(t, r, taskID, agentv1.TaskState_TASK_STATE_REMOVED)
	if list, _ := d.List(ctx, LabelTaskID+"="+taskID); len(list) != 0 {
		t.Fatalf("%d containers left after remove", len(list))
	}
}

func TestRunnerReportsBadImage(t *testing.T) {
	d := dockerOrSkip(t)
	r := NewRunner(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	taskID := "test-bad-" + time.Now().Format("150405.000000")
	go r.Run(context.Background(), &agentv1.TaskSpec{TaskId: taskID, Name: "syncloud-" + taskID, Image: "syncloud.invalid/nope:1"})
	for {
		select {
		case s := <-r.out:
			if s.State == agentv1.TaskState_TASK_STATE_FAILED {
				if s.Error == "" {
					t.Fatal("failure without an error message")
				}
				return
			}
		case <-time.After(60 * time.Second):
			t.Fatal("no failure reported for an unpullable image")
		}
	}
}
