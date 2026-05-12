package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	codehost "github.com/nSimonFR/gleaner/internal/adapter/codehost/github"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/executor"
	"github.com/nSimonFR/gleaner/internal/predicate"
)

// Orchestrator implements the SPEC §8.1 tick loop on top of a State
// + a Tracker + a CodeHost + per-provider QuotaSources. It owns one
// goroutine per running Worker and a single tick goroutine. State
// access is serialized via the State's mutex; the orchestrator
// itself is stateless beyond the State pointer it holds.
type Orchestrator struct {
	Cfg          *config.Config
	Tracker      tracker.Tracker
	CodeHost     *codehost.Client
	QuotaSources map[string]adapter.QuotaSource // keyed by Provider() ("claude", "codex", …)
	CodehostRepos []string
	WorkTreeRoot string
	State        *State

	// HookFire is the legacy event hook ("pr_opened", "dispatch_failed", …).
	// The orchestrator's PROpener (Milestone E) will invoke it.
	HookFire func(event string, payload map[string]any)
	// PROpener is the function used to push the branch and create the PR
	// after a successful dispatch. Mirrors the inline pushBranch+CreatePR
	// in drain.go but injected so the orchestrator stays testable.
	PROpener PROpener

	wg sync.WaitGroup // tracks active worker goroutines
}

// PROpener is the post-dispatch hook the orchestrator calls when an
// executor.Run yields HasChanges and the profile says open_pr. It
// pushes the branch and creates the PR. Returning an error logs the
// failure but doesn't change the dispatch outcome (the worktree is
// preserved for human follow-up).
type PROpener func(ctx context.Context, iss tracker.Issue, prof *config.Profile, res *executor.Result) (string, error)

// Tick runs one iteration of the SPEC §8.1 poll loop:
//   1. Reconcile running (Milestone D will check terminal-state here).
//   2. Validate config (skipped — config is loaded once at startup).
//   3. Fetch candidate issues from the tracker.
//   4. Sort by priority asc, created_at oldest first, identifier lex.
//   5. Dispatch eligible while concurrency slots remain.
//
// Tick is safe to call concurrently with itself; the State mutex
// serializes claim transitions. In practice callers run a single Tick
// per ticker.C event.
func (o *Orchestrator) Tick(ctx context.Context, now time.Time) {
	// SPEC §8.1 step 1: reconcile running. Milestone D will add the
	// terminal-state cleanup; for now this is a no-op placeholder.
	_ = o.reconcileRunning(ctx)

	// SPEC §8.1 step 2: validate. Config is loaded once; skip per-tick
	// re-Validate to avoid filesystem hits on every tick.

	// Global predicate (tick-level, no quota).
	globalDecision := predicate.EvaluateGlobal(ctx, predicate.Inputs{
		Cfg:           o.Cfg,
		CodeHost:      o.CodeHost,
		CodehostRepos: o.CodehostRepos,
		Now:           now,
	})
	if !globalDecision.Allow {
		fmt.Printf("[%s] skip: %s\n", now.Format(time.RFC3339), globalDecision.Reason)
		return
	}
	isActiveHour := predicate.IsActiveHour(now, o.Cfg.Hours)

	// SPEC §8.1 step 3: fetch candidates.
	candidates, err := o.Tracker.ListActive(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list_active_failed: %v\n", err)
		return
	}

	// SPEC §8.1 step 4: sort.
	sortBySpec(candidates)

	// SPEC §8.1 step 5: dispatch eligible while slots remain.
	o.dispatchEligible(ctx, candidates, now, isActiveHour)
}

func (o *Orchestrator) reconcileRunning(ctx context.Context) error {
	// Placeholder for Milestone D's terminal-state reconciliation.
	// Today's Tick returns immediately; future work: fetch
	// Tracker.GetState for each running issue; if terminal, Cancel
	// the worker.
	return nil
}

