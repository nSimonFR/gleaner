package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/claude_oauth"
	codehost "github.com/nSimonFR/gleaner/internal/adapter/codehost/github"
	"github.com/nSimonFR/gleaner/internal/adapter/codex_journal"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/executor"
	"github.com/nSimonFR/gleaner/internal/orchestrator"
)

func serveCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to YAML config (required)")
	workTreeRoot := fs.String("worktree-root", "/tmp/gleaner-worktrees", "where to create per-task worktrees")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "serve: --config is required")
		return 2
	}

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

	quotaSources := map[string]adapter.QuotaSource{
		"claude": &claude_oauth.Adapter{},
		"codex":  &codex_journal.Adapter{},
	}

	// SPEC §16.1: best-effort startup cleanup of terminal-state workspaces.
	orchestrator.CleanupTerminalWorkspaces(ctx, trk, *workTreeRoot, cfg.Tracker.TerminalStates)

	state := orchestrator.NewState()
	orch := &orchestrator.Orchestrator{
		Cfg:           cfg,
		Tracker:       trk,
		CodeHost:      ch,
		CodehostRepos: chRepos,
		QuotaSources:  quotaSources,
		WorkTreeRoot:  *workTreeRoot,
		State:         state,
		HookFire:      func(event string, payload map[string]any) { fireHook(cfg.Hook, event, payload) },
		PROpener:      makePROpener(trk, ch),
	}

	orch.Run(ctx)
	return 0
}

// makePROpener returns a PROpener bound to the Tracker + CodeHost. Mirrors
// the inline pushBranch+CreatePR dance in drain.go but plugs into the
// orchestrator's worker goroutine. Shared helpers (pushBranch,
// worktreeBase, buildPRBody) live in drain.go — they're package-level
// in cmd/gleaner.
func makePROpener(trk tracker.Tracker, ch *codehost.Client) orchestrator.PROpener {
	return func(ctx context.Context, iss tracker.Issue, prof *config.Profile, res *executor.Result) (string, error) {
		if err := pushBranch(ctx, res.WorkTree, res.Branch); err != nil {
			return "", err
		}
		base, err := worktreeBase(ctx, res.WorkTree)
		if err != nil {
			base = "main"
		}
		body := buildPRBody(trk.Kind(), &iss, prof, res)
		return ch.CreatePR(ctx, iss.Repo, base, res.Branch,
			fmt.Sprintf("afk: %s", iss.Title), body,
			[]string{"afk", "needs-review"})
	}
}
