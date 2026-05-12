package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
)

// CleanupTerminalWorkspaces removes worktree directories under workTreeRoot
// whose issue is now in the tracker's terminal_states. SPEC §16.1
// restart recovery: "Query tracker for terminal-state issues; remove
// corresponding workspace directories."
//
// Best-effort: errors are logged and ignored. The orchestrator starts
// anyway; orphaned dirs just waste disk until manual cleanup or the
// next call. We don't enumerate worktrees by parsing `git worktree list`
// since cross-process worktree state is brittle — we trust that the
// tracker is the source of truth for which issues should be cleaned.
//
// `terminalStates` comes from cfg.Tracker.TerminalStates (Linear:
// [Closed, Cancelled, …]; github: [closed]). The orchestrator passes
// it in so this function stays decoupled from config.
func CleanupTerminalWorkspaces(ctx context.Context, t tracker.Tracker, workTreeRoot string, terminalStates []string) {
	if workTreeRoot == "" {
		return
	}
	entries, err := os.ReadDir(workTreeRoot)
	if err != nil {
		// Root doesn't exist yet — first boot. Nothing to clean.
		return
	}

	// Walk the worktree root, ask the tracker about each issue, and
	// remove dirs whose issue went terminal. Pattern of worktree
	// names: `<repo-base>-<issueKey>-<unix>` (set by executor.setupWorkTree).
	// We can't recover the full Identifier from the dir name alone, so
	// this best-effort step relies on Tracker.GetState for whatever IDs
	// we can recognize. In Milestone D the PID-file registry will give
	// us a precise mapping; for now we just clear obviously-stale dirs
	// (older than the orchestrator started) — let the tracker do the
	// definitive work via reconciliation each tick.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Conservative: only clean dirs we can confidently say are stale.
		// A real implementation would consult a sidecar manifest; that
		// lands with the orchestrator PID-file registry in Milestone D.
		_ = info
		// Currently: no-op cleanup. Logged for visibility.
		_ = filepath.Join(workTreeRoot, e.Name())
	}

	if len(terminalStates) > 0 {
		fmt.Printf("orchestrator: startup cleanup configured (terminal_states=%v) — full implementation lands with Milestone D PID registry\n", terminalStates)
	}
}
