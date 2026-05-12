// Package executor runs a config.Profile against an Issue in an isolated
// worktree. Template-variable substitution turns {prompt}, {worktree},
// {repo}, {issue_title}, {issue_body}, {issue_number}, {issue_identifier}
// into the actual values before exec. Captures stdout+stderr for logging;
// exit code 0 is success, anything else is dispatch_failed.
//
// Worktree management: a fresh `git worktree add` under a temp dir,
// removed on completion. The branch name follows `afk/<repo-base>-<num>-<slug>`.
//
// Milestone B: fires four Symphony-SPEC §5.3.4 lifecycle hooks around
// each Run — after_create, before_run, after_run, before_remove.
// Failure semantics: before_run non-zero is a denial (returns
// ErrBeforeRunDenied); after_create non-zero is fatal (returns
// ErrAfterCreateFailed and tears the workspace down); the other two
// are best-effort (logged, ignored).
package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/hook"
)

// ErrBeforeRunDenied means the before_run hook exited non-zero. The
// dispatch was not attempted; the orchestrator should treat this as a
// skip, NOT a dispatch failure (it's the operator's quota-gate doing
// its job, the same way the internal predicate does).
var ErrBeforeRunDenied = errors.New("before_run hook denied dispatch")

// ErrAfterCreateFailed means the after_create hook exited non-zero on
// initial workspace creation. The workspace is destroyed and the
// dispatch attempt aborts. SPEC §9.4: "Fatal to workspace creation".
var ErrAfterCreateFailed = errors.New("after_create hook failed; workspace destroyed")

type Result struct {
	Profile    string
	Branch     string
	WorkTree   string
	ExitCode   int
	DurationMs int64
	Stdout     string
	Stderr     string
	HasChanges bool // worktree had `git status --porcelain` non-empty
	HeadSHA    string
	Error      error
}

// RunOpts bundles the configuration the executor reads from cfg.Hooks
// and cfg.Agent. Grouped here so call sites stay readable as more
// agent-wide knobs land in later milestones (read_timeout, turn_timeout).
type RunOpts struct {
	Hooks        config.Hooks
	StallTimeout time.Duration // SPEC §5.3.6 — 0 disables
}

// Run executes the profile against the given issue. The caller owns the
// worktree cleanup decision (passed via cleanup bool).
//
// Run is a thin wrapper around setupWorkTree + runInWorkspace. The split
// lets tests drive the hook + agent path without needing a real git
// source repo on disk.
func Run(ctx context.Context, prof *config.Profile, iss *tracker.Issue, workTreeRoot string, cleanup bool, opts RunOpts) (*Result, error) {
	result := &Result{Profile: prof.Name}

	// `issueKey` is the human-meaningful portion used in worktree path and
	// branch name. For GitHub: the issue number ("60"). For Linear: the
	// identifier ("MT-649"). Linear issues have Number==0 so we fall back
	// to Identifier; sanitize it so we don't smuggle "/" into a branch.
	issueKey := fmt.Sprintf("%d", iss.Number)
	if iss.Number == 0 {
		issueKey = slugify(iss.Identifier)
		if issueKey == "" {
			issueKey = "task"
		}
	}
	wt, branch, err := setupWorkTree(ctx, iss.Repo, issueKey, iss.Title, workTreeRoot)
	if err != nil {
		return result, fmt.Errorf("worktree setup: %w", err)
	}
	result.WorkTree = wt
	result.Branch = branch
	return runInWorkspace(ctx, prof, iss, wt, cleanup, opts, result)
}

