package adapter

import (
	"context"
	"time"
)

type Window struct {
	UsedPercent float64    `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	Minutes     int        `json:"minutes"`
}

type UsageSnapshot struct {
	Provider          string            `json:"provider"`
	Plan              string            `json:"plan"`
	AsOf              time.Time         `json:"as_of"`
	Windows           map[string]Window `json:"windows"`
	SubBuckets        map[string]Window `json:"sub_buckets,omitempty"`
	ExtraUsageEnabled bool              `json:"extra_usage_enabled"`
	SourceNote        string            `json:"source_note,omitempty"`

	// ActiveLimit carries the gating limit from the newer limits[] array
	// (Claude). It is the active entry with the highest utilization (or a
	// warning severity). Nil when the provider does not expose limits[]
	// (e.g. codex, or older Claude responses parsed via the
	// five_hour/seven_day fallback). Cap-aware re-dispatch reads ResetsAt
	// to learn when the gating window clears, and Capped to learn whether
	// it is currently throttling.
	ActiveLimit *ActiveLimit `json:"active_limit,omitempty"`
}

// ActiveLimit is the currently-gating usage limit derived from the
// Claude /api/oauth/usage limits[] array. Used by cap-aware re-dispatch
// to know when the throttling window resets.
type ActiveLimit struct {
	// Kind is the limit's kind, e.g. "session", "weekly_all",
	// "weekly_scoped".
	Kind string `json:"kind"`
	// Group is the limit's group label, when present.
	Group string `json:"group,omitempty"`
	// UsedPercent is 0-1 (normalized from the API's 0-100 percent).
	UsedPercent float64 `json:"used_percent"`
	// Severity is the API-reported severity, e.g. "normal", "warning".
	Severity string `json:"severity,omitempty"`
	// Scope is the API-reported scope string, when present.
	Scope string `json:"scope,omitempty"`
	// ResetsAt is when this limit's window resets. Nil if not reported.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Capped is true when this limit is throttling now: severity
	// "warning", or utilization at/above 100%.
	Capped bool `json:"capped"`
}

type QuotaSource interface {
	Snapshot(ctx context.Context) (*UsageSnapshot, error)
	Provider() string
}
