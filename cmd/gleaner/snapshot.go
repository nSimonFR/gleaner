package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/claude_oauth"
	"github.com/nSimonFR/gleaner/internal/adapter/codex_journal"
)

func snapshotCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON for machine consumption")
	timeoutSec := fs.Int("timeout", 10, "per-adapter timeout in seconds")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	srcs := []adapter.QuotaSource{
		&claude_oauth.Adapter{},
		&codex_journal.Adapter{},
	}

	results := make([]snapshotResult, len(srcs))
	for i, s := range srcs {
		sctx, cancel := context.WithTimeout(ctx, time.Duration(*timeoutSec)*time.Second)
		snap, err := s.Snapshot(sctx)
		cancel()
		results[i] = snapshotResult{Provider: s.Provider()}
		if err != nil {
			results[i].Error = err.Error()
			continue
		}
		results[i].Snapshot = snap
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		return 0
	}

	for _, r := range results {
		printHuman(r)
	}
	return 0
}

type snapshotResult struct {
	Provider string                  `json:"provider"`
	Snapshot *adapter.UsageSnapshot  `json:"snapshot,omitempty"`
	Error    string                  `json:"error,omitempty"`
}

func printHuman(r snapshotResult) {
	fmt.Printf("%s", r.Provider)
	if r.Error != "" {
		fmt.Printf(":\n  error: %s\n", r.Error)
		return
	}
	s := r.Snapshot
	if s == nil {
		fmt.Println(": (no data)")
		return
	}
	fmt.Printf(" (%s)", s.Plan)
	if s.SourceNote != "" {
		fmt.Printf("   [source: %s]", s.SourceNote)
	}
	fmt.Println(":")

	// Stable ordering: short before long
	order := []string{"short", "long"}
	for _, k := range order {
		if w, ok := s.Windows[k]; ok {
			fmt.Printf("  %-6s %s   %s\n", k, formatPct(w.UsedPercent), formatReset(w.ResetsAt))
		}
	}
	for k := range s.Windows {
		if k == "short" || k == "long" {
			continue
		}
		w := s.Windows[k]
		fmt.Printf("  %-6s %s   %s\n", k, formatPct(w.UsedPercent), formatReset(w.ResetsAt))
	}

	if len(s.SubBuckets) > 0 {
		keys := make([]string, 0, len(s.SubBuckets))
		for k := range s.SubBuckets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("  sub-buckets:")
		for _, k := range keys {
			fmt.Printf(" %s %s /", k, formatPct(s.SubBuckets[k].UsedPercent))
		}
		fmt.Println()
	}
	if s.ExtraUsageEnabled {
		fmt.Println("  extra_usage: enabled")
	}
}

func formatPct(p float64) string {
	return fmt.Sprintf("%5.1f%%", p*100)
}

func formatReset(t *time.Time) string {
	if t == nil {
		return "(no reset reported)"
	}
	d := time.Until(*t)
	if d < 0 {
		return "(reset due)"
	}
	if d < time.Hour {
		return fmt.Sprintf("resets in %dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("resets in %dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("resets in %dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}
