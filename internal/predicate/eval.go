// Package predicate evaluates the picker guards: kill-switch, hours of
// day, and per-provider quota window utilization.
//
// Result is Decision{Allow, Reason} — never a bare bool, so the operator
// can always see *why* a pick was skipped.
package predicate

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
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
	QuotaSources []adapter.QuotaSource
	Now          time.Time
}

// IsActiveHour returns true if `now` falls in the configured active
// window (stricter quota ceiling applies).
func IsActiveHour(now time.Time, hoursCfg config.Hours) bool {
	if now.IsZero() {
		now = time.Now()
	}
	return inWindow(now, hoursCfg.Active)
}

// EvaluateGlobal runs the tick-level guards: kill switch + hours.
func EvaluateGlobal(ctx context.Context, in Inputs) Decision {
	cfg := in.Cfg

	if cfg.Safety.KillSwitch != "" {
		if _, err := os.Stat(cfg.Safety.KillSwitch); err == nil {
			return deny("kill_switch present at %s", cfg.Safety.KillSwitch)
		}
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	inDrain := inWindow(now, cfg.Hours.Drain)
	isActive := inWindow(now, cfg.Hours.Active)
	if !inDrain && !isActive {
		return deny("outside_active_and_drain_hours (now=%s drain=%s active=%s)",
			now.Format("15:04"), cfg.Hours.Drain, cfg.Hours.Active)
	}

	return allow()
}

// EvaluateQuota checks one provider's short and long window against the
// configured ceilings. `isActive` selects the stricter ceiling.
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

// Evaluate runs global + every quota source; returns the first denial.
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

// inWindow returns true if `now`'s wall-clock time falls inside the
// "HH:MM-HH:MM" range. Handles wrap-around (e.g. 22:00-07:00). Empty
// range returns true (no restriction).
func inWindow(now time.Time, rng string) bool {
	if rng == "" {
		return true
	}
	start, end, ok := parseTimeRange(rng)
	if !ok {
		return true
	}
	cur := now.Hour()*60 + now.Minute()
	if start <= end {
		return cur >= start && cur < end
	}
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
