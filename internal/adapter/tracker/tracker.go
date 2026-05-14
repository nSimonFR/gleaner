// Package tracker abstracts the issue source so gleaner can read a Linear
// board and hand picked tickets off to Cyrus by reassigning them. Adapters
// live in sub-packages (linear/).
//
// Gleaner is a picker, not an orchestrator: it does not write state, post
// comments, or open PRs. Cyrus (the Linear coding-agent that listens for
// Linear Agent session events) owns lifecycle once a ticket is assigned.
package tracker

import (
	"context"
	"time"
)

// Issue is the gleaner-canonical issue shape. Adapters fill in what they
// support; missing fields are zero-valued.
type Issue struct {
	// ID is the tracker-native unique identifier (GraphQL node id). Stable
	// across renames. Used as the picker's deduplication key.
	ID string

	// Identifier is the human-readable label shown in logs (e.g. "NSI-42").
	Identifier string

	Title  string
	Body   string
	Labels []string

	// State is the tracker-native state name (Linear: "Todo" / "In Progress"
	// / "Done"). The picker only considers active states.
	State string

	// Priority follows Linear's 0=none, 1=urgent, 2=high, 3=medium, 4=low.
	//
	// CAUTION: 0 collides between "no priority set" and a real "no
	// priority" value. The picker sort treats 0 as LOWEST priority
	// (sorted last), matching Linear's own UI ordering.
	Priority int

	// AssigneeID is the tracker-native id of the current assignee, or "" if
	// unassigned. The picker only hands off unassigned tickets, or tickets
	// already assigned to the Cyrus user (idempotent re-pick).
	AssigneeID string

	URL string

	// BlockedBy is the set of tracker-native blocker issue IDs that are
	// still in a non-terminal state. The picker skips issues with any
	// outstanding blockers (SPEC §8.1 step 5).
	BlockedBy []string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Tracker is the issue-source contract. Implementations: linear.
type Tracker interface {
	// Kind returns "linear". Used for logging context.
	Kind() string

	// EnforceAuth runs once at startup. Validates credentials with a cheap
	// ping. Returns a clear, actionable error if the operator must
	// intervene.
	EnforceAuth(ctx context.Context) error

	// ListActive returns issues currently in any of the configured active
	// states, in tracker-default order. The picker re-sorts.
	ListActive(ctx context.Context) ([]Issue, error)

	// Assign sets the assignee on the given issue. For Linear, this fires
	// an "Agent session event" webhook when the new assignee is a Cyrus
	// agent user — that's the picker's handoff mechanism.
	Assign(ctx context.Context, issueID, userID string) error
}
