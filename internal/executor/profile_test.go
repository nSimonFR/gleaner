// Integration tests for the executor's hook lifecycle, driven via
// runInWorkspace so we don't need a real git source repo on disk.
// Each test:
//   - creates a temp workspace dir
//   - configures hooks as inline shell that touches marker files
//   - invokes runInWorkspace with a controlled agent command
//   - asserts the marker file set + the returned error sentinel
package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
)

func testIssue() *tracker.Issue {
	return &tracker.Issue{
		ID:         "github:nSimonFR/test#1",
		Identifier: "nSimonFR/test#1",
		Repo:       "nSimonFR/test",
		Number:     1,
		Title:      "test issue",
		Body:       "body",
	}
}

func touchHook(marker string) string {
	// `: > foo` is portable: truncate-or-create the file.
	return ": > " + marker
}

// markers reads the basenames of any file present in `dir` so callers
// can compare "which hooks fired" via set semantics.
func markers(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// TestRunInWorkspace_HappyPath: all 4 hooks fire in order, agent
// succeeds, and all 5 marker files end up in the parent root (the
// workspace itself is removed by the deferred cleanup, so markers
// must be written to a sibling dir that survives).
func TestRunInWorkspace_HappyPath(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := config.Hooks{
		AfterCreate:  touchHook(filepath.Join(root, "01-after_create")),
		BeforeRun:    touchHook(filepath.Join(root, "02-before_run")),
		AfterRun:     touchHook(filepath.Join(root, "03-after_run")),
		BeforeRemove: touchHook(filepath.Join(root, "04-before_remove")),
		Timeout:      5 * time.Second,
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", ": > " + filepath.Join(root, "05-agent")},
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}
	res, err := runInWorkspace(context.Background(), prof, testIssue(), wt, true, RunOpts{Hooks: hooks}, &Result{})
	if err != nil {
		t.Fatalf("runInWorkspace: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exitCode = %d; want 0", res.ExitCode)
	}
	got := markers(t, root)
	for _, want := range []string{"01-after_create", "02-before_run", "03-after_run", "04-before_remove", "05-agent"} {
		if !got[want] {
			t.Errorf("expected marker %q to be present (got %v)", want, got)
		}
	}
	// Workspace itself should be removed by the deferred before_remove
	// + cleanup since cleanup=true.
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("workspace %s should be removed; stat err=%v", wt, err)
	}
}

// TestRunInWorkspace_AfterCreateFatal: after_create exits 1 →
// ErrAfterCreateFailed; workspace is destroyed via before_remove +
// RemoveAll; before_run and after_run do NOT fire; the agent command
// never runs.
func TestRunInWorkspace_AfterCreateFatal(t *testing.T) {
	root := t.TempDir() // parent we control; workspace below it
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	// Markers go in `root` so they survive workspace teardown.
	hooks := config.Hooks{
		AfterCreate:  ": > " + filepath.Join(root, "01-after_create-fired") + "; exit 1",
		BeforeRun:    ": > " + filepath.Join(root, "02-before_run-fired"),
		AfterRun:     ": > " + filepath.Join(root, "03-after_run-fired"),
		BeforeRemove: ": > " + filepath.Join(root, "04-before_remove-fired"),
		Timeout:      5 * time.Second,
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", ": > " + filepath.Join(root, "agent-fired")},
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}
	res, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{Hooks: hooks}, &Result{})
	if !errors.Is(err, ErrAfterCreateFailed) {
		t.Fatalf("err = %v; want ErrAfterCreateFailed", err)
	}
	if res.Error == nil {
		t.Error("res.Error should be populated")
	}
	got := markers(t, root)
	if !got["01-after_create-fired"] {
		t.Error("after_create should have run")
	}
	// before_remove MUST fire even on after_create failure (Elixir
	// Symphony semantics; matches Workspace.remove always firing before
	// deletion). The workspace gets torn down regardless.
	if !got["04-before_remove-fired"] {
		t.Error("before_remove should fire when after_create fails")
	}
	// The other two and the agent must NOT have run.
	for _, k := range []string{"02-before_run-fired", "03-after_run-fired", "agent-fired"} {
		if got[k] {
			t.Errorf("hook %q should not have run after after_create failed", k)
		}
	}
	// Workspace itself should be gone.
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("workspace %s should be removed; stat err=%v", wt, err)
	}
}

