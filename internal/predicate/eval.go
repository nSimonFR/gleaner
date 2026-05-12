// Package predicate evaluates the dispatch guards: rolling-window
// utilization (short/long), inflight PR count, daily dispatch count,
// kill-switch presence, and time-of-day (active vs drain hours).
//
// Result is Decision{Allow, Reason} — never a bare bool, so the operator
// can always see *why* a dispatch was skipped.
//
// Milestone C: the predicate is split into a *global* tick-level check
// (EvaluateGlobal — kill, hours, inflight, daily) and a *per-provider*
// quota check (EvaluateQuota — short/long windows for one provider).
// The orchestrator runs EvaluateGlobal once per tick, then
// EvaluateQuota per candidate's profile.Plan. This lets Codex dispatch
// while Claude is over its short ceiling (and vice versa) — the
// agent-agnostic value-add.
//
// The legacy `Evaluate` function is kept as a thin wrapper for drain's
// single-shot dispatch path; it does global + a single all-providers
// pass like v0.0.3.
package predicate

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	codehost "github.com/nSimonFR/gleaner/internal/adapter/codehost/github"
	"github.com/nSimonFR/gleaner/internal/config"
)

type Decision struct {
	Allow  bool
	Reason string
}

func deny(format string, args ...any) Decision {
	return Decision{Allow: false, Reason: fmt.Sprintf(format, args...)}
}

func allow() Decision {
	return Decision{Allow: true, Reason: "ok"}
}

// Inputs bundles everything Evaluate needs.
type Inputs struct {
	Cfg           *config.Config
	CodeHost      *codehost.Client // for inflight + daily counts
	CodehostRepos []string         // repo(s) PRs count against
	QuotaSources  []adapter.QuotaSource
	Now           time.Time
}

// IsActiveHour returns true if `now` falls in the configured active
// window (stricter quota ceilings apply). Exported so the orchestrator
// can pass the same boolean to EvaluateQuota without re-parsing.
func IsActiveHour(now time.Time, hoursCfg config.Hours) bool {
	if now.IsZero() {
		now = time.Now()
	}
	return inDrainWindow(now, hoursCfg.Active)
}

// EvaluateGlobal runs the tick-level guards (kill switch, hours,
// inflight, daily). It does NOT touch QuotaSources — call EvaluateQuota
// per candidate before dispatch.
//
// Returns Decision{Allow: true} when the tick may proceed to consider
// candidates. The caller still has to clear EvaluateQuota for each
// candidate's specific provider.
func EvaluateGlobal(ctx context.Context, in Inputs) Decision {
	cfg := in.Cfg

	// 1. Kill switch — cheapest check, must be first.
	if cfg.Safety.KillSwitch != "" {
		if _, err := os.Stat(cfg.Safety.KillSwitch); err == nil {
			return deny("kill_switch present at %s", cfg.Safety.KillSwitch)
		}
	}

	// 2. Hours of day.
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	inDrain := inDrainWindow(now, cfg.Hours.Drain)
	isActive := inDrainWindow(now, cfg.Hours.Active)
	if !inDrain && !isActive {
		return deny("outside_drain_hours (now=%s drain=%s active=%s)",
			now.Format("15:04"), cfg.Hours.Drain, cfg.Hours.Active)
	}

	// 3. Inflight PR count + daily cap.
	if in.CodeHost != nil && len(in.CodehostRepos) > 0 {
		inflight, err := in.CodeHost.CountOpenInflight(ctx, in.CodehostRepos)
		if err != nil {
			return deny("inflight_count_failed: %v", err)
		}
		if inflight >= cfg.Guards.InflightPRs {
			return deny("inflight_cap_hit (%d/%d open PRs)", inflight, cfg.Guards.InflightPRs)
		}
		if cfg.Safety.MaxPerDay > 0 {
			today, err := in.CodeHost.CountDispatchedToday(ctx, in.CodehostRepos)
			if err != nil {
				return deny("daily_count_failed: %v", err)
			}
			if today >= cfg.Safety.MaxPerDay {
				return deny("daily_cap_hit (%d/%d)", today, cfg.Safety.MaxPerDay)
			}
		}
	}

	return allow()
}

