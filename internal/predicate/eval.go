// Package predicate evaluates the dispatch guards: rolling-window
// utilization (short/long), inflight PR count, daily dispatch count,
// kill-switch presence, and time-of-day (active vs drain hours).
//
// Result is Decision{Allow, Reason} — never a bare bool, so the operator
// can always see *why* a dispatch was skipped.
package predicate

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/github"
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
	Cfg          *config.Config
	GH           *github.Client
	QuotaSources []adapter.QuotaSource
	Now          time.Time
}

// Evaluate runs all guards in order; returns the first that denies.
func Evaluate(ctx context.Context, in Inputs) Decision {
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
	if !inDrainWindow(now, cfg.Hours.Drain) {
		isActive := inDrainWindow(now, cfg.Hours.Active)
		if isActive {
			// During active hours we still allow if utilization is fresh.
			// That's handled in the quota guards below — but if hours.Drain
			// excluded right now, only fresh quota saves us.
		} else {
			return deny("outside_drain_hours (now=%s drain=%s active=%s)",
				now.Format("15:04"), cfg.Hours.Drain, cfg.Hours.Active)
		}
	}
	isActiveHour := inDrainWindow(now, cfg.Hours.Active)

	// 3. Quota windows — every provider must clear short_window guard.
	for _, src := range in.QuotaSources {
		snap, err := src.Snapshot(ctx)
		if err != nil {
			// Soft-deny: don't dispatch if we can't read quota.
			return deny("quota_read_failed: %s: %v", src.Provider(), err)
		}
		if w, ok := snap.Windows["short"]; ok {
			ceiling := cfg.Guards.ShortWindowIdle
			if isActiveHour {
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
	}

	// 4. Inflight PR count.
	if in.GH != nil && len(cfg.Repos) > 0 {
		inflight, err := in.GH.CountOpenInflight(ctx, cfg.Repos)
		if err != nil {
			return deny("inflight_count_failed: %v", err)
		}
		if inflight >= cfg.Guards.InflightPRs {
			return deny("inflight_cap_hit (%d/%d open PRs)", inflight, cfg.Guards.InflightPRs)
		}

		// 5. Daily dispatch cap.
		if cfg.Safety.MaxPerDay > 0 {
			today, err := in.GH.CountDispatchedToday(ctx, cfg.Repos)
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

func parseHHMM(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
