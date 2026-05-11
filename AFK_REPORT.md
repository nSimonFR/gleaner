# Autonomous Build Session — Report

**Window**: ~3 hours of focused work overnight (kicked off after plan approval).
**Outcome**: v0.0.3 shipped, reviewed, reviewer-flagged bugs fixed, NixOS
build green. Stopped short of `nixos-rebuild switch` and `gh repo create`
since those are visible state changes you should authorize directly.

## What got built

### `/home/nsimon/gleaner/` — 5 commits on `main`

```
b051a06 docs: update README for v0.0.3 surface
86a5ab3 fix: reviewer-flagged bugs across v0.0.1-v0.0.3
5bd9b7a v0.0.3: serve daemon + NixOS module
887151c v0.0.2: drain subcommand with profile-stacking config + GH adapter
860c5d2 v0.0.1: snapshot CLI — read both providers' quota at zero token cost
```

Module layout:
- `cmd/gleaner/{main,snapshot,drain,serve,bootstrap}.go` — 5 subcommands
- `internal/adapter/{quota,claude_oauth,codex_journal,github}/` — adapters
- `internal/{config,predicate,executor,hook}/` — orchestration
- `flake.nix` + `module.nix` — Nix packaging + NixOS service
- `README.md` + `IDEAS.md` — docs

### nic-os worktree `feat/gleaner` — 3 commits ahead of `main`

```
31629a9 chore(flake): bump gleaner pin to README update
b9cf4a3 fix(flake): drop double-import + bump gleaner pin to reviewer-fixes
cfd4b46 feat: wire gleaner — quota-aware coding-agent dispatcher
```

Adds:
- `flake.nix` — `gleaner` input pointing at `path:/home/nsimon/gleaner`
  (flip to `github:nSimonFR-ai/gleaner` after you push)
- `rpi5/configuration.nix` — imports `./gleaner.nix`
- `rpi5/gleaner.nix` — wires `services.gleaner` for user `nsimon`,
  emits `/etc/gleaner/hooks/dispatch.sh` routing events to the existing
  `telegramNotify` helper from `monitoring.nix:6-14`
- `rpi5/gleaner.config.yaml` — 7-line profile-stacking config for your
  4 repos

## What passed verification

1. ✅ `gleaner snapshot` reports both providers, output renders
2. ✅ Claude % matches `/api/oauth/usage` exactly (that IS the source)
3. ✅ Codex % matches the latest `token_count` event exactly
4. ✅ Wall-clock ~260ms (HTTP-bound on Claude); CPU ~60ms
5. ✅ Zero subprocess execs of `claude` or `codex`
6. ✅ Predicate denies on `kill_switch present at /tmp/...`
7. ✅ Predicate denies on `outside_drain_hours`
8. ✅ Predicate denies on `short_window_ceiling_hit (claude short=90% > 75%)`
9. ✅ Predicate green path reaches inflight check and exits with `predicate: ok`
10. ✅ `gh` auth-switch enforcement rejects wrong active account
11. ✅ `nix build` of the gleaner flake succeeds
12. ✅ `sudo nixos-rebuild build --flake .#rpi5` succeeds, builds
    `gleaner.service`, `gleaner.timer`, `gleaner-hook-dispatch`,
    `gleaner-telegram-notify`, `gleaner-0.0.1` derivations

## What I did NOT do (deliberately)

These are visible/destructive actions that need your eyes:

- **`nixos-rebuild switch`** — would start the gleaner timer on rpi5.
  Build artifact is in `/nix/store/l89mdpa7anxlj4f73d8f8fjk0a104j9n-gleaner-0.0.1`
  ready to activate.
- **`gh repo create nSimonFR-ai/gleaner`** — would publish the gleaner
  Go code. Repo lives only at `/home/nsimon/gleaner` so far.
- **`git push`** — both for the gleaner repo (no remote) and for the
  nic-os `feat/gleaner` branch (no PR opened).
- **`gleaner bootstrap`** — would create 9 labels on each of your 4
  repos. State change visible on GitHub.
- **First real drain dispatch** — needs `afk-ready` + `complexity:*`
  labels and at least one labeled issue. Wouldn't have fired anyway
  during your AFK window because no issues are labeled.

## What the subagent reviewer found and what I fixed

Twelve issues across the v0.0.1-v0.0.3 surface. All fixed in commit
`86a5ab3`:

- **Worktree retry collision**: appended unix-second suffix to branch
  + worktree paths, so a second dispatch against the same issue won't
  fail on existing-path errors.
- **Stale base**: `git fetch origin` runs before `git worktree add`.
- **Default branch detection**: `git symbolic-ref refs/remotes/origin/HEAD`
  picks the right base (main/master/develop), not hardcoded.
- **`EnforceAuth` fragility**: switched from substring-matching
  `gh auth status` prose to `gh api user --jq .login` (stable JSON).
- **`PROMPT` env clobber**: child processes get `GLEANER_PROMPT`, not
  bare `PROMPT`.
- **Predicate dead branch**: hours-of-day logic now has explicit
  "neither drain nor active → deny" instead of an empty-`if` comment.
- **`parseHHMM` rejecting `9:00`**: now accepts H:MM as well as HH:MM.
- **Codex timestamp parse fallback**: malformed events now skip,
  rather than fall back to `time.Now()` (which made bad data win the
  "most recent" race).
