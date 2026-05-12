// Package tracker abstracts the issue source so gleaner can drain a GitHub
// backlog or a Linear board with the same orchestrator code. Adapters live
// in sub-packages (github, linear). The Tracker interface mirrors Symphony
// SPEC §5.3 — same shape, gleaner-flavored.
//
// PR opening is NOT a Tracker concern: code lives on GitHub regardless of
// where issues live. See internal/adapter/codehost for that. Tracker.Comment
// is how a Linear-tracked workflow learns about the resulting GitHub PR.
package tracker

import (
	"context"
	"time"
)

// Issue is the gleaner-canonical issue shape — a superset of fields needed
// by the orchestrator, predicate, executor, and PR opener. Adapters fill in
// what their backend supports; missing fields are zero-valued.
type Issue struct {
	// ID is the tracker-native unique identifier (GraphQL node id, opaque
	// string). Stable across renames. Used as the in-memory state-machine key.
	ID string

	// Identifier is the human-readable label shown in logs, dashboards, PR
	// titles. GitHub: "owner/repo#N". Linear: "TEAM-123". Required.
	Identifier string

	// Repo is the GitHub repo to operate against (owner/name). For
	// kind=github, this comes from the issue's source repo. For
	// kind=linear, the adapter resolves it from config — Linear issues
	// don't carry a repo, so the Linear adapter resolves one per config
	// (`tracker.codehost_repo`, or derived from issue labels/team).
	Repo string

	// Number is the GitHub issue number; non-zero only when the tracker
	// is GitHub. Used by the PR opener to add a `Closes #N` line.
	Number int

	Title  string
	Body   string
	Labels []string

	// State is the tracker-native state name (GitHub: "open"/"closed"; Linear:
	// "Todo"/"In Progress"/"Done"). Compared against config.active_states /
	// terminal_states (SPEC §5.3) to decide reconciliation actions.
	State string

	// Priority follows Linear's 0=none, 1=urgent, 2=high, 3=medium, 4=low.
	// GitHub has no priority concept; the GitHub adapter leaves it at 0.
	//
	// CAUTION: 0 collides between "no priority set" (Linear) and "GitHub
	// default" — both produce the same value. The orchestrator's
	// SPEC §8.1 step 4 sort (priority asc) lands in Milestone C; until
	// then, do not sort by this field. When sort lands, treat 0 as
	// LOWEST priority (sort it last), not highest.
	Priority int

	// BranchName is a suggested branch (Linear ships these per issue;
	// GitHub: empty — caller derives). Used by the executor when set.
	BranchName string

	URL       string
	BlockedBy []string // tracker-native blocker IDs; SPEC §8.1 step 5

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Tracker is the issue-source contract. Implementations: github, linear.
//
// SPEC §5.3 maps to Kind(); §8.1 step 3 to ListActive; §8.1 Part B to
// GetState; the Symphony-Codex `linear_graphql` tool is unnecessary in
// gleaner because the orchestrator itself writes back via Comment.
type Tracker interface {
	// Kind returns "github" or "linear". Used for logging context, the
	// session_id prefix (SPEC §13.1), and code paths that need to know
	// which tracker is active (e.g. PR `Closes #N` only for github).
	Kind() string

	// EnforceAuth runs once at startup. GitHub: asserts the active gh
	// account matches Account. Linear: validates the API key. Returns
	// a clear actionable error if the operator must intervene.
	EnforceAuth(ctx context.Context) error

	// ListActive returns issues currently in any of the configured
	// active_states, filtered by adapter-specific require/block rules
	// (GitHub labels, Linear states). SPEC §8.1 step 3.
	//
	// Order: most-recently-updated first; the orchestrator re-sorts per
	// SPEC §8.1 step 4 (priority asc, created_at oldest, identifier lex).
	ListActive(ctx context.Context) ([]Issue, error)

	// GetState returns the tracker-native state for one issue. Used
	// during reconciliation to detect issues that went terminal mid-run.
	// SPEC §8.1 Part B.
	GetState(ctx context.Context, issueID string) (string, error)

	// Comment posts back to the issue (e.g. "PR opened: <url>"). Best
	// effort: errors are returned but callers may decide to log-and-continue.
	// For GitHub-tracker mode, Comment writes to the GitHub issue. For
	// Linear-tracker mode, Comment writes to the Linear issue (the
	// orchestrator's only write-back path when not on GitHub).
	Comment(ctx context.Context, issueID, body string) error

	// SetState moves the tracker-native state of `issueID` to `stateName`.
	// Best-effort: callers log errors but a failure must NEVER fail the
	// dispatch — the issue is still being worked, the board write-back is
	// purely cosmetic. Implementations cache state-name → state-id lookups
	// lazily on first call.
	//
	// When the tracker has no concept of state for this issue (e.g. GitHub
	// issue not on any Project v2 board), implementations return nil
	// silently after a debug log line. SPEC §7.1.
	SetState(ctx context.Context, issueID, stateName string) error
}

// IsTerminal reports whether `state` appears in the configured terminal
// list. Used by the orchestrator's reconciliation step (SPEC §8.1 Part B).
func IsTerminal(state string, terminalStates []string) bool {
	for _, ts := range terminalStates {
		if state == ts {
			return true
		}
	}
	return false
}

// IsActive reports whether `state` appears in the configured active list.
// SPEC §8.1 step 3 / step 5 (dispatch eligibility).
func IsActive(state string, activeStates []string) bool {
	for _, as := range activeStates {
		if state == as {
			return true
		}
	}
	return false
}