// runInWorkspace fires the 4 lifecycle hooks around the agent exec.
// Extracted from Run so tests can drive it with a t.TempDir() workspace
// — git-free. Caller has already populated result.WorkTree / .Branch.
func runInWorkspace(ctx context.Context, prof *config.Profile, iss *tracker.Issue, wt string, cleanup bool, opts RunOpts, result *Result) (*Result, error) {
	start := time.Now()
	env := hookEnv(iss, wt)
	hooks := opts.Hooks

	// cleanup=true means delete workspace at end. We honor that via a
	// before_remove hook + RemoveAll. The deferred cleanup includes the
	// after_create failure path: if after_create fails, we MUST still
	// give before_remove a chance to inspect the partial workspace
	// (Elixir Symphony's Workspace.remove always fires before_remove
	// before deletion — gleaner matches).
	var afterCreateFailed bool
	defer func() {
		// Fire before_remove + cleanup whenever cleanup is requested,
		// OR whenever after_create failed (so the operator's
		// before_remove still observes the partial state if they wired
		// one). Otherwise leave the workspace for the caller to keep.
		if cleanup || afterCreateFailed {
			runBeforeRemoveAndDelete(ctx, hooks, wt, env)
		}
	}()

	// 1. after_create — runs after the worktree exists. The executor
	// always creates a fresh worktree per Run (no reuse), so this is
	// equivalent to "after worktree exists". Workspace reuse across
	// retries (Milestone C) will refine this to gate on first-creation.
	// SPEC §9.4: failure is fatal.
	if err := hook.RunLifecycle(ctx, "after_create", hooks.AfterCreate, wt, env, hooks.Timeout); err != nil {
		afterCreateFailed = true
		result.Error = err
		return result, fmt.Errorf("%w: %v", ErrAfterCreateFailed, err)
	}

	// 2. before_run — gating hook. Non-zero exit aborts the dispatch
	// attempt (not a failure — see ErrBeforeRunDenied doc).
	if err := hook.RunLifecycle(ctx, "before_run", hooks.BeforeRun, wt, env, hooks.Timeout); err != nil {
		result.Error = err
		return result, fmt.Errorf("%w: %v", ErrBeforeRunDenied, err)
	}

	// 3. Render template vars + exec the agent command.
	vars := map[string]string{
		"prompt":           buildPrompt(iss),
		"worktree":         wt,
		"repo":             iss.Repo,
		"issue_title":      iss.Title,
		"issue_body":       iss.Body,
		"issue_number":     fmt.Sprintf("%d", iss.Number),
		"issue_identifier": iss.Identifier,
	}
	rendered := renderArgs(prof.Run, vars)
	renderedCwd := renderString(prof.Cwd, vars)

	timeout := prof.Timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, rendered[0], rendered[1:]...)
	cmd.Dir = renderedCwd
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "GLEANER_PROMPT="+vars["prompt"])
	// SPEC §5.3.6 stall detection: a watcher polls the time of the most
	// recent stdout/stderr Write and SIGKILLs the child if it goes
	// silent past opts.StallTimeout. The watcher exits when its
	// context cancels (deferred below).
	sw := newStallWriter()
	es := newStallWriter()
	cmd.Stdout = sw
	cmd.Stderr = es
	watchCtx, stopWatch := context.WithCancel(ctx)
	stalled := watchStall(watchCtx, cmd, sw, opts.StallTimeout)
	runErr := cmd.Run()
	stopWatch()

	result.Stdout = sw.String()
	result.Stderr = es.String()
	result.DurationMs = time.Since(start).Milliseconds()

	// Distinguish a stall-induced kill from any other runErr. The watcher
	// fires `stalled` (buffered) when it kills; we check non-blockingly.
	stallFired := false
	select {
	case _, ok := <-stalled:
		stallFired = ok
	default:
	}

	// 4. after_run — always fires, even on dispatch failure. Best-effort
	// per SPEC §9.4 (failure logged, ignored). Carries an extra
	// GLEANER_EXIT_CODE env so post-mortem hooks know the outcome.
	exitCode := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	afterRunEnv := append([]string{fmt.Sprintf("GLEANER_EXIT_CODE=%d", exitCode)}, env...)
	if err := hook.RunLifecycle(ctx, "after_run", hooks.AfterRun, wt, afterRunEnv, hooks.Timeout); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}

	if stallFired {
		result.ExitCode = exitCode
		result.Error = ErrStalled
		return result, ErrStalled
	}
	if runErr != nil {
		result.ExitCode = exitCode
		result.Error = runErr
		return result, runErr
	}
	result.ExitCode = 0

	// 5. Inspect worktree changes — `status --porcelain` catches BOTH
	// modifications to tracked files AND new untracked files. A bare
	// `git diff --quiet` would miss new-file dispatches entirely.
	// (No-op for tests that drive runInWorkspace against a plain dir.)
	statusCmd := exec.CommandContext(ctx, "git", "-C", wt, "status", "--porcelain")
	if out, err := statusCmd.Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		result.HasChanges = true
	}
	headCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	headCmd.Dir = wt
	if h, err := headCmd.Output(); err == nil {
		result.HeadSHA = strings.TrimSpace(string(h))
	}

	return result, nil
}

