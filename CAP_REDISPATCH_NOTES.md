# Cap-aware re-dispatch — investigation notes (TASK B, read-only)

Goal: when Cyrus is interrupted mid-issue by a Claude usage cap, gleaner
should re-dispatch that issue after the usage window resets. This doc
records the current picker/assign flow, how an interrupted issue is left
in Linear, and the candidate re-fire mechanisms. **No code was changed for
this part; re-dispatch is NOT implemented.**

---

## 1. Current picker / assign flow

One tick (`internal/picker/picker.go:59` `Tick`):

1. **Global gate** — kill switch + hours of day (`picker.go:65`,
   `internal/predicate/eval.go:48` `EvaluateGlobal`).
2. **List** — `in.Tracker.ListActive(ctx)` (`picker.go:72`). The Linear
   adapter queries `issues(filter:{team, state:{name in ActiveStates}})`
   with `ActiveStates` defaulting to `["Todo","In Progress"]`
   (`internal/adapter/tracker/linear/client.go:138` `ListActive`,
   states default at `client.go:42` `New`).
3. **Filter to candidates** (`picker.go:79`): skip if `len(BlockedBy)>0`;
   skip if `AssigneeID != "" && AssigneeID != cyrus` (human-owned).
   Kept: unassigned, or already assigned to the Cyrus user.
   `cyrus = in.Cfg.Tracker.CyrusUserID` (`picker.go:78`;
   config field `internal/config/config.go:27`).
4. **Sort** (`picker.go:97`): priority asc (0 treated as lowest), then
   `CreatedAt` oldest, then `Identifier` lexical. `top = candidates[0]`.
5. **Quota gate** (`picker.go:118`): for each `QuotaSource`,
   `EvaluateQuota` (`eval.go:73`) checks `Windows["short"]` vs
   `ShortWindow{Active,Idle}` and `Windows["long"]` vs
   `LongWindowCeiling`. First denial wins → tick stops.
6. **Handoff** (`picker.go:131`):
   - If `top.AssigneeID == cyrus` → **no-op** (`tick_already_assigned`,
     `picker.go:132`). This is the load-bearing line for re-dispatch:
     gleaner today will NOT re-touch an issue already assigned to Cyrus.
   - Else (DryRun aside) `in.Tracker.Assign(ctx, top.ID, cyrus)`
     (`picker.go:149`).

Assign mutation (`linear/client.go:209` `Assign`):

```graphql
mutation($id: String!, $assignee: String!) {
  issueUpdate(id: $id, input: { assigneeId: $assignee }) { success }
}
```

So the only handoff signal gleaner emits is a single
`issueUpdate{assigneeId: cyrus}`. There is no comment, no state change, no
agent-session mutation.

---

## 2. How an interrupted issue is left in Linear

What we know from the code/config:

- Cyrus is wired to the Linear **"Agent session events"** webhook only
  (`/home/nsimon/nic-os/rpi5/cyrus.nix:35`, and the header comment at
  `cyrus.nix:88-93`). It is a coding-agent that reacts to *agent session*
  events, not raw issue events.
- gleaner is a picker, not an orchestrator: it "does not write state, post
  comments, or open PRs. Cyrus owns lifecycle once a ticket is assigned"
  (`internal/adapter/tracker/tracker.go:5-7`).
- When Cyrus's underlying Claude run is interrupted by a usage cap, Cyrus
  itself never changes the assignee/delegate (gleaner set it; Cyrus only
  comments / moves state / opens PRs as its lifecycle dictates).

Therefore an interrupted issue is left:

- **Assignee/delegate**: still Cyrus. gleaner set it once and nothing
  clears it. (Caveat — see the delegate-vs-assignee finding in §3; the
  field gleaner *thinks* it set as `assignee` may surface as `delegate`.)
- **State**: whatever state the issue was in when the run died. If Cyrus
  had already moved it to "In Progress", it stays "In Progress"
  (still an active state → still returned by `ListActive`). If Cyrus
  never got far enough to move it, it stays "Todo".
- **Agent session**: the prior session exists but is no longer making
  progress. Linear marks session state automatically; it will not
  spontaneously restart when the cap clears.

Net effect on the next gleaner tick after the cap clears: the issue is
still active and still assigned to Cyrus, so it lands in `candidates`,
likely sorts back to `top`, and then hits the
`top.AssigneeID == cyrus` **no-op** branch (`picker.go:132`). **gleaner
does nothing — the issue is silently stranded.** This is the exact gap
cap-aware re-dispatch must close.

**OPEN QUESTION (needs a live check):** does
`issueUpdate{assigneeId: cyrus}` come back on a subsequent `ListActive`
as `assignee.id == cyrus` (which the filter/no-op logic assumes), or as a
`delegate` with `assignee == null`? See §3 — this materially changes both
the filter (step 3) and the no-op check (step 6).

---

## 3. Re-fire mechanism — options and evidence

### Evidence gathered

- Linear docs, Agents page (https://linear.app/developers/agents):
  - "Sessions are created automatically when an agent is mentioned or
    delegated an issue." Two triggers: **delegation (assignment)** and
    **@mention**.
  - Delegation "triggers a `created` AgentSessionEvent webhook containing
    an `agentSession` object with the relevant issue, comment, and
    context." — this is precisely the event Cyrus listens for.
  - **"Assigning an issue to your app now sets it as the `delegate`, not
    the `assignee`—so humans maintain ownership while agents act on their
    behalf."** The `app:assignable` scope lets the app "be assigned as a
    delegate on issues." (Cyrus's OAuth app requests
    `app:assignable` — `cyrus.nix:31`/manual-steps comment.)
