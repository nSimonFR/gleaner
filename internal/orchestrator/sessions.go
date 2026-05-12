package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
)

// NewSession returns a fresh Session ID. SPEC §13.1 requires a
// `session_id` field on every log line and HTTP /api/v1/state entry.
// Symphony's Codex-flavored shape is `<thread_id>-<turn_id>`; gleaner
// is coding-agent-agnostic so we mint our own:
//
//	<tracker_kind>:<issue.ID>:<attempt>:<random4>
//
// e.g.  github:nSimonFR/nic-os#60:1:7f3a
//
// Attempt is 1 on the first run, 2 on the first retry, etc. The
// random suffix is 4 hex chars (16 bits) — collision-vanishingly-
// unlikely within a single host's process lifetime.
//
// Continuation retries should reuse the Session (matches Symphony
// parity); only fresh-from-tracker dispatches get a new Session.
// Today gleaner doesn't distinguish "continuation" vs "failure
// retry" — every retry mints a new Session. Milestone D may refine.
func NewSession(t tracker.Tracker, iss tracker.Issue, attempt int) Session {
	return Session{
		ID:        fmt.Sprintf("%s:%s:%d:%s", t.Kind(), iss.ID, attempt, randHex(4)),
		StartedAt: time.Now(),
	}
}

func randHex(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure on Linux is essentially impossible (no
		// entropy source means a much bigger problem than this fallback
		// can fix). Return a deterministic placeholder so the caller
		// gets a valid string anyway.
		return "deadbeef"[:nBytes*2]
	}
	return hex.EncodeToString(buf)
}
