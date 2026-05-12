package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/config"
)

// TestRunInWorkspace_Phases_TwoPhaseHappyPath: agent runs once per phase.
// First invocation sees GLEANER_PHASE=plan and writes .gleaner/PLAN.md;
// gleaner reads it and fires OnPlanReady. Second invocation sees
// GLEANER_PHASE=execute. Both phases share the same workspace.
func TestRunInWorkspace_Phases_TwoPhaseHappyPath(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	phasesLog := filepath.Join(root, "phases.log")
	planFile := ".gleaner/PLAN.md"

	// Profile writes ".gleaner/PLAN.md" on plan phase, no-op on execute,
	// appends GLEANER_PHASE to phasesLog every call.
	script := `set -e
echo "$GLEANER_PHASE" >> ` + phasesLog + `
if [ "$GLEANER_PHASE" = "plan" ]; then
  mkdir -p .gleaner
  echo "- touch file.txt" > ` + planFile + `
fi
`
	prof := &config.Profile{
		Name:    "test",
		Run:     []string{"sh", "-c", script},
		Cwd:     wt,
		Timeout: 5 * time.Second,
	}

	var planReadyMu sync.Mutex
	var planReadyText string
	opts := RunOpts{
		Phases: []Phase{
			{Name: "plan", PromptTpl: "plan for: {prompt}", Required: false},
			{Name: "execute", PromptTpl: "", Required: true},
		},
		PlanFile: planFile,
		OnPlanReady: func(text string) {
			planReadyMu.Lock()
			planReadyText = text
			planReadyMu.Unlock()
		},
	}
	_, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, opts, &Result{})
	if err != nil {
		t.Fatalf("runInWorkspace: %v", err)
	}

	// Phases fired in order.
	data, _ := os.ReadFile(phasesLog)
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"plan", "execute"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("phase order = %v; want %v", got, want)
	}

	// OnPlanReady fired with the plan file contents (trimmed).
	planReadyMu.Lock()
	defer planReadyMu.Unlock()
	if !strings.Contains(planReadyText, "touch file.txt") {
		t.Errorf("OnPlanReady text = %q; want plan content", planReadyText)
	}
}

// TestRunInWorkspace_Phases_PlanMissing: plan phase exits 0 but writes
// NOTHING. OnPlanReady must NOT be called; execute phase still runs.
func TestRunInWorkspace_Phases_PlanMissing(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	executeFired := filepath.Join(root, "execute-fired")
	script := `if [ "$GLEANER_PHASE" = "execute" ]; then : > ` + executeFired + `; fi`
	prof := &config.Profile{Name: "test", Run: []string{"sh", "-c", script}, Cwd: wt, Timeout: 5 * time.Second}

	var planReadyCalled bool
	opts := RunOpts{
		Phases: []Phase{
			{Name: "plan", Required: false},
			{Name: "execute", Required: true},
		},
		PlanFile:    ".gleaner/PLAN.md",
		OnPlanReady: func(text string) { planReadyCalled = true },
	}
	_, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, opts, &Result{})
	if err != nil {
		t.Fatalf("runInWorkspace: %v", err)
	}
	if planReadyCalled {
		t.Error("OnPlanReady fired despite missing plan file")
	}
	if _, err := os.Stat(executeFired); err != nil {
		t.Error("execute phase did not run after plan phase wrote nothing")
	}
}

// TestRunInWorkspace_Phases_PlanFailureBestEffort: plan phase exits 1
// (best-effort, Required=false). Execute phase still runs and succeeds;
// the overall Run returns nil.
func TestRunInWorkspace_Phases_PlanFailureBestEffort(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	executeFired := filepath.Join(root, "execute-fired")
	script := `if [ "$GLEANER_PHASE" = "plan" ]; then exit 1; fi; : > ` + executeFired
	prof := &config.Profile{Name: "test", Run: []string{"sh", "-c", script}, Cwd: wt, Timeout: 5 * time.Second}

	opts := RunOpts{
		Phases: []Phase{
			{Name: "plan", Required: false},
			{Name: "execute", Required: true},
		},
	}
	_, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, opts, &Result{})
	if err != nil {
		t.Fatalf("plan failure should not propagate when Required=false; got: %v", err)
	}
	if _, err := os.Stat(executeFired); err != nil {
		t.Error("execute phase did not run after best-effort plan failure")
	}
}

// TestRunInWorkspace_Phases_ExecuteFailureFatal: execute phase exits 1
// (Required=true). Overall Run returns the agent error.
func TestRunInWorkspace_Phases_ExecuteFailureFatal(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `if [ "$GLEANER_PHASE" = "execute" ]; then exit 7; fi`
	prof := &config.Profile{Name: "test", Run: []string{"sh", "-c", script}, Cwd: wt, Timeout: 5 * time.Second}

	opts := RunOpts{
		Phases: []Phase{
			{Name: "plan", Required: false},
			{Name: "execute", Required: true},
		},
	}
	res, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, opts, &Result{})
	if err == nil {
		t.Fatal("expected execute phase exit-7 error")
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d; want 7", res.ExitCode)
	}
}

// TestRunInWorkspace_Phases_PlanFileCleanedUp: after all phases run,
// the plan file (and its parent dir if empty) must be removed from the
// worktree — plan content is captured via OnPlanReady; the on-disk
// scratch must never end up in the resulting PR.
func TestRunInWorkspace_Phases_PlanFileCleanedUp(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	planFile := ".gleaner/PLAN.md"
	script := `if [ "$GLEANER_PHASE" = "plan" ]; then mkdir -p .gleaner && echo "plan" > ` + planFile + `; fi`
	prof := &config.Profile{Name: "test", Run: []string{"sh", "-c", script}, Cwd: wt, Timeout: 5 * time.Second}

	opts := RunOpts{
		Phases: []Phase{
			{Name: "plan", Required: false},
			{Name: "execute", Required: true},
		},
		PlanFile:    planFile,
		OnPlanReady: func(string) {},
	}
	if _, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, opts, &Result{}); err != nil {
		t.Fatalf("runInWorkspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, planFile)); !os.IsNotExist(err) {
		t.Errorf("plan file still present after dispatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".gleaner")); !os.IsNotExist(err) {
		t.Errorf(".gleaner/ dir still present after dispatch: %v", err)
	}
}

// TestRunInWorkspace_Phases_BackCompatSinglePhase: nil Phases preserves
// the v0.1 single-invocation behavior — GLEANER_PHASE is NOT exported.
func TestRunInWorkspace_Phases_BackCompatSinglePhase(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "ws")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	phaseEnv := filepath.Join(root, "phase-env")
	script := `echo "phase=${GLEANER_PHASE-UNSET}" > ` + phaseEnv
	prof := &config.Profile{Name: "test", Run: []string{"sh", "-c", script}, Cwd: wt, Timeout: 5 * time.Second}

	_, err := runInWorkspace(context.Background(), prof, testIssue(), wt, false, RunOpts{}, &Result{})
	if err != nil {
		t.Fatalf("runInWorkspace: %v", err)
	}
	data, _ := os.ReadFile(phaseEnv)
	if strings.TrimSpace(string(data)) != "phase=UNSET" {
		t.Errorf("GLEANER_PHASE leaked into back-compat path: %s", data)
	}
}
