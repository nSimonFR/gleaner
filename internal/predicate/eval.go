// Package predicate evaluates the dispatch guards: rolling-window
// utilization (short/long), inflight PR count, daily dispatch count,
// kill-switch presence, and time-of-day (active vs drain hours).
//
// Result is Decision{Allow, Reason} — never a bare bool, so the operator
// can always see *why* a dispatch was skipped.
//
// Milestone A: the inflight/daily counters are codehost concerns (PRs live
// on GitHub regardless of which tracker drives issues), so this package
// takes a *codehost.Client and a repos slice rather than the old
// `*github.Client` direct dependency.
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
	//   - In drain window: proceed with idle threshold (more permissive).
	//   - In active window: proceed with active threshold (stricter).
	//   - In neither: deny outright.
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	inDrain := inDrainWindow(now, cfg.Hours.Drain)
	isActiveHour := inDrainWindow(now, cfg.Hours.Active)
	if !inDrain && !isActiveHour {
		return deny("outside_drain_hours (now=%s drain=%s active=%s)",
			now.Format("15:04"), cfg.Hours.Drain, cfg.Hours.Active)
	}

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
	if in.CodeHost != nil && len(in.CodehostRepos) > 0 {
		inflight, err := in.CodeHost.CountOpenInflight(ctx, in.CodehostRepos)
		if err != nil {
			return deny("inflight_count_failed: %v", err)
		}
		if inflight >= cfg.Guards.InflightPRs {
			return deny("inflight_cap_hit (%d/%d open PRs)", inflight, cfg.Guards.InflightPRs)
		}

		// 5. Daily dispatch cap.
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
