package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/config"
)

// TestRunInWorkspace_Stall: a profile that sleeps without output past
// stall_timeout → SIGKILL'd by the watcher; runInWorkspace returns
// ErrStalled.
func TestRunInWorkspace_Stall(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", "sleep 30"}, // 30s sleep, no output
		Cwd:     wt,
		Timeout: 60 * time.Second, // overall timeout > stall — stall should fire first
	}
	start := time.Now()
	_, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{
		StallTimeout: 300 * time.Millisecond,
	}, &Result{})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrStalled) {
		t.Fatalf("err = %v; want ErrStalled", err)
	}
	// Watcher polls every stall/4 = 75ms; expect kill within ~2x stall.
	if elapsed > 2*time.Second {
		t.Errorf("stall kill took %s; should be near 300ms", elapsed)
	}
}

// TestRunInWorkspace_NoStallWhenChatty: a profile that emits a line
// each tick keeps the watcher fed; no stall fires.
func TestRunInWorkspace_NoStallWhenChatty(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	// 5 iterations × 100ms each = ~500ms total; emits every 100ms.
	// Stall timeout 300ms — should NEVER trigger because there's a
	// write every 100ms.
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", "for i in 1 2 3 4 5; do echo $i; sleep 0.1; done"},
		Cwd:     wt,
		Timeout: 10 * time.Second,
	}
	res, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{
		StallTimeout: 300 * time.Millisecond,
	}, &Result{})
	if err != nil {
		t.Fatalf("chatty profile should not stall: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exitCode = %d; want 0", res.ExitCode)
	}
}

// TestRunInWorkspace_StallTimeoutZeroDisables: opts.StallTimeout <= 0
// disables stall detection (SPEC §5.3.6 — "≤0 disables stall
// detection").
func TestRunInWorkspace_StallTimeoutZeroDisables(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", "sleep 0.5"}, // 500ms sleep, no output
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}
	res, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{
		StallTimeout: 0, // disabled
	}, &Result{})
	if err != nil {
		t.Fatalf("StallTimeout=0 must not kill the child: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d", res.ExitCode)
	}
}
