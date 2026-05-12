package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
)

// recordingTracker captures every SetState invocation for assertion.
type recordingTracker struct {
	fakeTracker
	calls   []recordedSetState
	failErr error
}

type recordedSetState struct {
	IssueID, StateName string
}

func (r *recordingTracker) SetState(ctx context.Context, issueID, stateName string) error {
	r.calls = append(r.calls, recordedSetState{issueID, stateName})
	return r.failErr
}

// TestSetStateBestEffort_HappyPath: a non-empty state name routes through
// the tracker exactly once.
func TestSetStateBestEffort_HappyPath(t *testing.T) {
	r := &recordingTracker{fakeTracker: fakeTracker{kind: "linear"}}
	SetStateBestEffort(context.Background(), r, tracker.Issue{ID: "X", Identifier: "NSI-18"}, "In Progress", "linear:X:1:abcd")
	if len(r.calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(r.calls))
	}
	if r.calls[0].IssueID != "X" || r.calls[0].StateName != "In Progress" {
		t.Errorf("call mismatch: %+v", r.calls[0])
	}
}

// TestSetStateBestEffort_EmptyStateName: empty name MUST skip the tracker
// (operator opted out of the transition via tracker.in_progress_state: "").
func TestSetStateBestEffort_EmptyStateName(t *testing.T) {
	r := &recordingTracker{fakeTracker: fakeTracker{kind: "linear"}}
	SetStateBestEffort(context.Background(), r, tracker.Issue{ID: "X"}, "", "sess")
	if len(r.calls) != 0 {
		t.Errorf("empty name should be silent no-op; got %d call(s)", len(r.calls))
	}
}

// TestSetStateBestEffort_NilTracker: defensive — nil tracker must not panic.
func TestSetStateBestEffort_NilTracker(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil tracker should not panic; got: %v", r)
		}
	}()
	SetStateBestEffort(context.Background(), nil, tracker.Issue{ID: "X"}, "In Progress", "sess")
}

// TestSetStateBestEffort_SwallowsErrors: a SetState failure must not
// propagate. The dispatch must continue.
func TestSetStateBestEffort_SwallowsErrors(t *testing.T) {
	r := &recordingTracker{
		fakeTracker: fakeTracker{kind: "linear"},
		failErr:     errors.New("boom"),
	}
	// Will panic if errors propagate.
	SetStateBestEffort(context.Background(), r, tracker.Issue{ID: "X", Identifier: "NSI-18"}, "In Progress", "sess")
	if len(r.calls) != 1 {
		t.Fatalf("calls = %d; want 1 (function should still attempt the call)", len(r.calls))
	}
}
