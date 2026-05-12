package orchestrator

import (
	"context"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/logging"
)

// CleanupTerminalWorkspaces is a Milestone C/D STUB. SPEC §16.1 calls
// for startup cleanup of workspace directories whose issues went
// terminal while the orchestrator was offline. A correct
// implementation needs a persistent (issue.ID → workspace path)
// mapping so we can call `Tracker.GetState` per dir without depending
// on the filesystem-only `<repo>-<issueKey>-<unix>` convention.
//
// That mapping lands with Milestone E's per-issue API (which exposes
// the running issue → workspace mapping). Until then this function
// logs a one-liner and returns. The trade-off is the SPEC-allowed
// "best-effort cleanup can miss orphans" path: stale dirs accumulate
// slowly until manual cleanup.
func CleanupTerminalWorkspaces(ctx context.Context, t tracker.Tracker, workTreeRoot string, terminalStates []string) {
	if workTreeRoot == "" || len(terminalStates) == 0 {
		return
	}
	logging.Log("startup_cleanup_deferred",
		logging.F("tracker_kind", t.Kind()),
		logging.F("workspace_root", workTreeRoot))
}
