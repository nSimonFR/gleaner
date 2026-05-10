package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/claude_oauth"
	"github.com/nSimonFR/gleaner/internal/adapter/codex_journal"
	"github.com/nSimonFR/gleaner/internal/adapter/github"
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
	if cfg.Account == "" {
		fmt.Fprintln(os.Stderr, "config: account is required (e.g. nSimonFR-ai)")
		return 1
	}
	if len(cfg.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "config: repos must not be empty")
		return 1
	}

	gh := github.New(cfg.Account)
	if err := gh.EnforceAuth(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	sources := []adapter.QuotaSource{
		&claude_oauth.Adapter{},
		&codex_journal.Adapter{},
	}

	decision := predicate.Evaluate(ctx, predicate.Inputs{
		Cfg: cfg, GH: gh, QuotaSources: sources,
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
	issue, profile, err := pickIssue(ctx, gh, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pick:", err)
		return 1
	}
	if issue == nil {
		fmt.Println("skip: no_eligible_issues")
		return 0
	}
	if err := dispatchAndOpenPR(ctx, cfg, gh, issue, profile, *workTreeRoot); err != nil {
		return 1
	}
	return 0
}

// dispatchAndOpenPR runs `profile` against `issue` in a worktree, and if
// the profile's on_success == open_pr and the worktree has changes, opens
// a PR. Used by both `drain` and `serve`. Returns nil on the full success
// path or on no-change skips; returns error only on hard failures.
func dispatchAndOpenPR(ctx context.Context, cfg *config.Config, gh *github.Client, issue *github.Issue, profile *config.Profile, workTreeRoot string) error {
	fmt.Printf("dispatch: %s#%d → profile=%s (%s)\n", issue.Repo, issue.Number, profile.Name, strings.Join(profile.Run, " "))

	res, runErr := executor.Run(ctx, profile, issue, workTreeRoot, false)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "dispatch_failed: exit=%d err=%v\n", res.ExitCode, runErr)
		fireHook(cfg.Hook, "dispatch_failed", map[string]any{
			"reason":   runErr.Error(),
			"profile":  profile.Name,
			"task_id":  fmt.Sprintf("github:%s#%d", issue.Repo, issue.Number),
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
	prBody := buildPRBody(issue, profile, res)
	url, err := gh.CreatePR(ctx, issue.Repo, "main", res.Branch,
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
		"task_id": fmt.Sprintf("github:%s#%d", issue.Repo, issue.Number),
	})
	return nil
}

func pickIssue(ctx context.Context, gh *github.Client, cfg *config.Config) (*github.Issue, *config.Profile, error) {
	for _, repo := range cfg.Repos {
		issues, err := gh.EligibleIssues(ctx, repo, cfg.Require, cfg.Block)
		if err != nil {
			return nil, nil, err
		}
		for _, iss := range issues {
			labels := make([]string, 0, len(iss.Labels))
			for _, l := range iss.Labels {
				labels = append(labels, l.Name)
			}
			profile := cfg.MatchProfile(labels)
			if profile == nil {
				continue
			}
			return &iss, profile, nil
		}
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

func buildPRBody(iss *github.Issue, prof *config.Profile, res *executor.Result) string {
	return fmt.Sprintf(`Closes %s#%d.

%s

---

<!-- gleaner: profile=%s plan=%s task_id=github:%s#%d branch=%s duration_ms=%d head_sha=%s -->
*Opened by gleaner via profile* %s *(%s).*
`,
		iss.Repo, iss.Number,
		iss.Title,
		prof.Name, prof.Plan, iss.Repo, iss.Number, res.Branch, res.DurationMs, res.HeadSHA,
		prof.Name, strings.Join(prof.Run, " "),
	)
}