- Linear docs, Agent Interaction page
  (https://linear.app/developers/agent-interaction):
  - `created` action: "A new Agent Session has been created (triggered by
    a user mention or delegation)."
  - `prompted` action: "A user sent a new message into an existing Agent
    Session" — body in `agentActivity.body`. **"An agent cannot generate a
    `prompt` type activity"** (prompts are user-generated).
  - Proactive creation without waiting for a human:
    **`agentSessionCreateOnIssue`** and **`agentSessionCreateOnComment`**
    mutations.
  - The docs do **not** state whether re-delegating an issue already
    delegated to the same agent re-fires `created` or is a no-op. This is
    an explicit documentation gap (confirmed on both pages).

### Options to re-trigger Cyrus on an interrupted issue

**Option A — re-issue `issueUpdate{assigneeId: cyrus}` as-is (cheapest).**
- Pro: zero new code; gleaner already does this mutation.
- Con: very likely a no-op when Cyrus is *already* the
  assignee/delegate. Linear's docs don't promise idempotent re-fire, and
  the general "set field to the value it already holds → no change event"
  pattern strongly suggests no new `created` webhook. **Not reliable.**

**Option B — unassign → reassign cycle.**
- `issueUpdate{assigneeId: null}` then `issueUpdate{assigneeId: cyrus}`.
- Pro: forces a genuine delegation transition, which is the documented
  `created` trigger; no new GraphQL surface beyond what `Assign` already
  uses.
- Con: two mutations + a brief window where the issue is unassigned (a
  concurrent human/tick could grab it). Also unclear whether the old
  agent session is reused or a second one is created.

**Option C — `agentSessionCreateOnIssue` mutation (most explicit).**
- Purpose-built to "proactively create a session" on an issue without a
  human mention/delegation.
- Pro: semantically exact — directly asks Linear to start a fresh agent
  session, which is what we want after a cap reset.
- Con: new mutation to add to the Linear adapter; need to confirm the
  exact input shape (issue id, and whether it targets a specific agent
  app/delegate) and required OAuth scope. Behavior when a prior session
  exists on the same issue is undocumented (second session vs. error).

**Option D — post a comment / `prompted` activity.**
- A user-authored comment that @mentions the agent, or a `prompt`
  activity into the existing session, produces a `prompted` event Cyrus
  handles by continuing the conversation.
- Pro: reuses the existing (interrupted) session, preserving context.
- Con: gleaner explicitly "does not post comments" (design boundary,
  `tracker.go:5-7`); `prompt` activities are documented as
  **user-generated only** — an agent/integration cannot emit them, and
  gleaner authenticating as a Linear *user* API key vs. the Cyrus *app*
  is a separate identity, so "who posts" needs care.

### Recommended minimal approach

**Primary: Option C (`agentSessionCreateOnIssue`)**, gated on the new
cap-aware signal from TASK A.

Rationale: it is the documented, purpose-built "start a session now"
primitive, so it does not depend on Linear's undocumented re-delegation
idempotency (Option A's fatal flaw) and avoids the unassigned-window race
of Option B. It also keeps gleaner within its "picker, not commenter"
boundary (no Option D comments).

Suggested flow (NOT implemented):
1. In the picker, when `top.AssigneeID == cyrus` (today's no-op at
   `picker.go:132`), consult `UsageSnapshot.ActiveLimit` (TASK A):
   if the issue was previously dispatched and the gating window has since
   reset (`ActiveLimit == nil` or `ActiveLimit.ResetsAt` is in the past,
   and `!Capped`), treat it as an interrupted issue eligible for
   re-dispatch instead of a plain no-op.
2. Call a new `tracker.Tracker.Restart(ctx, issueID)` that issues
   `agentSessionCreateOnIssue` for that issue.
3. Keep it idempotency-safe with a small marker so we don't re-create a
   session every tick within the same window (e.g. only restart once per
   reset boundary).

**Fallback if Option C proves unavailable/awkward in practice:**
Option B (unassign→reassign), accepting the brief unassigned window and
adding a guard so a concurrent tick can't double-handle.

---

## 4. Open questions requiring a live test or human decision

1. **assignee vs delegate (highest priority).** Confirm what
   `issueUpdate{assigneeId: cyrus}` actually produces on a Cyrus *app*
   user: does `ListActive` see `assignee.id == cyrus`, or `assignee ==
   null` + a populated `delegate`? If it's the latter, gleaner's filter
   (`picker.go:84`) and no-op check (`picker.go:132`) are already
   mismatched with reality and the Linear query needs a `delegate { id }`
   field. (The fact that handoff currently works suggests assigneeId
   still maps to the delegate for app users, but verify.)
2. **Re-delegation idempotency.** Does setting `assigneeId` to the
   already-current Cyrus value re-fire a `created` AgentSessionEvent? If
   yes, Option A becomes viable and is the truly-minimal fix. Needs one
   controlled live test against a throwaway issue.
3. **`agentSessionCreateOnIssue` contract.** Exact input fields, required
   OAuth scope, and behavior when a prior (interrupted) session already
   exists on the issue — new session, reused session, or error?
4. **"Interrupted" detection.** gleaner keeps no state between ticks
   (`picker.go:16`). Distinguishing "Cyrus is actively working" from
   "Cyrus's run died at the cap" cannot be done from assignee alone. We
   likely need either: a Linear agent-session state field surfaced into
   `tracker.Issue`, or a lightweight persisted marker of "last dispatched
   at / under which reset window". Human decision: how much state is
   gleaner allowed to keep (vs. its current stateless design)?
5. **Cap-reset boundary semantics.** TASK A surfaces
   `ActiveLimit.ResetsAt`. Confirm that "window reset" is the right
   re-dispatch trigger (vs. `!Capped` alone), and decide the once-per-
   window guard to avoid restart spam.
