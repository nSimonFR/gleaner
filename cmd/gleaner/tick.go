package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/claude_oauth"
	"github.com/nSimonFR/gleaner/internal/adapter/codex_journal"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/logging"
	"github.com/nSimonFR/gleaner/internal/trigger"
)

func tickCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("tick", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to gleaner YAML config (required)")
	dryRun := fs.Bool("dry-run", false, "evaluate predicates but skip exec")
	only := fs.String("only", "", "if set, only consider the trigger with this name")
	snapTimeout := fs.Int("snapshot-timeout", 10, "per-source snapshot fetch timeout (seconds)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "tick: --config is required")
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logging.Log("tick_load_failed", logging.F("err", err))
		return 1
	}
	if len(cfg.Triggers) == 0 {
		logging.Log("tick_no_triggers")
		return 0
	}

	sources := gatherSnapshots(ctx, time.Duration(*snapTimeout)*time.Second)
	for _, s := range sources {
		if s.Err != nil {
			logging.Log("snapshot_failed", logging.F("provider", s.Provider), logging.F("err", s.Err))
			continue
		}
		logging.Log("snapshot_ok",
			logging.F("provider", s.Provider),
			logging.F("short_pct", fmt.Sprintf("%.1f", s.Snap.Windows["short"].UsedPercent*100)),
			logging.F("long_pct", fmt.Sprintf("%.1f", s.Snap.Windows["long"].UsedPercent*100)),
		)
	}
	evalCtx := trigger.BuildContext(sources)

	any := false
	for _, t := range cfg.Triggers {
		if *only != "" && t.Name != *only {
			continue
		}
		any = true
		r := trigger.Run(ctx, t.Name, t.When, t.Run, t.Timeout, t.Env, evalCtx, *dryRun)
		logTriggerResult(r)
	}
	if !any && *only != "" {
		logging.Log("tick_only_no_match", logging.F("name", *only))
		return 2
	}
	return 0
}

func gatherSnapshots(ctx context.Context, perSource time.Duration) []trigger.Source {
	srcs := []adapter.QuotaSource{
		&claude_oauth.Adapter{},
		&codex_journal.Adapter{},
	}
	out := make([]trigger.Source, len(srcs))
	var wg sync.WaitGroup
	for i, s := range srcs {
		i, s := i, s
		out[i] = trigger.Source{Provider: s.Provider()}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, perSource)
			defer cancel()
			snap, err := s.Snapshot(sctx)
			if err != nil {
				out[i].Err = err
				return
			}
			out[i].Snap = snap
		}()
	}
	wg.Wait()
	return out
}

func logTriggerResult(r trigger.Result) {
	switch {
	case r.ParseErr != nil:
		logging.Log("trigger_parse_error", logging.F("name", r.Name), logging.F("err", r.ParseErr))
	case !r.Allowed:
		logging.Log("trigger_skipped", logging.F("name", r.Name), logging.F("reason", "when_false"))
	case r.DryRun:
		logging.Log("trigger_would_run", logging.F("name", r.Name))
	case r.ExecErr != nil:
		logging.Log("trigger_failed",
			logging.F("name", r.Name),
			logging.F("err", r.ExecErr),
			logging.F("duration_ms", r.Duration.Milliseconds()),
			logging.F("stderr", truncForLog(r.Stderr)),
		)
	default:
		logging.Log("trigger_ok",
			logging.F("name", r.Name),
			logging.F("duration_ms", r.Duration.Milliseconds()),
			logging.F("stdout_bytes", len(r.Stdout)),
		)
	}
}

func truncForLog(s string) string {
	const lim = 512
	if len(s) <= lim {
		return s
	}
	return s[:lim] + "…"
}
