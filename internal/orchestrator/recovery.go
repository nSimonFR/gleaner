package orchestrator

import (
	"context"
	"fmt"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
)

// CleanupTerminalWorkspaces is a Milestone C STUB. SPEC §16.1 calls for
// startup cleanup of workspace directories whose issues went terminal
// while the orchestrator was offline. A correct implementation needs a
// persistent (issue.ID → workspace path) mapping so we can call
// `Tracker.GetState` per dir without depending on the filesystem-only
// `<repo>-<issueKey>-<unix>` convention.
//
// That mapping lands with Milestone D's PID-file registry under
// /var/lib/gleaner/active/. Until then this function logs a one-liner
// and returns. The trade-off is the SPEC-allowed "best-effort cleanup
// can miss orphans" path: stale dirs accumulate slowly until manual
// cleanup or the next milestone.
func CleanupTerminalWorkspaces(ctx context.Context, t tracker.Tracker, workTreeRoot string, terminalStates []string) {
	if workTreeRoot == "" || len(terminalStates) == 0 {
		return
	}
	fmt.Printf("orchestrator: startup cleanup deferred to Milestone D (tracker_kind=%s root=%s)\n",
		t.Kind(), workTreeRoot)
}
