package orchestrator

import (
	"context"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/logging"
)

// SetStateBestEffort calls trk.SetState and logs the outcome. Errors are
// swallowed: board write-back is cosmetic, must never fail the dispatch.
// When stateName is empty, the call is skipped silently (operator opted out
// by clearing tracker.in_progress_state or tracker.review_state).
//
// Exported because cmd/gleaner/drain.go mirrors the same two transition
// points to keep drain/serve behaviorally identical. SPEC §7.1.
func SetStateBestEffort(ctx context.Context, trk tracker.Tracker, iss tracker.Issue, stateName, sessionID string) {
	if stateName == "" || trk == nil {
		return
	}
	if err := trk.SetState(ctx, iss.ID, stateName); err != nil {
		logging.Log("set_state_failed",
			logging.F("issue_id", iss.ID),
			logging.F("issue_identifier", iss.Identifier),
			logging.F("session_id", sessionID),
			logging.F("state", stateName),
			logging.F("err", err))
		return
	}
	logging.Log("set_state",
		logging.F("issue_id", iss.ID),
		logging.F("issue_identifier", iss.Identifier),
		logging.F("session_id", sessionID),
		logging.F("state", stateName))
}
