package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/claude_oauth"
	codehost "github.com/nSimonFR/gleaner/internal/adapter/codehost/github"
	"github.com/nSimonFR/gleaner/internal/adapter/codex_journal"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/executor"
	"github.com/nSimonFR/gleaner/internal/hook"
	"github.com/nSimonFR/gleaner/internal/predicate"
)

func fireHook(script, event string, payload map[string]any) {
	hook.Fire(script, event, payload)
}

func drainCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("drain", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to YAML config")
	once := fs.Bool("once", false, "dispatch one issue and exit (v0.0.2 always behaves this way)")
	dryRun := fs.Bool("dry-run", false, "evaluate predicate only; print decision; do not dispatch")
	workTreeRoot := fs.String("worktree-root", "/tmp/gleaner-worktrees", "where to create per-task worktrees")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = once // v0.0.2 is single-shot; flag accepted for forward compat

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}

	trk, err := buildTracker(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tracker:", err)
		return 1
	}
	if err := trk.EnforceAuth(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ch := buildCodeHost(cfg)
	chRepos := codehostRepos(cfg)

	sources := []adapter.QuotaSource{
		&claude_oauth.Adapter{},
		&codex_journal.Adapter{},
	}

	decision := predicate.Evaluate(ctx, predicate.Inputs{
		Cfg:           cfg,
		CodeHost:      ch,
		CodehostRepos: chRepos,
		QuotaSources:  sources,
	})
	if !decision.Allow {
		fmt.Printf("skip: %s\n", decision.Reason)
		return 0
	}
	fmt.Println("predicate: ok")
	if *dryRun {
		return 0
	}

	// Pick one issue.
	issue, profile, err := pickIssue(ctx, trk, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pick:", err)
		return 1
	}
	if issue == nil {
		fmt.Println("skip: no_eligible_issues")
		return 0
	}
	if err := dispatchAndOpenPR(ctx, cfg, trk, ch, issue, profile, *workTreeRoot); err != nil {
		return 1
	}
	return 0
}

