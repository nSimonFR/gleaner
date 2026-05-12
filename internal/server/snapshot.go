package server

import (
	"context"
	"net/url"
	"time"

	"github.com/nSimonFR/gleaner/internal/orchestrator"
	"github.com/nSimonFR/gleaner/internal/predicate"
)

// Snapshot is the JSON shape served by GET /api/v1/state. Adapted from
// Symphony SPEC §13.7.2 — gleaner-flavored differences:
//
//   - `tracker.kind` exposes which adapter is wired (github | linear).
//   - `quota` replaces SPEC's codex_totals: gleaner has used_percent
//     metrics, not running token totals.
//   - `predicate` exposes the tick-level guard decision.
//   - `inflight_prs` / `merged_this_week` / `last_skip_reason` carry
//     the gleaner-native v0.0.4 metrics from the original plan.
type Snapshot struct {
	GeneratedAt    time.Time             `json:"generated_at"`
	Tracker        TrackerInfo           `json:"tracker"`
	Counts         Counts                `json:"counts"`
	Predicate      PredicateDecision     `json:"predicate"`
	InflightPRs    int                   `json:"inflight_prs"`
	MergedThisWeek int                   `json:"merged_this_week"`
	Running        []RunningEntry        `json:"running"`
	Retrying       []RetryingEntry       `json:"retrying"`
	Quota          map[string]QuotaInfo  `json:"quota"`
	RateLimits     any                   `json:"rate_limits"` // null today; reserved for future
}

type TrackerInfo struct {
	Kind string `json:"kind"`
}

type Counts struct {
	Running  int `json:"running"`
	Retrying int `json:"retrying"`
}

type PredicateDecision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason"`
}

type RunningEntry struct {
	IssueID         string    `json:"issue_id"`
	IssueIdentifier string    `json:"issue_identifier"`
	SessionID       string    `json:"session_id"`
	Profile         string    `json:"profile"`
	Workspace       string    `json:"workspace,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	LastEventAt     time.Time `json:"last_event_at"`
	LastMessage     string    `json:"last_message,omitempty"`
}

type RetryingEntry struct {
	IssueID         string    `json:"issue_id"`
	IssueIdentifier string    `json:"issue_identifier"`
	Attempt         int       `json:"attempt"`
	DueAt           time.Time `json:"due_at"`
	Error           string    `json:"error"`
}

// QuotaInfo mirrors a single provider's short+long window snapshot.
type QuotaInfo struct {
	ShortPct      float64 `json:"short_pct"`
	LongPct       float64 `json:"long_pct"`
	ShortResetIn  int64   `json:"short_reset_s"`
	LongResetIn   int64   `json:"long_reset_s"`
}

// BuildSnapshot composes the current orchestrator + tracker + codehost
// + quota state into a Snapshot. Every call re-queries dependencies;
// safe to call from any handler.
func (s *Server) BuildSnapshot(ctx context.Context) Snapshot {
	now := time.Now()
	snap := Snapshot{
		GeneratedAt: now.UTC(),
		Tracker:     TrackerInfo{Kind: s.Tracker.Kind()},
		Quota:       map[string]QuotaInfo{},
	}

	// Counts + running/retrying.
	running := s.State.SnapshotRunning()
	retries := s.State.SnapshotRetries()
	snap.Counts.Running = len(running)
	snap.Counts.Retrying = len(retries)
	for _, w := range running {
		snap.Running = append(snap.Running, RunningEntry{
			IssueID:         w.Issue.ID,
			IssueIdentifier: w.Issue.Identifier,
			SessionID:       w.Session.ID,
			Profile:         profileName(w),
			Workspace:       w.Workspace,
			StartedAt:       w.StartedAt,
			LastEventAt:     w.LastEvent,
			LastMessage:     w.LastMessage,
		})
	}
	for _, r := range retries {
		snap.Retrying = append(snap.Retrying, RetryingEntry{
			IssueID:         r.Issue.ID,
			IssueIdentifier: r.Issue.Identifier,
			Attempt:         r.Attempt,
			DueAt:           r.DueAt,
			Error:           r.LastError,
		})
	}

	// Predicate (cheap, no quota loop).
	dec := predicate.EvaluateGlobal(ctx, predicate.Inputs{
		Cfg:           s.Cfg,
		CodeHost:      s.CodeHost,
		CodehostRepos: s.CodehostRepos,
		Now:           now,
	})
	snap.Predicate = PredicateDecision{Allow: dec.Allow, Reason: dec.Reason}

	// Codehost counts. Best-effort: errors → leave at 0. Each remote
	// call gets its own short deadline so a slow gh shell doesn't
	// stall the dashboard.
	if s.CodeHost != nil && len(s.CodehostRepos) > 0 {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if n, err := s.CodeHost.CountOpenInflight(cctx, s.CodehostRepos); err == nil {
			snap.InflightPRs = n
		}
		cancel()
		cctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		if n, err := s.CodeHost.MergedThisWeek(cctx, s.CodehostRepos); err == nil {
			snap.MergedThisWeek = n
		}
		cancel()
	}

	// Quota per provider. Best-effort, per-source deadline so one
	// stuck adapter doesn't hang the dashboard for the others.
	for name, src := range s.QuotaSources {
		qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		usage, err := src.Snapshot(qctx)
		cancel()
		if err != nil || usage == nil {
			continue
		}
		qi := QuotaInfo{}
		if w, ok := usage.Windows["short"]; ok {
			qi.ShortPct = w.UsedPercent
			if w.ResetsAt != nil {
				qi.ShortResetIn = int64(time.Until(*w.ResetsAt).Seconds())
			}
		}
		if w, ok := usage.Windows["long"]; ok {
			qi.LongPct = w.UsedPercent
			if w.ResetsAt != nil {
				qi.LongResetIn = int64(time.Until(*w.ResetsAt).Seconds())
			}
		}
		snap.Quota[name] = qi
	}

	return snap
}

func profileName(w orchestrator.Worker) string {
	if w.Profile == nil {
		return ""
	}
	return w.Profile.Name
}

// issueRunningJSON renders the per-issue body for an issue currently
// in state.Running. SPEC §13.7 GET /api/v1/<id>.
func issueRunningJSON(w orchestrator.Worker) map[string]any {
	return map[string]any{
		"issue_id":         w.Issue.ID,
		"issue_identifier": w.Issue.Identifier,
		"status":           "running",
		"workspace":        map[string]any{"path": w.Workspace},
		"attempts": map[string]any{
			"restart_count":          0, // gleaner restarts are per-process, not per-issue; always 0 on a single host
			"current_retry_attempt":  0, // no retry is pending when we're running
		},
		"running": map[string]any{
			"session_id":    w.Session.ID,
			"profile":       profileName(w),
			"started_at":    w.StartedAt,
			"last_event_at": w.LastEvent,
			"last_message":  w.LastMessage,
		},
		"retry":      nil,
		"last_error": nil,
	}
}

// issueRetryJSON renders the per-issue body for an issue currently
// queued for retry.
func issueRetryJSON(r orchestrator.RetryAttempt) map[string]any {
	return map[string]any{
		"issue_id":         r.Issue.ID,
		"issue_identifier": r.Issue.Identifier,
		"status":           "retrying",
		"workspace":        map[string]any{"path": ""},
		"attempts": map[string]any{
			"restart_count":         0,
			"current_retry_attempt": r.Attempt,
		},
		"running": nil,
		"retry": map[string]any{
			"attempt": r.Attempt,
			"due_at":  r.DueAt,
		},
		"last_error": r.LastError,
	}
}

func urlPathUnescape(s string) (string, error) { return url.PathUnescape(s) }