// TestRunInWorkspace_BeforeRunDenial: before_run exits 1 →
// ErrBeforeRunDenied; the agent never runs; after_run does NOT fire
// (the run "did not happen"). before_remove fires per cleanup=true.
func TestRunInWorkspace_BeforeRunDenial(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := config.Hooks{
		BeforeRun:    "exit 1",
		AfterRun:     ": > " + filepath.Join(root, "after_run-fired"),
		BeforeRemove: ": > " + filepath.Join(root, "before_remove-fired"),
		Timeout:      5 * time.Second,
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", ": > " + filepath.Join(root, "agent-fired")},
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}
	_, err := runInWorkspace(context.Background(), prof, testIssue(), wt, true, RunOpts{Hooks: hooks}, &Result{})
	if !errors.Is(err, ErrBeforeRunDenied) {
		t.Fatalf("err = %v; want ErrBeforeRunDenied", err)
	}
	got := markers(t, root)
	if got["agent-fired"] {
		t.Error("agent command must NOT have run after before_run denied")
	}
	if got["after_run-fired"] {
		t.Error("after_run must NOT fire when before_run denied (run did not happen)")
	}
	if !got["before_remove-fired"] {
		t.Error("before_remove should fire when cleanup=true")
	}
}

// TestRunInWorkspace_AgentFailsAfterRunStillFires: agent exits 1 →
// runErr propagated, BUT after_run STILL fires (SPEC §9.4 "after each
// attempt, any outcome"). after_run receives GLEANER_EXIT_CODE so the
// hook can act on the failure.
func TestRunInWorkspace_AgentFailsAfterRunStillFires(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	exitCodeRecord := filepath.Join(root, "exit_code")
	hooks := config.Hooks{
		AfterRun: "echo $GLEANER_EXIT_CODE > " + exitCodeRecord,
		Timeout:  5 * time.Second,
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", "exit 7"},
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}
	res, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{Hooks: hooks}, &Result{})
	if err == nil {
		t.Fatal("expected agent exit-1 error")
	}
	if errors.Is(err, ErrBeforeRunDenied) || errors.Is(err, ErrAfterCreateFailed) {
		t.Errorf("agent failure must not be classified as hook sentinel: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("res.ExitCode = %d; want 7", res.ExitCode)
	}
	data, err := os.ReadFile(exitCodeRecord)
	if err != nil {
		t.Fatalf("after_run did not write exit code: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "7" {
		t.Errorf("GLEANER_EXIT_CODE = %q; want 7", got)
	}
}

// TestRunInWorkspace_NoHooks confirms an empty Hooks{} skips everything
// gracefully — the agent command still runs normally.
func TestRunInWorkspace_NoHooks(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", ": > " + filepath.Join(root, "agent")},
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}
	res, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{}, &Result{})
	if err != nil {
		t.Fatalf("runInWorkspace with empty hooks: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exitCode = %d", res.ExitCode)
	}
	if _, err := os.Stat(filepath.Join(root, "agent")); err != nil {
		t.Errorf("agent should have run: %v", err)
	}
}

// TestRunInWorkspace_OrderOfFire records hook fire order via append to
// a marker file; ensures after_create → before_run → agent → after_run
// → before_remove.
func TestRunInWorkspace_OrderOfFire(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "order.log")
	emit := func(label string) string {
		return "echo " + label + " >> " + log
	}
	hooks := config.Hooks{
		AfterCreate:  emit("after_create"),
		BeforeRun:    emit("before_run"),
		AfterRun:     emit("after_run"),
		BeforeRemove: emit("before_remove"),
		Timeout:      5 * time.Second,
	}
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", "echo agent >> " + log},
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}
	if _, err := runInWorkspace(context.Background(), prof, testIssue(), wt, true, RunOpts{Hooks: hooks}, &Result{}); err != nil {
		t.Fatalf("runInWorkspace: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"after_create", "before_run", "agent", "after_run", "before_remove"}
	if len(got) != len(want) {
		t.Fatalf("order had %d entries; want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("position %d: got %q, want %q", i, got[i], w)
		}
	}
}
