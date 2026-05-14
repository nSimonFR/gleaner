// Package picker selects the next Linear ticket to hand off to Cyrus.
//
// One tick:
//  1. Run the global predicate (kill switch + hours). If denied → stop.
//  2. ListActive from the tracker.
//  3. Filter to candidates Cyrus can own: unassigned, or already
//     assigned to the Cyrus user (idempotent), and no outstanding
//     blockers.
//  4. Sort: priority asc (treating 0 as lowest), then createdAt oldest,
//     then identifier lex.
//  5. Run the quota predicate. If denied → stop.
//  6. If top candidate is already assigned to Cyrus → no-op (Cyrus has
//     it). Otherwise Assign to Cyrus, which fires Linear's Agent session
//     event webhook.
//
// No state is kept between ticks. The systemd timer is the only driver.
package picker

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/logging"
	"github.com/nSimonFR/gleaner/internal/predicate"
)

// Inputs bundles dependencies for one tick.
type Inputs struct {
	Cfg          *config.Config
	Tracker      tracker.Tracker
	QuotaSources []adapter.QuotaSource
	Now          time.Time
	DryRun       bool // if true, log the pick but skip Assign
}

// Outcome reports what a tick did.
type Outcome struct {
	// Picked is the issue handed off to Cyrus this tick. Nil when the
	// tick was a no-op (no candidates, gate denied, top candidate
	// already owned by Cyrus, etc.).
	Picked *tracker.Issue

	// Skipped is the predicate denial reason when nothing was picked
	// because of a gate (kill/hours/quota). Empty when the tick was
	// allowed but had no candidates.
	Skipped string

	// AlreadyAssigned is true when the top candidate was already
	// assigned to the Cyrus user — handoff was unnecessary.
	AlreadyAssigned bool
}

// Tick runs one picker pass.
func Tick(ctx context.Context, in Inputs) (Outcome, error) {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	// 1. Global gate.
	gd := predicate.EvaluateGlobal(ctx, predicate.Inputs{Cfg: in.Cfg, Now: in.Now})
	if !gd.Allow {
		logging.Log("tick_skipped", logging.F("reason", gd.Reason))
		return Outcome{Skipped: gd.Reason}, nil
	}

	// 2. ListActive.
	issues, err := in.Tracker.ListActive(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("list active: %w", err)
	}

	// 3. Filter.
	cyrus := in.Cfg.Tracker.CyrusUserID
	candidates := make([]tracker.Issue, 0, len(issues))
	for _, iss := range issues {
		if len(iss.BlockedBy) > 0 {
			continue
		}
		if iss.AssigneeID != "" && iss.AssigneeID != cyrus {
			// Owned by a human — leave it alone.
			continue
		}
		candidates = append(candidates, iss)
	}

	if len(candidates) == 0 {
		logging.Log("tick_no_candidates", logging.F("total_active", len(issues)))
		return Outcome{}, nil
	}

	// 4. Sort.
	sort.SliceStable(candidates, func(i, j int) bool {
		// Linear priority: 0=none, 1=urgent..4=low. Treat 0 as lowest.
		pi, pj := candidates[i].Priority, candidates[j].Priority
		if pi == 0 {
			pi = 999
		}
		if pj == 0 {
			pj = 999
		}
		if pi != pj {
			return pi < pj
		}
		if !candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].Identifier < candidates[j].Identifier
	})
	top := &candidates[0]

	// 5. Quota gate. Runs every source; first denial wins. We still log
	// the chosen candidate so operators see what would have been picked.
	isActive := predicate.IsActiveHour(in.Now, in.Cfg.Hours)
	for _, src := range in.QuotaSources {
		qd := predicate.EvaluateQuota(ctx, src, in.Cfg, isActive)
		if !qd.Allow {
			logging.Log("tick_quota_blocked",
				logging.F("issue_id", top.ID),
				logging.F("issue_identifier", top.Identifier),
				logging.F("reason", qd.Reason),
			)
			return Outcome{Skipped: qd.Reason}, nil
		}
	}

	// 6. Handoff.
	if top.AssigneeID == cyrus {
		logging.Log("tick_already_assigned",
			logging.F("issue_id", top.ID),
			logging.F("issue_identifier", top.Identifier),
		)
		return Outcome{Picked: top, AlreadyAssigned: true}, nil
	}

	if in.DryRun {
		logging.Log("tick_picked_dry_run",
			logging.F("issue_id", top.ID),
			logging.F("issue_identifier", top.Identifier),
			logging.F("priority", top.Priority),
		)
		return Outcome{Picked: top}, nil
	}

	if err := in.Tracker.Assign(ctx, top.ID, cyrus); err != nil {
		return Outcome{}, fmt.Errorf("assign %s to cyrus: %w", top.Identifier, err)
	}
	logging.Log("tick_picked",
		logging.F("issue_id", top.ID),
		logging.F("issue_identifier", top.Identifier),
		logging.F("priority", top.Priority),
		logging.F("assigned_to", cyrus),
	)
	return Outcome{Picked: top}, nil
}
