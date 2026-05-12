// Package orchestrator implements the Symphony-equivalent dispatch
// loop for gleaner — state machine (SPEC §7.1), session IDs
// (§13.1), retry/backoff (§8.4), and tick reconciliation (§8.1).
//
// State lives in-memory per SPEC §14.3 ("In-memory scheduler state…
// retry timers, running sessions, and live worker state do NOT
// survive process restart"). On startup, the orchestrator queries
// the tracker for terminal issues, removes their workspaces, then
// fresh-polls to re-dispatch active work.
//
// Per the v0.1 plan, gleaner is coding-agent-agnostic: a Worker
// runs an argv (`profile.Run`), and "done" means exit 0. There is
// no Codex `app-server` protocol, no thread_id/turn_id concepts —
// the Session.ID is gleaner-defined.
package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
)

// Phase mirrors Symphony SPEC §7.1 issue orchestration states.
type Phase int

const (
	// Unclaimed: issue is eligible but no worker exists. Default state
	// for any issue returned from Tracker.ListActive that isn't in
	// state.Running / state.Claimed / state.Retries.
	Unclaimed Phase = iota

	// Claimed: reserved between the moment dispatch decides to handle
	// this issue and the moment a goroutine takes over. Prevents the
	// same tick from double-dispatching the same issue.
	Claimed

	// Running: a worker goroutine exists in state.Running.
	Running

	// RetryQueued: previous attempt failed; an entry exists in
	// state.Retries with DueAt in the future. The dispatcher skips
	// issues in this phase until DueAt is reached.
	RetryQueued

	// Released: terminal phase. The orchestrator has dropped the
	// claim because the issue went terminal in the tracker, the run
	// completed, or retries were exhausted. Released issues are not
	// stored — they leave state.Running / state.Claimed / state.Retries.
	Released
)

// Session is the agent-agnostic equivalent of Symphony's Codex
// `thread_id`+`turn_id`. ID has shape
// `<tracker_kind>:<issue.ID>:<attempt>:<random4>` — see sessions.go.
type Session struct {
	ID        string
	StartedAt time.Time
}

// Worker tracks one in-flight dispatch.
type Worker struct {
	Issue       tracker.Issue
	Session     Session
	Profile     *config.Profile
	StartedAt   time.Time
	LastEvent   time.Time
	LastMessage string
	// Workspace is the worktree path; populated by the
	// OnWorkspaceReady executor callback as soon as `git worktree add`
	// succeeds, so the HTTP API can surface it while the worker is
	// still running.
	Workspace string
	// Cancel terminates the worker mid-flight (Milestone D's
	// reconciliation: kill workers whose issue went terminal).
	Cancel context.CancelFunc
}

// RetryAttempt represents an issue queued for retry after a failed
// dispatch. SPEC §8.4 backoff schedule.
type RetryAttempt struct {
	Issue     tracker.Issue
	Attempt   int       // 1-based; first retry is attempt=1
	DueAt     time.Time // wall clock when this becomes eligible again
	LastError string
}

// State is the single-process orchestrator memory. All transitions
// go through methods that take the mutex.
type State struct {
	mu      sync.Mutex
	running map[string]*Worker       // keyed by tracker.Issue.ID
	claimed map[string]struct{}      // keyed by tracker.Issue.ID
	retries map[string]*RetryAttempt // keyed by tracker.Issue.ID
}

// NewState returns an empty State.
func NewState() *State {
	return &State{
		running: make(map[string]*Worker),
		claimed: make(map[string]struct{}),
		retries: make(map[string]*RetryAttempt),
	}
}

// PhaseOf returns the current phase of an issue. SPEC §7.1.
// Unclaimed when no record exists; Released is implicit (issue
// returned to the tracker pool).
func (s *State) PhaseOf(issueID string) Phase {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[issueID]; ok {
		return Running
	}
	if _, ok := s.claimed[issueID]; ok {
		return Claimed
	}
	if _, ok := s.retries[issueID]; ok {
		return RetryQueued
	}
	return Unclaimed
}

// TryClaim reserves an issue for dispatch. Returns false if another
// claim/run exists. The caller must Release on failure to launch.
func (s *State) TryClaim(issueID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[issueID]; ok {
		return false
	}
	if _, ok := s.claimed[issueID]; ok {
		return false
	}
	s.claimed[issueID] = struct{}{}
	return true
}

// MarkRunning transitions Claimed → Running by storing the Worker.
// Caller has just spawned the goroutine and wants the State to see
// the live record. If no Claim exists, MarkRunning returns false
// (the State machine refuses Running-without-Claim).
func (s *State) MarkRunning(issueID string, w *Worker) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.claimed[issueID]; !ok {
		return false
	}
	delete(s.claimed, issueID)
	// Clear any retry record now that we're actually running.
	delete(s.retries, issueID)
	s.running[issueID] = w
	return true
}

// FailAndQueueRetry records a failed attempt and schedules a retry
// at the given DueAt. The Worker is removed from Running. SPEC §8.4.
func (s *State) FailAndQueueRetry(issueID string, attempt int, dueAt time.Time, lastErr string, iss tracker.Issue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, issueID)
	delete(s.claimed, issueID)
	s.retries[issueID] = &RetryAttempt{
		Issue:     iss,
		Attempt:   attempt,
		DueAt:     dueAt,
		LastError: lastErr,
	}
}

// Release moves the issue to the implicit Released phase by dropping
// any claim / running / retry entry. SPEC §7.1: "claim removed
// (terminal/inactive/missing/retry done)".
func (s *State) Release(issueID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, issueID)
	delete(s.claimed, issueID)
	delete(s.retries, issueID)
}

// CancelAll terminates every running worker. Used on shutdown.
func (s *State) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.running {
		if w.Cancel != nil {
			w.Cancel()
		}
	}
}

// SetWorkspace records the worktree path for a running worker. Called
// by the executor's OnWorkspaceReady callback so the HTTP API can
// surface workspace.path while the worker is still running.
func (s *State) SetWorkspace(issueID, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.running[issueID]; ok {
		w.Workspace = path
	}
}

// RunningCount returns the count of in-flight workers — used for
// concurrency caps (agent.max_concurrent_agents).
func (s *State) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// RunningByProvider counts workers grouped by Profile.Plan
// (e.g. "claude/team": 2). Used for the per-provider concurrency
// sub-cap (concurrency.per_provider).
func (s *State) RunningByProvider() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.running))
	for _, w := range s.running {
		if w.Profile != nil {
			out[w.Profile.Plan]++
		}
	}
	return out
}

// SnapshotRunning returns a copy of running workers (for the
// HTTP /api/v1/state endpoint in Milestone E).
func (s *State) SnapshotRunning() []*Worker {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Worker, 0, len(s.running))
	for _, w := range s.running {
		out = append(out, w)
	}
	return out
}

// SnapshotRetries returns a copy of pending retries.
func (s *State) SnapshotRetries() []*RetryAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*RetryAttempt, 0, len(s.retries))
	for _, r := range s.retries {
		out = append(out, r)
	}
	return out
}

// IsRetryEligible reports whether `now` is past the retry DueAt
// for the issue. Issues with no retry record are eligible (i.e. they
// were never in retry). SPEC §8.4.
func (s *State) IsRetryEligible(issueID string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.retries[issueID]
	if !ok {
		return true
	}
	return !now.Before(r.DueAt)
}

// RetryAttemptCount returns the number of prior attempts for an
// issue — used by Backoff to compute the next delay.
func (s *State) RetryAttemptCount(issueID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.retries[issueID]
	if !ok {
		return 0
	}
	return r.Attempt
}
