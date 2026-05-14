# Ideas — not on the v0.2.x path

Stuff considered, deliberately cut from v0.2.x. Gleaner is now a Linear
picker that hands off to Cyrus. Promote out of here only when (a) the
picker is running stably and (b) you can articulate which existing
failure mode the idea addresses.

## Additional pickers — "automatic review and test"

The current `tick` only picks new Todo tickets. The same picker shape
applies to two other Linear-visible states that Cyrus could be handed:

- **review picker** — issues in a `Needs Review` state (or tickets
  whose linked PR has `needs-review` and no human reviewer) get
  reassigned to Cyrus with a review-mode hint. Requires Cyrus to
  support a review session profile (currently it implements features).
- **test picker** — issues/PRs in a `Needs Test` state get reassigned
  to Cyrus to run the test suite, fix flakes, and update test
  coverage. Cheaper per-tick than feature work; could share quota
  budget with feature picks via a per-state weight.

Shape: each picker is a small variant of `internal/picker/picker.go`
with its own active-state filter and Cyrus dispatch mode. The quota
gate and sort logic stay identical. Open questions before promoting:

- How does Cyrus learn what *mode* to run in? Linear ticket label, a
  state name, or a dedicated comment template added at assignment
  time?
- Should review/test ticks compete with feature ticks for the same
  quota budget, or get a separate sub-cap (e.g. "review can run when
  feature is gated but long-window still has 30%")?
- PR-level pickers (vs issue-level) would require Cyrus to accept a
  PR id as the work item rather than a Linear issue id — open
  question whether Linear's Agent session protocol supports that.

## Quota optimization

- Warm-start Haiku ping at 22:55 to anchor the 5h window in night
  hours.
- Sub-bucket per-model routing: pick `complexity:trivial` issues only
  when Haiku window is fresh, hard issues only when Opus is fresh.
- Cold-path filter: skip issues whose hint-files were modified on
  `main` in the last 24h (cache discipline).

## Routing

- Multi-team backlog pooling: rank a unioned set of tickets across
  several `team_key`s with a single priority queue. Today gleaner only
  reads one team.
- `files_touched_hint:` directive parsing — when an issue body lists
  files, factor in whether those files are hot on the team's PR queue
  (avoid stomping on someone's in-flight branch).

## Observability

- Web UI / homepage widget showing recent picks, current quota, and
  reasons for skipped ticks.
- Weekly merge-rate alarm: if `merged_this_week` is 0 but utilization
  climbed, fire an alert — structural anti-goal protection.

## Storage

- History retention: gleaner is stateless. Linear keeps the assignment
  audit trail; nothing to drop.
