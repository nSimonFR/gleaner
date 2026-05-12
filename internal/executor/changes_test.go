package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitInit creates a minimal git tree: bare origin/<base> + an agent worktree
// branched off it, with gleaner.base set to <base>. Returns the worktree path.
func gitInit(t *testing.T, base string) string {
	t.Helper()
	root := t.TempDir()

	// 1. Bare "origin" repo + working seed repo to push the base commit.
	originDir := filepath.Join(root, "origin.git")
	seedDir := filepath.Join(root, "seed")
	run(t, "git", "init", "--bare", "--initial-branch="+base, originDir)
	run(t, "git", "init", "--initial-branch="+base, seedDir)
	runIn(t, seedDir, "git", "config", "user.email", "test@example.com")
	runIn(t, seedDir, "git", "config", "user.name", "Test")
	runIn(t, seedDir, "git", "config", "commit.gpgsign", "false")
	runIn(t, seedDir, "git", "remote", "add", "origin", originDir)
	if err := os.WriteFile(filepath.Join(seedDir, "seed.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(t, seedDir, "git", "add", ".")
	runIn(t, seedDir, "git", "commit", "-m", "initial")
	runIn(t, seedDir, "git", "push", "origin", base)

	// 2. Clone origin → agent worktree, branch off origin/<base>.
	work := filepath.Join(root, "work")
	run(t, "git", "clone", "--branch", base, originDir, work)
	runIn(t, work, "git", "config", "user.email", "test@example.com")
	runIn(t, work, "git", "config", "user.name", "Test")
	runIn(t, work, "git", "config", "commit.gpgsign", "false")
	runIn(t, work, "git", "checkout", "-b", "afk/feature")
	runIn(t, work, "git", "config", "--local", "gleaner.base", base)
	return work
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func runIn(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("[%s] %s %v: %v\n%s", dir, name, args, err, out)
	}
}

// TestDetectWorktreeChanges_CleanNoCommits: working tree clean and HEAD at
// origin/main → no changes.
func TestDetectWorktreeChanges_CleanNoCommits(t *testing.T) {
	wt := gitInit(t, "main")
	if detectWorktreeChanges(context.Background(), wt) {
		t.Error("detectWorktreeChanges=true on a clean worktree at origin/main")
	}
}

// TestDetectWorktreeChanges_DirtyUncommitted: an uncommitted file makes
// HasChanges=true (existing behavior, preserved).
func TestDetectWorktreeChanges_DirtyUncommitted(t *testing.T) {
	wt := gitInit(t, "main")
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !detectWorktreeChanges(context.Background(), wt) {
		t.Error("detectWorktreeChanges=false despite untracked file")
	}
}

// TestDetectWorktreeChanges_CommittedClean: agent committed and wd is
// clean (the bug the original code missed). HEAD is ahead of
// origin/main → must be detected as changes.
func TestDetectWorktreeChanges_CommittedClean(t *testing.T) {
	wt := gitInit(t, "main")
	if err := os.WriteFile(filepath.Join(wt, "file.txt"), []byte("real change"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(t, wt, "git", "add", ".")
	runIn(t, wt, "git", "commit", "-m", "feat: real change")
	if !detectWorktreeChanges(context.Background(), wt) {
		t.Error("detectWorktreeChanges=false on a worktree with a real commit ahead of origin/main")
	}
}

// TestDetectWorktreeChanges_NoGleanerBase: when the worktree lacks the
// gleaner.base config (corrupt/missing setup), the function returns
// false without panicking. Better to under-report than to push a noisy
// PR for a misconfigured workspace.
func TestDetectWorktreeChanges_NoGleanerBase(t *testing.T) {
	wt := t.TempDir()
	runIn(t, wt, "git", "init")
	if detectWorktreeChanges(context.Background(), wt) {
		t.Error("detectWorktreeChanges=true on a plain git repo with no gleaner.base")
	}
}