func (o *Orchestrator) dispatchEligible(ctx context.Context, candidates []tracker.Issue, now time.Time, isActiveHour bool) {
	maxConc := o.Cfg.Agent.MaxConcurrentAgents
	if maxConc <= 0 {
		maxConc = 1
	}

	for i := range candidates {
		iss := candidates[i]

		// Global slot check.
		if o.State.RunningCount() >= maxConc {
			return
		}
		// In-flight check.
		if o.State.PhaseOf(iss.ID) != Unclaimed {
			// Released / Running / Claimed / RetryQueued: skip.
			if !o.State.IsRetryEligible(iss.ID, now) {
				continue
			}
			// Retry is now due — fall through and try to claim.
		}

		// SPEC §8.1 step 5: Todo issues with non-terminal blockers are
		// ineligible. Tracker.ListActive already populates BlockedBy
		// with the unresolved blocker IDs.
		if len(iss.BlockedBy) > 0 {
			fmt.Printf("skip-issue: %s reason=blocked_by=%v\n", iss.Identifier, iss.BlockedBy)
			continue
		}

		// Profile match.
		hasComplexity := false
		for _, l := range iss.Labels {
			if strings.HasPrefix(l, "complexity:") {
				hasComplexity = true
				break
			}
		}
		if !hasComplexity {
			continue
		}
		prof := o.Cfg.MatchProfile(iss.Labels)
		if prof == nil {
			continue
		}

		// Per-provider concurrency check.
		provider := providerFromPlan(prof.Plan)
		if cap, ok := o.Cfg.Concurrency.PerProvider[provider]; ok && cap > 0 {
			running := o.State.RunningByProvider()
			if running[prof.Plan] >= cap {
				continue
			}
		}

		// Per-provider quota predicate.
		if src, ok := o.QuotaSources[provider]; ok {
			d := predicate.EvaluateQuota(ctx, src, o.Cfg, isActiveHour)
			if !d.Allow {
				continue
			}
		}

		// Claim. If another tick already claimed this issue, skip.
		if !o.State.TryClaim(iss.ID) {
			continue
		}

		// Spawn worker.
		attempt := o.State.RetryAttemptCount(iss.ID) + 1
		o.launchWorker(ctx, iss, prof, attempt)
	}
}

func (o *Orchestrator) launchWorker(parentCtx context.Context, iss tracker.Issue, prof *config.Profile, attempt int) {
	wctx, cancel := context.WithCancel(parentCtx)
	session := NewSession(o.Tracker, iss, attempt)
	w := &Worker{
		Issue:     iss,
		Session:   session,
		Profile:   prof,
		StartedAt: time.Now(),
		LastEvent: time.Now(),
		Cancel:    cancel,
	}
	if !o.State.MarkRunning(iss.ID, w) {
		// Claim was lost between TryClaim and MarkRunning — bail.
		cancel()
		o.State.Release(iss.ID)
		return
	}

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer cancel()
		o.runWorker(wctx, w, attempt)
	}()
}

