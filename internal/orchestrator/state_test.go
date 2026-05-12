package orchestrator

import (
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
)

func dummyIssue(id string) tracker.Issue {
	return tracker.Issue{ID: id, Identifier: id}
}

func dummyWorker(id string, plan string) *Worker {
	return &Worker{
		Issue:   dummyIssue(id),
		Profile: &config.Profile{Name: "p", Plan: plan},
	}
}

// TestPhaseTransitions walks an issue through the full state machine
// per SPEC §7.1: Unclaimed → Claimed → Running → (Released).
func TestPhaseTransitions(t *testing.T) {
	s := NewState()

	if got := s.PhaseOf("X"); got != Unclaimed {
		t.Fatalf("initial: got %d, want Unclaimed", got)
	}

	if !s.TryClaim("X") {
		t.Fatal("TryClaim should succeed on a fresh issue")
	}
	if got := s.PhaseOf("X"); got != Claimed {
		t.Errorf("after TryClaim: got %d, want Claimed", got)
	}

	// Second TryClaim must fail.
	if s.TryClaim("X") {
		t.Error("TryClaim should refuse already-claimed issue")
	}

	w := dummyWorker("X", "claude/team")
	if !s.MarkRunning("X", w) {
		t.Error("MarkRunning should succeed when claim exists")
	}
	if got := s.PhaseOf("X"); got != Running {
		t.Errorf("after MarkRunning: got %d, want Running", got)
	}

	s.Release("X")
	if got := s.PhaseOf("X"); got != Unclaimed {
		t.Errorf("after Release: got %d, want Unclaimed (Released = absent)", got)
	}
}

// TestMarkRunningWithoutClaimRefused: state machine refuses a transition
// to Running without an existing claim — guards against goroutines that
// skip TryClaim.
func TestMarkRunningWithoutClaimRefused(t *testing.T) {
	s := NewState()
	w := dummyWorker("X", "claude/team")
	if s.MarkRunning("X", w) {
		t.Error("MarkRunning must require a prior Claim")
	}
}

// TestFailAndQueueRetry: failed dispatch records a RetryAttempt with
// DueAt; the issue moves Running → RetryQueued. IsRetryEligible reflects
// the schedule.
func TestFailAndQueueRetry(t *testing.T) {
	s := NewState()
	s.TryClaim("X")
	s.MarkRunning("X", dummyWorker("X", "claude/team"))

	dueAt := time.Now().Add(10 * time.Second)
	s.FailAndQueueRetry("X", 1, dueAt, "boom", dummyIssue("X"))

	if got := s.PhaseOf("X"); got != RetryQueued {
		t.Errorf("phase = %d, want RetryQueued", got)
	}
	if s.RetryAttemptCount("X") != 1 {
		t.Errorf("attempt count = %d, want 1", s.RetryAttemptCount("X"))
	}
	if s.IsRetryEligible("X", time.Now()) {
		t.Error("not yet due — should NOT be eligible")
	}
	if !s.IsRetryEligible("X", dueAt.Add(time.Second)) {
		t.Error("past due — should be eligible")
	}
	// Issues with no retry record are always eligible.
	if !s.IsRetryEligible("Y", time.Now()) {
		t.Error("never-tried issue should be eligible")
	}
}

// TestRunningCount + RunningByProvider underpin the orchestrator's
// concurrency caps.
func TestRunningCounts(t *testing.T) {
	s := NewState()
	for _, id := range []string{"A", "B", "C"} {
		s.TryClaim(id)
		plan := "claude/team"
		if id == "C" {
			plan = "codex/plus"
		}
		s.MarkRunning(id, dummyWorker(id, plan))
	}
	if got := s.RunningCount(); got != 3 {
		t.Errorf("RunningCount = %d, want 3", got)
	}
	byPlan := s.RunningByProvider()
	if byPlan["claude/team"] != 2 {
		t.Errorf("claude/team count = %d, want 2", byPlan["claude/team"])
	}
	if byPlan["codex/plus"] != 1 {
		t.Errorf("codex/plus count = %d, want 1", byPlan["codex/plus"])
	}
}

// TestClaimRunRetry sequence preserves the contract that MarkRunning
// clears the retry record (so a retried issue, once running again,
// has no stale retry entry).
func TestRetryClearedOnMarkRunning(t *testing.T) {
	s := NewState()
	s.TryClaim("X")
	s.MarkRunning("X", dummyWorker("X", "claude/team"))
	s.FailAndQueueRetry("X", 1, time.Now().Add(time.Second), "err", dummyIssue("X"))
	if s.PhaseOf("X") != RetryQueued {
		t.Fatal("setup")
	}
	// Simulate retry due → re-claim → run.
	if !s.TryClaim("X") {
		t.Fatal("re-claim should succeed once retry is due")
	}
	s.MarkRunning("X", dummyWorker("X", "claude/team"))
	if s.PhaseOf("X") != Running {
		t.Errorf("phase = %d, want Running", s.PhaseOf("X"))
	}
	if s.RetryAttemptCount("X") != 0 {
		t.Errorf("retry record should be cleared on MarkRunning; got attempt %d", s.RetryAttemptCount("X"))
	}
}

// TestSnapshots return defensive copies, not references to State maps.
func TestSnapshots(t *testing.T) {
	s := NewState()
	s.TryClaim("X")
	s.MarkRunning("X", dummyWorker("X", "claude/team"))
	s.FailAndQueueRetry("Y", 1, time.Now(), "boom", dummyIssue("Y"))

	running := s.SnapshotRunning()
	if len(running) != 1 || running[0].Issue.ID != "X" {
		t.Errorf("SnapshotRunning = %+v", running)
	}
	retries := s.SnapshotRetries()
	if len(retries) != 1 || retries[0].Issue.ID != "Y" {
		t.Errorf("SnapshotRetries = %+v", retries)
	}
}