// dispatchAndOpenPR runs `profile` against `issue` in a worktree, and if
// the profile's on_success == open_pr and the worktree has changes, opens
// a PR. Used by both `drain` and `serve`. Returns nil on the full success
// path or on no-change skips; returns error only on hard failures.
func dispatchAndOpenPR(ctx context.Context, cfg *config.Config, trk tracker.Tracker, ch *codehost.Client, issue *tracker.Issue, profile *config.Profile, workTreeRoot string) error {
	taskID := fmt.Sprintf("%s:%s", trk.Kind(), issue.Identifier)
	fmt.Printf("dispatch: %s → profile=%s (%s)\n", issue.Identifier, profile.Name, strings.Join(profile.Run, " "))

	res, runErr := executor.Run(ctx, profile, issue, workTreeRoot, false, executor.RunOpts{
		Hooks:        cfg.Hooks,
		StallTimeout: cfg.Agent.StallTimeout,
		TurnTimeout:  cfg.Agent.TurnTimeout,
	})
	if runErr != nil {
		// before_run denial is the operator's quota-gate doing its job —
		// log as a skip, do NOT fire the dispatch_failed event hook.
		if errors.Is(runErr, executor.ErrBeforeRunDenied) {
			fmt.Printf("skip: before_run_denied reason=%v\n", runErr)
			return nil
		}
		fmt.Fprintf(os.Stderr, "dispatch_failed: exit=%d err=%v\n", res.ExitCode, runErr)
		fireHook(cfg.Hook, "dispatch_failed", map[string]any{
			"reason":   runErr.Error(),
			"profile":  profile.Name,
			"task_id":  taskID,
			"exitcode": res.ExitCode,
		})
		return runErr
	}
	fmt.Printf("dispatch_ok: branch=%s changes=%v duration=%dms\n", res.Branch, res.HasChanges, res.DurationMs)

	if profile.OnSuccess != "open_pr" || !res.HasChanges {
		fmt.Printf("on_success=%s changes=%v — no PR opened\n", profile.OnSuccess, res.HasChanges)
		return nil
	}

	if err := pushBranch(ctx, res.WorkTree, res.Branch); err != nil {
		fmt.Fprintln(os.Stderr, "push:", err)
		return err
	}
	prBody := buildPRBody(trk.Kind(), issue, profile, res)
	// Pull the default branch from the worktree (it was branched off
	// origin/<default>, so HEAD's upstream knows the right base).
	base, err := worktreeBase(ctx, res.WorkTree)
	if err != nil {
		base = "main" // permissive fallback
	}
	url, err := ch.CreatePR(ctx, issue.Repo, base, res.Branch,
		fmt.Sprintf("afk: %s", issue.Title), prBody,
		[]string{"afk", "needs-review"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pr_create:", err)
		return err
	}
	fmt.Printf("pr_opened: %s\n", url)
	fireHook(cfg.Hook, "pr_opened", map[string]any{
		"pr":      map[string]any{"url": url, "branch": res.Branch},
		"profile": profile.Name,
		"task_id": taskID,
	})

	// For non-github trackers, write the PR URL back to the tracker so the
	// operator sees the result on their board (SPEC §11.4 equivalent via
	// orchestrator-owned Comment rather than agent tool calls).
	if trk.Kind() != "github" {
		body := fmt.Sprintf("Gleaner opened PR via profile `%s`: %s", profile.Name, url)
		if err := trk.Comment(ctx, issue.ID, body); err != nil {
			// Best-effort: log only. PR is already open; tracker write-back
			// failing should not fail the dispatch.
			fmt.Fprintf(os.Stderr, "tracker_comment_failed: %v\n", err)
		}
	}
	return nil
}

func pickIssue(ctx context.Context, trk tracker.Tracker, cfg *config.Config) (*tracker.Issue, *config.Profile, error) {
	issues, err := trk.ListActive(ctx)
	if err != nil {
		return nil, nil, err
	}
	for i := range issues {
		iss := issues[i]
		hasComplexity := false
		for _, l := range iss.Labels {
			if strings.HasPrefix(l, "complexity:") {
				hasComplexity = true
				break
			}
		}
		// Per the plan: missing complexity:* → skip, don't default-route.
		// The wildcard match: "*" profile catches everything otherwise,
		// which would route un-triaged issues to the default model.
		if !hasComplexity {
			fmt.Printf("skip-issue: %s reason=missing_complexity_label\n", iss.Identifier)
			continue
		}
		profile := cfg.MatchProfile(iss.Labels)
		if profile == nil {
			fmt.Printf("skip-issue: %s reason=no_matching_profile labels=%v\n", iss.Identifier, iss.Labels)
			continue
		}
		return &iss, profile, nil
	}
	return nil, nil, nil
}

func pushBranch(ctx context.Context, worktree, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "push", "-u", "origin", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push: %w (%s)", err, string(out))
	}
	return nil
}

// worktreeBase returns the short name of the branch the worktree was
// branched from. We stamped it as a config value in setupWorkTree:
//   `git config --local gleaner.base <base>` runs there.
// Falls back to reading `origin/HEAD`'s ref.
func worktreeBase(ctx context.Context, worktree string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", worktree, "config", "--local", "gleaner.base").Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s, nil
		}
	}
	// Fallback: symbolic-ref of origin/HEAD.
	out, err = exec.CommandContext(ctx, "git", "-C", worktree,
		"symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		return "main", nil
	}
	ref := strings.TrimSpace(string(out))
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[i+1:], nil
	}
	return ref, nil
}

// buildPRBody renders the PR description. For github tracker, includes
// `Closes <repo>#N` so merge auto-closes the issue; for non-github, adds
// a "Related to <identifier>" pointer with the source URL.
func buildPRBody(kind string, iss *tracker.Issue, prof *config.Profile, res *executor.Result) string {
	var ref string
	if kind == "github" {
		ref = fmt.Sprintf("Closes %s#%d.", iss.Repo, iss.Number)
	} else {
		ref = fmt.Sprintf("Related to %s (%s).", iss.Identifier, iss.URL)
	}
	return fmt.Sprintf(`%s

%s

---

<!-- gleaner: profile=%s plan=%s tracker=%s task_id=%s:%s branch=%s duration_ms=%d head_sha=%s -->
*Opened by gleaner via profile* %s *(%s).*
`,
		ref,
		iss.Title,
		prof.Name, prof.Plan, kind, kind, iss.Identifier, res.Branch, res.DurationMs, res.HeadSHA,
		prof.Name, strings.Join(prof.Run, " "),
	)
}
