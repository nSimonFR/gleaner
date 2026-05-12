package hook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunLifecycle_Empty ensures an empty script string is a no-op
// (not an error). This is the path used when an operator omits a hook
// — gleaner shouldn't reject that configuration.
func TestRunLifecycle_Empty(t *testing.T) {
	if err := RunLifecycle(context.Background(), "before_run", "", "/tmp", nil, 5*time.Second); err != nil {
		t.Fatalf("empty script should be no-op; got %v", err)
	}
}

// TestRunLifecycle_Success runs a trivial shell command and verifies it
// returns nil. The hook is `bash -lc <script>` so inline shell works.
func TestRunLifecycle_Success(t *testing.T) {
	tmp := t.TempDir()
	if err := RunLifecycle(context.Background(), "after_create",
		"echo hi > marker.txt", tmp, nil, 5*time.Second); err != nil {
		t.Fatalf("trivial script: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "marker.txt"))
	if err != nil {
		t.Fatalf("marker.txt missing: %v", err)
	}
	if strings.TrimSpace(string(data)) != "hi" {
		t.Errorf("marker.txt = %q; want %q", strings.TrimSpace(string(data)), "hi")
	}
}

// TestRunLifecycle_NonZeroExit returns an error containing the hook name
// and the stderr from the script. Operators rely on the stderr surfaced
// here to debug why a hook denied dispatch.
func TestRunLifecycle_NonZeroExit(t *testing.T) {
	err := RunLifecycle(context.Background(), "before_run",
		"echo failreason >&2; exit 7", t.TempDir(), nil, 5*time.Second)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "before_run") {
		t.Errorf("error missing hook name: %v", err)
	}
	if !strings.Contains(err.Error(), "failreason") {
		t.Errorf("error missing stderr: %v", err)
	}
}

// TestRunLifecycle_Timeout kills a hook that exceeds its deadline and
// returns a "timeout after …" error so the operator can distinguish a
// hung hook from a hook that just exited non-zero.
func TestRunLifecycle_Timeout(t *testing.T) {
	start := time.Now()
	err := RunLifecycle(context.Background(), "after_create",
		"sleep 5", t.TempDir(), nil, 200*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention timeout: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("did not kill on deadline: elapsed=%s", elapsed)
	}
}

// TestRunLifecycle_DefaultTimeout exercises the timeout<=0 branch.
// The default (60s) is plenty for `true`, so this should pass without
// the test fixture waiting a minute.
func TestRunLifecycle_DefaultTimeout(t *testing.T) {
	err := RunLifecycle(context.Background(), "before_run",
		"true", t.TempDir(), nil, 0)
	if err != nil {
		t.Errorf("default-timeout branch should not fail trivial cmd: %v", err)
	}
}

// TestRunLifecycle_CwdAndEnv verifies cwd and env propagation — the
// SPEC §9.2 invariant that the hook sees the workspace as its cwd
// and the GLEANER_* env vars the executor exports.
func TestRunLifecycle_CwdAndEnv(t *testing.T) {
	tmp := t.TempDir()
	script := `pwd > here.txt; echo "$GLEANER_REPO" > repo.txt`
	env := []string{"GLEANER_REPO=nSimonFR/test", "GLEANER_WORKTREE=" + tmp}
	if err := RunLifecycle(context.Background(), "after_create",
		script, tmp, env, 5*time.Second); err != nil {
		t.Fatalf("hook: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "here.txt"))
	if err != nil {
		t.Fatalf("here.txt: %v", err)
	}
	// pwd may resolve symlinks (on macOS /var → /private/var, on Linux
	// the path may be canonicalized). Compare via filepath.EvalSymlinks
	// to avoid spurious test flakes.
	wantCwd, _ := filepath.EvalSymlinks(tmp)
	gotCwd := strings.TrimSpace(string(got))
	gotCwdResolved, _ := filepath.EvalSymlinks(gotCwd)
	if gotCwdResolved != wantCwd && gotCwd != tmp {
		t.Errorf("cwd = %q; want %q (or symlink-resolved %q)", gotCwd, tmp, wantCwd)
	}
	repo, err := os.ReadFile(filepath.Join(tmp, "repo.txt"))
	if err != nil {
		t.Fatalf("repo.txt: %v", err)
	}
	if strings.TrimSpace(string(repo)) != "nSimonFR/test" {
		t.Errorf("GLEANER_REPO = %q; want nSimonFR/test", strings.TrimSpace(string(repo)))
	}
}

// TestFire is a regression guard on the existing v0.0.3 event hook —
// it should be unchanged by Milestone B and continue to be best-effort
// (no error surfaced, even on script failure).
func TestFire_NoCrashOnFailure(t *testing.T) {
	tmp := t.TempDir()
	failScript := filepath.Join(tmp, "fail.sh")
	if err := os.WriteFile(failScript, []byte("#!/bin/bash\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Should not panic / not block; Fire is fire-and-forget.
	Fire(failScript, "pr_opened", map[string]any{"url": "https://x"})
}
