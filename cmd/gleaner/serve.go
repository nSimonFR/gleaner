package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/claude_oauth"
	codehost "github.com/nSimonFR/gleaner/internal/adapter/codehost/github"
	"github.com/nSimonFR/gleaner/internal/adapter/codex_journal"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/predicate"
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

	sources := []adapter.QuotaSource{
		&claude_oauth.Adapter{},
		&codex_journal.Adapter{},
	}

	poll := cfg.Hours.Poll
	if poll == 0 {
		poll = 10 * time.Minute
	}
	fmt.Printf("serve: starting; tracker=%s poll=%s profiles=%d\n", trk.Kind(), poll, len(cfg.Profiles))

	tick := time.NewTicker(poll)
	defer tick.Stop()

	runOne := func() {
		decision := predicate.Evaluate(ctx, predicate.Inputs{
			Cfg:           cfg,
			CodeHost:      ch,
			CodehostRepos: chRepos,
			QuotaSources:  sources,
		})
		if !decision.Allow {
			fmt.Printf("[%s] skip: %s\n", time.Now().Format(time.RFC3339), decision.Reason)
			return
		}
		fmt.Printf("[%s] predicate: ok — dispatching\n", time.Now().Format(time.RFC3339))
		runDispatchOnce(ctx, cfg, trk, ch, *workTreeRoot)
	}

	// Fire once immediately, then on every tick.
	runOne()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("serve: shutting down")
			return 0
		case <-tick.C:
			runOne()
		}
	}
}

// runDispatchOnce reuses drain's pickIssue + dispatchAndOpenPR. Logs but
// does NOT propagate errors — the serve loop must continue across failures.
func runDispatchOnce(ctx context.Context, cfg *config.Config, trk tracker.Tracker, ch *codehost.Client, wtRoot string) {
	issue, profile, err := pickIssue(ctx, trk, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pick: %v\n", err)
		return
	}
	if issue == nil {
		fmt.Println("no eligible issues")
		return
	}
	if err := dispatchAndOpenPR(ctx, cfg, trk, ch, issue, profile, wtRoot); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch error: %v\n", err)
	}
}
