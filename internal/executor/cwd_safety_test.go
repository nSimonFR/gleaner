package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/config"
)

// TestRunInWorkspace_CwdEscapeRejected verifies the SPEC §9.5 cwd
// safety invariant: a profile that points cwd outside the workspace
// must NOT execute the agent. Otherwise an attacker-supplied profile
// (or a typo like `cwd: /etc`) would run with the orchestrator's
// privileges in an unrelated dir.
func TestRunInWorkspace_CwdEscapeRejected(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}

	prof := &config.Profile{
		Name: "test",
		// touch a marker file in /tmp; if cwd were honored, this file
		// would exist after the run. We assert it does NOT.
		Run:     []string{"sh", "-c", ": > " + filepath.Join(root, "should_not_exist")},
		Cwd:     "/etc", // ESCAPE: not inside wt
		Timeout: 5 * time.Second,
	}
	_, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{}, &Result{})
	if err == nil {
		t.Fatal("expected runInWorkspace to refuse cwd outside workspace")
	}
	if !strings.Contains(err.Error(), "escapes workspace") {
		t.Errorf("error should mention escape; got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "should_not_exist")); statErr == nil {
		t.Error("agent command should NOT have run; the marker file exists")
	}
}

// TestRunInWorkspace_CwdInsideAllowed: cwd EQUAL to wt is fine.
func TestRunInWorkspace_CwdInsideAllowed(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", ": > marker"},
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}
	res, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{}, &Result{})
	if err != nil {
		t.Fatalf("cwd==wt should be allowed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exitCode = %d", res.ExitCode)
	}
}

// TestRunInWorkspace_CwdSubdirAllowed: cwd inside wt is fine (some
// profiles want to run inside a subdirectory of the worktree).
func TestRunInWorkspace_CwdSubdirAllowed(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	sub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", "true"},
		Cwd:     sub,
		Timeout: 5 * time.Second,
	}
	_, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{}, &Result{})
	if err != nil {
		t.Errorf("cwd inside wt should be allowed: %v", err)
	}
}