// EvaluateQuota checks one provider's short and long window against the
// configured ceilings. `isActive` selects the stricter ceiling
// (Guards.ShortWindowActive) vs the more permissive
// (Guards.ShortWindowIdle).
//
// Used by the orchestrator before dispatching a candidate: the
// candidate's profile.Plan picks which QuotaSource to evaluate.
func EvaluateQuota(ctx context.Context, src adapter.QuotaSource, cfg *config.Config, isActive bool) Decision {
	snap, err := src.Snapshot(ctx)
	if err != nil {
		return deny("quota_read_failed: %s: %v", src.Provider(), err)
	}
	if w, ok := snap.Windows["short"]; ok {
		ceiling := cfg.Guards.ShortWindowIdle
		if isActive {
			ceiling = cfg.Guards.ShortWindowActive
		}
		if w.UsedPercent > ceiling {
			return deny("short_window_ceiling_hit (%s short=%.0f%% > %.0f%%)",
				src.Provider(), w.UsedPercent*100, ceiling*100)
		}
	}
	if w, ok := snap.Windows["long"]; ok {
		if w.UsedPercent > cfg.Guards.LongWindowCeiling {
			return deny("long_window_ceiling_hit (%s long=%.0f%% > %.0f%%)",
				src.Provider(), w.UsedPercent*100, cfg.Guards.LongWindowCeiling*100)
		}
	}
	return allow()
}

// Evaluate runs all guards in order; returns the first that denies.
// Legacy v0.0.x entry point — `drain --once` and `bootstrap` use it.
// New orchestrator code uses EvaluateGlobal + EvaluateQuota separately
// for per-provider routing.
func Evaluate(ctx context.Context, in Inputs) Decision {
	d := EvaluateGlobal(ctx, in)
	if !d.Allow {
		return d
	}
	isActive := IsActiveHour(in.Now, in.Cfg.Hours)
	for _, src := range in.QuotaSources {
		if d := EvaluateQuota(ctx, src, in.Cfg, isActive); !d.Allow {
			return d
		}
	}
	return allow()
}

// inDrainWindow returns true if `now`'s wall-clock time falls inside the
// "HH:MM-HH:MM" range. Handles wrap-around (e.g. 22:00-07:00).
// Empty range returns true (no restriction).
func inDrainWindow(now time.Time, rng string) bool {
	if rng == "" {
		return true
	}
	start, end, ok := parseTimeRange(rng)
	if !ok {
		return true // permissive on malformed
	}
	cur := now.Hour()*60 + now.Minute()
	if start <= end {
		return cur >= start && cur < end
	}
	// wrap-around: e.g. 22:00 → 07:00
	return cur >= start || cur < end
}

func parseTimeRange(rng string) (start, end int, ok bool) {
	for i := 0; i < len(rng); i++ {
		if rng[i] == '-' {
			s, ok1 := parseHHMM(rng[:i])
			e, ok2 := parseHHMM(rng[i+1:])
			if ok1 && ok2 {
				return s, e, true
			}
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// parseHHMM accepts H:MM or HH:MM. Users routinely write "9:00".
func parseHHMM(s string) (int, bool) {
	colon := -1
	for i, c := range s {
		if c == ':' {
			colon = i
			break
		}
	}
	if colon < 1 || len(s)-colon-1 != 2 {
		return 0, false
	}
	hStr := s[:colon]
	mStr := s[colon+1:]
	h := 0
	for _, c := range hStr {
		if c < '0' || c > '9' {
			return 0, false
		}
		h = h*10 + int(c-'0')
	}
	if mStr[0] < '0' || mStr[0] > '9' || mStr[1] < '0' || mStr[1] > '9' {
		return 0, false
	}
	m := int(mStr[0]-'0')*10 + int(mStr[1]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
