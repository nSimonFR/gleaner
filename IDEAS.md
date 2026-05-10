# Ideas — not on the v0.0.x path

Stuff considered, deliberately cut from v0.0.x. Promote out of here only when
(a) v0.0.5 is running stably and (b) you can articulate which existing failure
mode the idea addresses.

## Executor profiles to ship as built-in defaults

Each is config-only — no Go code, just new entries in
`internal/executor/defaults.go`. Add when there's a real use case:

- `opencastle-implement`: `npx opencastle run implement.convoy.yml` —
  multi-agent dev/UI/DB/test specialists with quality gates
  (`monkilabs/opencastle`)
- `opencastle-review`: `npx opencastle run review.convoy.yml` — review-only
  pass on an existing PR
- `opencastle-cleanup`: same tool, cleanup convoy — for stale-code /
  dead-flag removal tasks
- `claude-review`: `claude -p "review this diff" --allowedTools "Read,Bash"`
  for cheap per-PR sanity checks
- `routines-fire`: POSTs to an Anthropic Routine's `/fire` endpoint as
  fallback when local 5h cap is hit but Routines daily cap isn't. Inverts
  trust model.

## Hook recipes for downstream consumers

These live in the consuming system, not in gleaner:

- Slack notifier hook script using existing webhook
- Discord notifier hook script
- Email digest hook (cron-summarizes the day's events from history)
- Auto-archive hook: when `quota_cap_hit` fires, run `gh issue comment` on
  every in-flight issue with "paused until reset"

## Auto-merge

- Reviewer heuristic (LOC delta + CI status + risk labels) → score
- Auto-merge gate at score ≥ 0.95, max 50-line diff, repo allowlist (NEVER
  the repo that hosts gleaner itself), never touches `**/*.nix` / `*.age` /
  `flake.nix`
- claude-reviewer Haiku scoring as richer alternative to heuristic — but
  spending tokens to decide whether to spend tokens is suspicious; needs
  justification

## Quota optimization

- Warm-start Haiku ping at 22:55 to anchor the 5h window in night hours
- Sub-bucket per-model routing: `complexity:trivial`→haiku, routine→sonnet,
  hard→opus, with per-bucket ceilings (80/95/99)
- Delta-safety abort: if one tool-call burns >15% of the short window, kill
  the convoy
- Cold-path filter: skip issues whose hint-files were modified on `main` in
  last 24h (cache discipline)

## Observability

- Web UI at `:8088/` (not just JSON endpoint)
- Weekly merge-rate alarm: if `merged_this_week` is 0 but utilization
  climbed, fire an alert — structural anti-goal protection
- Multi-day trend dashboard
- HTTP `/trigger` endpoint for manual dispatch from another host

## Routing

- Issue body parsing for `files_touched_hint:` directive
- Reviewer agent assignment instead of label
- Multi-repo backlog pooling with priority queue

## Storage

- History retention: nothing to drop while stateless (GitHub keeps it)