func (o *Orchestrator) runWorker(ctx context.Context, w *Worker, attempt int) {
	// Panic-safety: if anything panics inside the worker goroutine, the
	// State would otherwise show this issue stuck in Running forever
	// (no Elixir-style Process.monitor / :DOWN handler in Go). Drop the
	// claim + log so the next tick can re-dispatch. SPEC §7.1 doesn't
	// prescribe this, but it's the obvious analog to Elixir's supervisor
	// restart semantics.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "worker_panic: issue=%s session=%s recover=%v\n",
				w.Issue.Identifier, w.Session.ID, r)
			o.State.Release(w.Issue.ID)
		}
	}()

	taskID := fmt.Sprintf("%s:%s", o.Tracker.Kind(), w.Issue.Identifier)
	fmt.Printf("dispatch: %s session=%s attempt=%d profile=%s\n",
		w.Issue.Identifier, w.Session.ID, attempt, w.Profile.Name)

	res, runErr := executor.Run(ctx, w.Profile, &w.Issue, o.WorkTreeRoot, false, o.Cfg.Hooks)
	if runErr != nil {
		if errors.Is(runErr, executor.ErrBeforeRunDenied) {
			// Denial: not a failure. Release the issue back to Unclaimed
			// (operator's quota-gate doing its job).
			fmt.Printf("skip: before_run_denied issue=%s session=%s reason=%v\n",
				w.Issue.Identifier, w.Session.ID, runErr)
			o.State.Release(w.Issue.ID)
			return
		}
		// Real failure: schedule retry.
		next := Backoff(attempt, o.Cfg.Agent.MaxRetryBackoff)
		dueAt := time.Now().Add(next)
		fmt.Fprintf(os.Stderr, "dispatch_failed: issue=%s session=%s attempt=%d retry_in=%s err=%v\n",
			w.Issue.Identifier, w.Session.ID, attempt, next, runErr)
		o.State.FailAndQueueRetry(w.Issue.ID, attempt, dueAt, runErr.Error(), w.Issue)
		if o.HookFire != nil {
			o.HookFire("dispatch_failed", map[string]any{
				"reason":   runErr.Error(),
				"profile":  w.Profile.Name,
				"task_id":  taskID,
				"session":  w.Session.ID,
				"attempt":  attempt,
				"exitcode": res.ExitCode,
			})
		}
		return
	}

	fmt.Printf("dispatch_ok: issue=%s session=%s branch=%s changes=%v duration=%dms\n",
		w.Issue.Identifier, w.Session.ID, res.Branch, res.HasChanges, res.DurationMs)

	if w.Profile.OnSuccess != "open_pr" || !res.HasChanges {
		o.State.Release(w.Issue.ID)
		return
	}

	if o.PROpener == nil {
		fmt.Fprintf(os.Stderr, "pr_opener_nil: issue=%s — workspace preserved at %s\n",
			w.Issue.Identifier, res.WorkTree)
		o.State.Release(w.Issue.ID)
		return
	}
	url, err := o.PROpener(ctx, w.Issue, w.Profile, res)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr_open_failed: issue=%s session=%s err=%v\n",
			w.Issue.Identifier, w.Session.ID, err)
		o.State.Release(w.Issue.ID)
		return
	}
	fmt.Printf("pr_opened: %s session=%s\n", url, w.Session.ID)
	if o.HookFire != nil {
		o.HookFire("pr_opened", map[string]any{
			"pr":      map[string]any{"url": url, "branch": res.Branch},
			"profile": w.Profile.Name,
			"task_id": taskID,
			"session": w.Session.ID,
		})
	}

	// For non-github trackers, write the PR URL back via Tracker.Comment.
	if o.Tracker.Kind() != "github" {
		body := fmt.Sprintf("Gleaner opened PR via profile `%s`: %s", w.Profile.Name, url)
		if err := o.Tracker.Comment(ctx, w.Issue.ID, body); err != nil {
			fmt.Fprintf(os.Stderr, "tracker_comment_failed: %v\n", err)
		}
	}
	o.State.Release(w.Issue.ID)
}

// Run drives the tick loop until ctx is cancelled. SPEC §8.1: each tick
// fires polling.interval_ms apart (gleaner: Hours.Poll). On shutdown
// the orchestrator cancels every active worker and waits for them.
func (o *Orchestrator) Run(ctx context.Context) {
	poll := o.Cfg.Hours.Poll
	if poll == 0 {
		poll = 10 * time.Minute
	}
	fmt.Printf("orchestrator: starting; tracker=%s poll=%s max_concurrent=%d\n",
		o.Tracker.Kind(), poll, o.Cfg.Agent.MaxConcurrentAgents)

	tick := time.NewTicker(poll)
	defer tick.Stop()

	o.Tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			fmt.Println("orchestrator: shutting down — cancelling running workers")
			o.State.CancelAll()
			o.wg.Wait()
			return
		case t := <-tick.C:
			o.Tick(ctx, t)
		}
	}
}

// sortBySpec sorts in place per SPEC §8.1 step 4: priority ascending,
// created_at oldest first, identifier lexicographic. Linear's
// priority=0 means "no priority"; we treat that as LOWEST (sorted last,
// not first) — matches Symphony Elixir and avoids the trap that
// pre-Milestone-C plan reviewers flagged.
func sortBySpec(issues []tracker.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		ap, bp := normalizePriority(a.Priority), normalizePriority(b.Priority)
		if ap != bp {
			return ap < bp
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.Identifier < b.Identifier
	})
}

// normalizePriority maps Linear's 0=none into a sentinel that sorts
// last (largest int). Linear: 1=urgent (highest), 4=low (lowest).
// Without this, "no priority" issues would sort BEFORE urgent ones.
func normalizePriority(p int) int {
	if p == 0 {
		return 999
	}
	return p
}

func providerFromPlan(plan string) string {
	if i := strings.Index(plan, "/"); i > 0 {
		return plan[:i]
	}
	return plan
}
