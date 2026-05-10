// Package executor runs a config.Profile against an Issue in an isolated
// worktree. Template-variable substitution turns {prompt}, {worktree},
// {repo}, {issue_title}, {issue_body}, {issue_number} into the actual
// values before exec. Captures stdout+stderr for logging; exit code 0 is
// success, anything else is dispatch_failed.
//
// Worktree management: a fresh `git worktree add` under a temp dir,
// removed on completion. The branch name follows `afk/<repo-base>-<num>-<slug>`.
package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/github"
	"github.com/nSimonFR/gleaner/internal/config"
)

type Result struct {
	Profile      string
	Branch       string
	WorkTree     string
	ExitCode     int
	DurationMs   int64
	Stdout       string
	Stderr       string
	HasChanges   bool   // worktree had `git diff --quiet` non-zero
	HeadSHA      string
	Error        error
}

// Run executes the profile against the given issue. The caller owns the
// worktree cleanup decision (passed via cleanup bool).
func Run(ctx context.Context, prof *config.Profile, iss *github.Issue, workTreeRoot string, cleanup bool) (*Result, error) {
	start := time.Now()
	result := &Result{Profile: prof.Name}

	// 1. Clone fresh worktree.
	wt, branch, err := setupWorkTree(ctx, iss.Repo, iss.Number, iss.Title, workTreeRoot)
	if err != nil {
		return result, fmt.Errorf("worktree setup: %w", err)
	}
	result.WorkTree = wt
	result.Branch = branch
	if cleanup {
		defer cleanupWorkTree(wt)
	}

	// 2. Render template vars in profile.Run.
	vars := map[string]string{
		"prompt":       buildPrompt(iss),
		"worktree":     wt,
		"repo":         iss.Repo,
		"issue_title":  iss.Title,
		"issue_body":   iss.Body,
		"issue_number": fmt.Sprintf("%d", iss.Number),
	}
	rendered := renderArgs(prof.Run, vars)
	renderedCwd := renderString(prof.Cwd, vars)

	// 3. Exec the command.
	timeout := prof.Timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, rendered[0], rendered[1:]...)
	cmd.Dir = renderedCwd
	cmd.Env = append(os.Environ(),
		"PROMPT="+vars["prompt"],
		"GLEANER_WORKTREE="+wt,
		"GLEANER_REPO="+iss.Repo,
		"GLEANER_ISSUE="+vars["issue_number"],
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.DurationMs = time.Since(start).Milliseconds()

	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			result.ExitCode = ee.ExitCode()
		} else {
			result.ExitCode = -1
		}
		result.Error = runErr
		return result, runErr
	}
	result.ExitCode = 0

	// 4. Inspect worktree changes.
	diffCmd := exec.CommandContext(ctx, "git", "diff", "--quiet")
	diffCmd.Dir = wt
	if err := diffCmd.Run(); err != nil {
		// Non-zero = changes present.
		result.HasChanges = true
	}
	headCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	headCmd.Dir = wt
	if h, err := headCmd.Output(); err == nil {
		result.HeadSHA = strings.TrimSpace(string(h))
	}

	return result, nil
}

func renderArgs(args []string, vars map[string]string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = renderString(a, vars)
	}
	return out
}

func renderString(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

func buildPrompt(iss *github.Issue) string {
	return fmt.Sprintf("GitHub issue %s#%d: %s\n\n%s\n\nMake the change. Commit when done.",
		iss.Repo, iss.Number, iss.Title, iss.Body)
}

func setupWorkTree(ctx context.Context, repo string, issueNum int, title, root string) (string, string, error) {
	repoBase := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repoBase = repo[i+1:]
	}
	slug := slugify(title)
	if slug == "" {
		slug = "task"
	}
	branch := fmt.Sprintf("afk/%s-%d-%s", repoBase, issueNum, slug)
	wtName := fmt.Sprintf("%s-%d", repoBase, issueNum)
	wt := filepath.Join(root, wtName)

	// gleaner doesn't ship with the source repo. Caller must have a local
	// clone available at $HOME/<repoBase> (matches user's convention for
	// sure-nix, for-sure, etc.). We use that as the source.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("home: %w", err)
	}
	sourceRepo := filepath.Join(home, repoBase)
	if _, err := os.Stat(filepath.Join(sourceRepo, ".git")); err != nil {
		return "", "", fmt.Errorf("source repo not found at %s (expected for %s)", sourceRepo, repo)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", sourceRepo, "worktree", "add", "-b", branch, wt, "origin/main")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("worktree add: %w (%s)", err, stderr.String())
	}
	return wt, branch, nil
}

func cleanupWorkTree(wt string) {
	// Best-effort: remove the worktree directory. The source repo's
	// .git/worktrees/<name> metadata becomes stale; `git worktree prune`
	// from the source repo will clean it on demand. v0.0.2 callers
	// usually pass cleanup=false so this is rarely invoked.
	_ = os.RemoveAll(wt)
}

func slugify(s string) string {
	var b strings.Builder
	prev := byte('-')
	for i := 0; i < len(s) && b.Len() < 40; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c)
			prev = c
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + 32)
			prev = c + 32
		case c >= '0' && c <= '9':
			b.WriteByte(c)
			prev = c
		case prev != '-':
			b.WriteByte('-')
			prev = '-'
		}
	}
	return strings.Trim(b.String(), "-")
}
