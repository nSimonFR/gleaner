package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/claude_oauth"
	"github.com/nSimonFR/gleaner/internal/adapter/codex_journal"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker/linear"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/picker"
)

func tickCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("tick", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to gleaner YAML config (required)")
	dryRun := fs.Bool("dry-run", false, "log the pick but skip the Linear Assign mutation")
	timeoutSec := fs.Int("timeout", 30, "overall tick timeout in seconds")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "tick: --config is required")
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tick: load config: %v\n", err)
		return 1
	}

	tctx, cancel := context.WithTimeout(ctx, time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	tr := linear.New(cfg.Tracker.APIKeyFile, cfg.Tracker.TeamKey, cfg.Tracker.ActiveStates)
	if err := tr.EnforceAuth(tctx); err != nil {
		fmt.Fprintf(os.Stderr, "tick: %v\n", err)
		return 1
	}

	srcs := []adapter.QuotaSource{
		&claude_oauth.Adapter{},
		&codex_journal.Adapter{},
	}

	out, err := picker.Tick(tctx, picker.Inputs{
		Cfg:          cfg,
		Tracker:      tr,
		QuotaSources: srcs,
		DryRun:       *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tick: %v\n", err)
		return 1
	}

	switch {
	case out.Picked != nil && out.AlreadyAssigned:
		fmt.Printf("already_assigned %s (%s)\n", out.Picked.Identifier, out.Picked.ID)
	case out.Picked != nil && *dryRun:
		fmt.Printf("dry_run_pick %s (%s) priority=%d\n", out.Picked.Identifier, out.Picked.ID, out.Picked.Priority)
	case out.Picked != nil:
		fmt.Printf("picked %s (%s) priority=%d → assigned to %s\n",
			out.Picked.Identifier, out.Picked.ID, out.Picked.Priority, cfg.Tracker.CyrusUserID)
	case out.Skipped != "":
		fmt.Printf("skipped: %s\n", out.Skipped)
	default:
		fmt.Println("no_candidates")
	}
	return 0
}