- **`bootstrap --repo` flag broken**: added `--account` flag so the
  subcommand works without `--config`.
- **`serve` swallowed errors**: dispatch errors are now logged and the
  loop continues (was silently discarded).
- **Untracked files missed**: `git status --porcelain` (not
  `git diff --quiet`) detects new files.
- **Module `user` default was wrong**: required, no default. Gleaner
  is always run as a real human user (needs to read their journals).

I left the `MergedThisWeek` adapter method and `AbortIfStep` guard
config field annotated as "reserved for v0.0.4" rather than deleting —
they're the API surfaces the next phase wires.

## Known caveats / things to watch on resume

1. **Codex 5h shows "(reset due)"** on the snapshot output. This is
   correct behavior — your last codex token_count event was on May 10
   at 12:34 UTC, and that 5h window has long since reset. As soon as
   you use codex once, the snapshot will show a fresh window.
2. **Claude 5h spiked to 90%** during this autonomous session (subagent
   work). It reset around 00:00. Future autonomous runs of this length
   will hit the cap if you don't pre-anchor the window — that's the
   warm-start ping idea sitting in IDEAS.md.
3. **`path:/home/nsimon/gleaner` flake input** is fine for local
   development. Switch to `github:nSimonFR-ai/gleaner` after you push.
   The `flake.lock` already references the path; bumping the URL
   triggers one re-lock.
4. **No tests yet**. Acceptance was end-to-end (build runs, predicate
   evaluates correctly on real inputs). Worth adding unit tests in
   v0.1, but not v0.0.x.

## Suggested order for resume

1. Skim `git diff main..HEAD` on `feat/gleaner`. Read `rpi5/gleaner.nix`.
2. `cd /home/nsimon/gleaner && git log --stat` to see what's in each commit.
3. Optional: `sudo nixos-rebuild switch --flake .#rpi5` to activate the
   service. With no labels on any issues, every 10-min tick will skip
   with `reason="no_eligible_issues"` — zero side effects.
4. Create the GitHub repo and push:
   ```
   cd /home/nsimon/gleaner
   gh auth switch -u nSimonFR-ai
   gh repo create nSimonFR-ai/gleaner --public --source . --remote origin
   git push -u origin main
   ```
5. Update the nic-os flake to point at the public repo:
   ```nix
   gleaner.url = "github:nSimonFR-ai/gleaner";
   ```
   Then `sudo nix flake lock --update-input gleaner` + commit.
6. When ready for first real drain: `gleaner bootstrap --config rpi5/gleaner.config.yaml`,
   label one test issue with `afk-ready` + `complexity:routine`, wait
   for the next tick or `sudo systemctl start gleaner.service`.

## Second review pass (after first AFK_REPORT was written)

A second subagent reviewer caught four issues the first pass missed.
All fixed in commit `671a261`:

1. **PR base hardcoded to `"main"`** at `drain.go:121` — the first
   fix only updated `setupWorkTree`, leaving `gh pr create --base main`
   broken for repos using `master`/`develop`. Now stamps
   `git config --local gleaner.base <default-branch>` in the worktree
   and reads it back at PR time.
2. **`pickIssue` ignored complexity:* gating** — the plan v0.0.2 §3
   says "missing complexity:* → skip, log, don't default", but with a
   `match: "*"` profile in the config every `afk-ready` issue got
   dispatched to the default. Now filters explicitly and logs
   `skip-issue: ... reason=missing_complexity_label`.
3. **`mergeOverlay` zero-value collapse** — `inflight_prs: 0` was
   being silently ignored because the merger used `!= 0` tests.
   Rewrote as pointer-bearing `configOverlay` + `applyTo`; verified
   that `inflight_prs: 0` now denies with
   `skip: inflight_cap_hit (0/0 open PRs)`.
4. **`gleaner.config.yaml` repos all 404** — used `nSimonFR-ai/<repo>`
   (bot account) but the repos live under `nSimonFR/` (personal).
   Without this fix, every timer tick would log
   `pick: gh issue list: GraphQL: Could not resolve to a Repository`
   forever, no dispatch ever. Fixed; `pi-mobile` also dropped from
   the list because `$HOME/pi-mobile` isn't cloned (gleaner's worktree
   creator needs the local clone).

Re-verified after the fixes:
- `nix build` of gleaner: green
- `sudo nixos-rebuild build --flake .#rpi5`: green
- `gleaner snapshot` still works
- `inflight_prs: 0` override actually denies

The reviewer also explicitly noted that *my first AFK_REPORT's claim
"Claude % matches /api/oauth/usage exactly" was tautological* — the
adapter just relays the endpoint, so it can't disagree by construction.
Honest read: claim #2 in the verification table is a no-op assertion,
not a verification. I should have written "Claude % is what
/api/oauth/usage returns; we don't compute it locally so divergence is
impossible".

## Where to look if something looks wrong

- The plan file: `/home/nsimon/.claude/plans/rustling-chasing-engelbart.md`
- This conversation's session log: `~/.claude/projects/-home-nsimon-nic-os--claude-worktrees-bridge-cse-01NLEFhNYDxcyxBzhJz5JdxN/`
- The reviewers' actual critiques: in this conversation. The first
  reviewer's findings preceded commit `86a5ab3`; the second
  reviewer's findings preceded `671a261`. Search for "**Top 3 risks**"
  to find them.