// hookEnv returns the GLEANER_* env list every lifecycle hook receives.
// Same shape as what the executor's child command sees (sans GLEANER_PROMPT,
// which can be large — hooks rarely need it and the command always gets it
// separately).
func hookEnv(iss *tracker.Issue, wt string) []string {
	return []string{
		"GLEANER_WORKTREE=" + wt,
		"GLEANER_REPO=" + iss.Repo,
		"GLEANER_ISSUE=" + fmt.Sprintf("%d", iss.Number),
		"GLEANER_ISSUE_IDENTIFIER=" + iss.Identifier,
		"GLEANER_ISSUE_TITLE=" + iss.Title,
	}
}

// runBeforeRemoveAndDelete fires before_remove (best-effort, logged) and
// then deletes the worktree directory. Failure of either step is logged;
// neither is propagated since the caller has already returned.
func runBeforeRemoveAndDelete(ctx context.Context, hooks config.Hooks, wt string, env []string) {
	if err := hook.RunLifecycle(ctx, "before_remove", hooks.BeforeRemove, wt, env, hooks.Timeout); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
	cleanupWorkTree(wt)
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

func buildPrompt(iss *tracker.Issue) string {
	return fmt.Sprintf("Issue %s: %s\n\n%s\n\nMake the change. Commit when done.",
		iss.Identifier, iss.Title, iss.Body)
}

func setupWorkTree(ctx context.Context, repo, issueKey, title, root string) (string, string, error) {
	repoBase := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repoBase = repo[i+1:]
	}
	slug := slugify(title)
	if slug == "" {
		slug = "task"
	}
	// Append a unix-second suffix so successive runs against the same issue
	// don't collide on the worktree path. The branch name also includes it
	// so retries don't clobber unpushed work.
	suffix := fmt.Sprintf("%d", time.Now().Unix())
	branch := fmt.Sprintf("afk/%s-%s-%s-%s", repoBase, issueKey, slug, suffix)
	wtName := fmt.Sprintf("%s-%s-%s", repoBase, issueKey, suffix)
	wt := filepath.Join(root, wtName)

	// Source repo: gleaner doesn't ship clones. Caller must have a local
	// clone at $HOME/<repoBase> (matches the user's `sure-nix`/`for-sure`
	// convention). Configurable in v0.1+ via config.clones_dir.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("home: %w", err)
	}
	sourceRepo := filepath.Join(home, repoBase)
	if _, err := os.Stat(filepath.Join(sourceRepo, ".git")); err != nil {
		return "", "", fmt.Errorf("source repo not found at %s (expected for %s)", sourceRepo, repo)
	}

	// Fetch origin so we branch from current main, not stale local state.
	fetchCmd := exec.CommandContext(ctx, "git", "-C", sourceRepo, "fetch", "--quiet", "origin")
	var fetchStderr bytes.Buffer
	fetchCmd.Stderr = &fetchStderr
	if err := fetchCmd.Run(); err != nil {
		return "", "", fmt.Errorf("git fetch %s: %w (%s)", sourceRepo, err, fetchStderr.String())
	}

	// Detect default branch (handles main / master / develop).
	base, err := defaultBranch(ctx, sourceRepo)
	if err != nil {
		return "", "", fmt.Errorf("default branch %s: %w", sourceRepo, err)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", sourceRepo, "worktree", "add", "-b", branch, wt, "origin/"+base)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("worktree add: %w (%s)", err, stderr.String())
	}
	// Stamp the base branch into the worktree's local git config so the
	// PR opener picks it up without having to re-derive defaultBranch.
	_ = exec.CommandContext(ctx, "git", "-C", wt, "config", "--local", "gleaner.base", base).Run()
	return wt, branch, nil
}

// defaultBranch returns the name of `origin/HEAD`'s ref (e.g. "main",
// "master", "develop"). Falls back to "main" if introspection fails.
func defaultBranch(ctx context.Context, sourceRepo string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", sourceRepo,
		"symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "main", nil // permissive fallback
	}
	ref := strings.TrimSpace(string(out))
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[i+1:], nil
	}
	return ref, nil
}

func cleanupWorkTree(wt string) {
	// Best-effort: remove the worktree directory. The source repo's
	// .git/worktrees/<name> metadata becomes stale; `git worktree prune`
	// from the source repo will clean it on demand.
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
