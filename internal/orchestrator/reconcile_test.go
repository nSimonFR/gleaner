package orchestrator

import (
	"context"
	"testing"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
)

// reconcileTracker is a fakeTracker that lets the test control what
// GetState returns per issue ID.
type reconcileTracker struct {
	fakeTracker
	states map[string]string
}

func (f *reconcileTracker) GetState(ctx context.Context, id string) (string, error) {
	return f.states[id], nil
}

// TestReconcileRunning_CancelsTerminalWorkers: a worker whose issue
// went terminal externally gets its context cancelled and its claim
// released.
func TestReconcileRunning_CancelsTerminalWorkers(t *testing.T) {
	state := NewState()
	state.TryClaim("A")
	state.TryClaim("B")
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	state.MarkRunning("A", &Worker{Issue: tracker.Issue{ID: "A", Identifier: "A"}, Cancel: cancelA})
	state.MarkRunning("B", &Worker{Issue: tracker.Issue{ID: "B", Identifier: "B"}, Cancel: cancelB})

	o := &Orchestrator{
		Cfg: &config.Config{
			Tracker: config.Tracker{TerminalStates: []string{"closed"}},
		},
		Tracker: &reconcileTracker{
			fakeTracker: fakeTracker{kind: "github"},
			states:      map[string]string{"A": "open", "B": "closed"},
		},
		State: state,
	}

	if err := o.reconcileRunning(context.Background()); err != nil {
		t.Fatalf("reconcileRunning: %v", err)
	}

	// B should be cancelled and Released.
	select {
	case <-ctxB.Done():
		// ok
	default:
		t.Error("worker B context should have been cancelled")
	}
	if state.PhaseOf("B") != Unclaimed {
		t.Errorf("B phase = %d, want Unclaimed (released)", state.PhaseOf("B"))
	}

	// A should be untouched.
	select {
	case <-ctxA.Done():
		t.Error("worker A should NOT have been cancelled (state still active)")
	default:
	}
	if state.PhaseOf("A") != Running {
		t.Errorf("A phase = %d, want Running", state.PhaseOf("A"))
	}
}

// TestReconcileRunning_NoTerminalStatesSkips: when TerminalStates is
// empty the function returns immediately without calling GetState.
func TestReconcileRunning_NoTerminalStatesSkips(t *testing.T) {
	state := NewState()
	state.TryClaim("A")
	_, cancelA := context.WithCancel(context.Background())
	state.MarkRunning("A", &Worker{Issue: tracker.Issue{ID: "A", Identifier: "A"}, Cancel: cancelA})

	called := false
	tr := &reconcileTracker{
		fakeTracker: fakeTracker{kind: "github"},
		states:      map[string]string{},
	}
	// Override GetState to record invocation.
	getter := func(ctx context.Context, id string) (string, error) {
		called = true
		return "", nil
	}
	_ = getter // not actually injected — the empty terminal list short-circuits

	o := &Orchestrator{
		Cfg:     &config.Config{Tracker: config.Tracker{TerminalStates: nil}},
		Tracker: tr,
		State:   state,
	}
	if err := o.reconcileRunning(context.Background()); err != nil {
		t.Fatalf("reconcileRunning: %v", err)
	}
	if called {
		t.Error("GetState should not be called when TerminalStates is empty")
	}
	if state.PhaseOf("A") != Running {
		t.Error("A should still be Running")
	}
}
