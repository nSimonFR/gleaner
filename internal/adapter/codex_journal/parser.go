// Package codex_journal reads Codex CLI's session journals to extract the
// most recent rate-limit snapshot. Codex pre-computes used_percent, so this
// adapter is almost pure decoding — no token-cap math required.
//
// Layout: ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
// Each token_count event is one line:
//
//	{"type":"event_msg","payload":{"type":"token_count",
//	 "rate_limits":{
//	    "primary":   {"used_percent": 1.0, "window_minutes": 300,   "resets_at": 1778434491},
//	    "secondary": {"used_percent": 0.0, "window_minutes": 10080, "resets_at": 1779021291},
//	    ...
//	 }}}
//
// We pick the globally latest event across all session files modified within
// the long-window horizon. mtime-filtering keeps the walk bounded.
package codex_journal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
)

type Adapter struct {
	SessionsDir string // default: ~/.codex/sessions
	Plan        string // "plus" by default; informational only — quota math is server-side
}

func (a *Adapter) Provider() string { return "codex" }

func (a *Adapter) Snapshot(ctx context.Context) (*adapter.UsageSnapshot, error) {
	dir := a.SessionsDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("UserHomeDir: %w", err)
		}
		dir = filepath.Join(home, ".codex", "sessions")
	}
	plan := a.Plan
	if plan == "" {
		plan = "plus"
	}

	cutoff := time.Now().Add(-8 * 24 * time.Hour) // long window is 7d; one day slack
	latest, latestTime, latestPath, err := walkLatest(ctx, dir, cutoff)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return &adapter.UsageSnapshot{
			Provider:   "codex",
			Plan:       plan,
			AsOf:       time.Now(),
			Windows:    map[string]adapter.Window{},
			SourceNote: fmt.Sprintf("no token_count events found in %s within last 8 days", dir),
		}, nil
	}

	snap := &adapter.UsageSnapshot{
		Provider:   "codex",
		Plan:       plan,
		AsOf:       latestTime,
		Windows:    map[string]adapter.Window{},
		SourceNote: filepath.Base(latestPath),
	}
	if latest.Primary != nil {
		snap.Windows["short"] = makeWindow(*latest.Primary)
	}
	if latest.Secondary != nil {
		snap.Windows["long"] = makeWindow(*latest.Secondary)
	}
	if latest.Credits != nil {
		snap.ExtraUsageEnabled = latest.Credits.HasCredits || latest.Credits.Unlimited
	}
	return snap, nil
}

type rateLimits struct {
	Primary   *rlWindow `json:"primary"`
	Secondary *rlWindow `json:"secondary"`
	Credits   *struct {
		HasCredits bool     `json:"has_credits"`
		Unlimited  bool     `json:"unlimited"`
		Balance    *float64 `json:"balance"`
	} `json:"credits"`
	PlanType *string `json:"plan_type"`
}

type rlWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"` // unix seconds
}

func makeWindow(rw rlWindow) adapter.Window {
	w := adapter.Window{
		UsedPercent: rw.UsedPercent / 100.0,
		Minutes:     rw.WindowMinutes,
	}
	if rw.ResetsAt > 0 {
		t := time.Unix(rw.ResetsAt, 0)
		w.ResetsAt = &t
	}
	return w
}

type eventEnvelope struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string      `json:"type"`
		RateLimits *rateLimits `json:"rate_limits"`
	} `json:"payload"`
}

// walkLatest finds the most-recent token_count event across all session files
// modified since cutoff. Returns the parsed rate_limits, its event-timestamp
// (parsed from .timestamp), and the source file path.
func walkLatest(ctx context.Context, dir string, cutoff time.Time) (*rateLimits, time.Time, string, error) {
	var (
		bestRL   *rateLimits
		bestTime time.Time
		bestPath string
	)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}

		rl, ts, found := scanFile(path)
		if !found {
			return nil
		}
		if ts.After(bestTime) {
			bestRL = rl
			bestTime = ts
			bestPath = path
		}
		return nil
	})
	if err != nil {
		return nil, time.Time{}, "", err
	}
	return bestRL, bestTime, bestPath, nil
}

// scanFile reads one session jsonl and returns the LAST token_count event
// observed (running total — last is the freshest within this session).
func scanFile(path string) (*rateLimits, time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	defer f.Close()

	var lastRL *rateLimits
	var lastTime time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		// Quick prefilter — avoid full unmarshal for non-matching lines.
		if !bytes.Contains(line, []byte(`"token_count"`)) {
			continue
		}
		var ev eventEnvelope
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "event_msg" || ev.Payload.Type != "token_count" || ev.Payload.RateLimits == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if err != nil {
			// Skip events with malformed timestamps; using time.Now() here
			// would let bad data win the "most recent" race.
			continue
		}
		lastRL = ev.Payload.RateLimits
		lastTime = t
	}
	if lastRL == nil {
		return nil, time.Time{}, false
	}
	return lastRL, lastTime, true
}

