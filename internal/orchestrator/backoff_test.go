package orchestrator

import (
	"testing"
	"time"
)

// TestBackoff_Schedule asserts the SPEC §8.4 schedule:
//
//	attempt=1 → 10s
//	attempt=2 → 20s
//	attempt=3 → 40s
//	attempt=4 → 80s
//	attempt=5 → 160s
//	attempt=6 → 300s (capped at 5min default)
//	attempt=99 → 300s (still capped, no overflow)
func TestBackoff_Schedule(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 80 * time.Second},
		{5, 160 * time.Second},
		{6, 5 * time.Minute},  // 320s > 300s cap
		{99, 5 * time.Minute}, // overflow guard
	}
	for _, c := range cases {
		got := Backoff(c.attempt, 5*time.Minute)
		if got != c.want {
			t.Errorf("Backoff(%d) = %s, want %s", c.attempt, got, c.want)
		}
	}
}

// TestBackoff_CustomMax: a smaller max (e.g. 30s) caps earlier.
func TestBackoff_CustomMax(t *testing.T) {
	if got := Backoff(3, 30*time.Second); got != 30*time.Second {
		t.Errorf("Backoff(3, 30s) = %s, want 30s (40s > cap)", got)
	}
}

// TestBackoff_ZeroMaxFallsBackToDefault: a zero/negative cap uses the
// SPEC default 5min.
func TestBackoff_ZeroMaxFallsBackToDefault(t *testing.T) {
	if got := Backoff(6, 0); got != 5*time.Minute {
		t.Errorf("Backoff(6, 0) = %s, want default cap 5min", got)
	}
	if got := Backoff(6, -1); got != 5*time.Minute {
		t.Errorf("Backoff(6, -1) = %s, want default cap 5min", got)
	}
}

// TestBackoff_Attempt0Treated as 1: defensive against off-by-one
// callers; first retry is "attempt=1" with 10s delay.
func TestBackoff_AttemptBelowOne(t *testing.T) {
	if got := Backoff(0, 5*time.Minute); got != 10*time.Second {
		t.Errorf("Backoff(0) = %s, want 10s (clamped to attempt=1)", got)
	}
	if got := Backoff(-3, 5*time.Minute); got != 10*time.Second {
		t.Errorf("Backoff(-3) = %s, want 10s (clamped to attempt=1)", got)
	}
}
