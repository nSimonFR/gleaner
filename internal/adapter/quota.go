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
}

type QuotaSource interface {
	Snapshot(ctx context.Context) (*UsageSnapshot, error)
	Provider() string
}
