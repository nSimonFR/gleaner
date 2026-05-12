package orchestrator

import "time"

// Default backoff bounds (SPEC §8.4).
const (
	defaultInitialBackoff = 10 * time.Second
	defaultMaxBackoff     = 5 * time.Minute
)

// Backoff returns the delay before the (attempt+1)-th retry. SPEC §8.4:
//
//	delay = min(10000ms * 2^(attempt - 1), agent.max_retry_backoff_ms)
//
// attempt is 1-indexed (the first retry is attempt=1 with 10s delay).
// Caller passes maxBackoff from config.Agent.MaxRetryBackoff; pass 0
// for the SPEC default (5 minutes).
//
// SPEC also defines a 1s "continuation retry" for normal exits; gleaner
// doesn't distinguish those today (every retry is a failure retry),
// so this function covers all paths.
func Backoff(attempt int, maxBackoff time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if maxBackoff <= 0 {
		maxBackoff = defaultMaxBackoff
	}
	// 10s * 2^(attempt-1). Computed in int64 nanos with an overflow
	// guard: at attempt ~63 the shift overflows; cap well below that.
	shift := uint(attempt - 1)
	if shift > 30 { // 10s << 30 ≈ 10.7 thousand seconds; well beyond max
		return maxBackoff
	}
	d := defaultInitialBackoff << shift
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}
