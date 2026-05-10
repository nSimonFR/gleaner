# gleaner

A quota-aware coding-agent dispatcher. Drains a curated GitHub backlog into your
idle hours so subscription tokens don't go to waste at week reset — without
blocking your live work and without drowning you in PRs to review on Monday.

**Metric**: `merged_pr_equivalent_per_week`
**Anti-goal**: `maximize_tokens_burned`

> The name is the agricultural term for someone who picks up grain left in the
> field after harvest. Value already paid for, recovered before it's plowed
> under. See Millet's *The Gleaners* (1857) for the visual cue, or Agnès
> Varda's *Les Glaneurs et la Glaneuse* (2000) for the modern reading.

---

## Status

**v0.0.3** — all three early phases shipped in one autonomous build session.
A subagent reviewed every commit; the `fix:` commit on top of the v0.0.3
work resolves twelve issues it surfaced.

- `gleaner snapshot` — read both providers' quota at zero token cost.
- `gleaner drain --config <yaml>` — single-shot dispatch (predicate →
  one issue → one PR labeled `needs-review` with a metadata block).
- `gleaner serve --config <yaml>` — long-running daemon for non-systemd
  setups. The shipped NixOS module uses the simpler timer-fires-drain
  pattern instead; serve is for testing or other init systems.
- `gleaner bootstrap --config <yaml>` — idempotent label creation
  across your repos (afk-ready, complexity:*, needs-human, blocked,
  wip, afk, needs-review).
- NixOS module: `services.gleaner.{enable, user, configFile, ...}`.
  Run `inputs.gleaner.nixosModules.gleaner` in your service file's
  imports, then set the options as in the example below.

```
$ gleaner snapshot
claude (max_5x)   [source: /api/oauth/usage]:
  short   83.0%   resets in 0h 12m
  long    44.0%   resets in 14h 23m
  sub-buckets: omelette  0.0% / sonnet  0.0% /

codex (plus)   [source: rollout-2026-05-10T14-34-49-019e11e2.jsonl]:
  short    1.0%   resets in 4h 15m
  long     0.0%   resets in 6d 18h
```

Next: HTTP `/status` for a homepage widget (v0.0.4); a worked example of
the Codex executor profile (v0.0.5; the executor mechanism already
supports it via `run: ["codex", "exec", "{prompt}"]`). See
[IDEAS.md](./IDEAS.md) for everything deliberately deferred past v0.0.5.

## Quota sources

| Provider | Source                              | Cost              | Notes                                             |
|----------|-------------------------------------|-------------------|---------------------------------------------------|
| Claude   | `https://api.anthropic.com/api/oauth/usage` | 0 tokens   | Metadata endpoint — does not draw quota.           |
| Codex    | `~/.codex/sessions/**/*.jsonl`     | 0 tokens          | `event_msg.payload.type=="token_count"` events.    |

The CLI never invokes `claude` or `codex` as a subprocess to read usage —
both adapters work entirely from artifacts the CLIs already write.

## Build

```
nix build
./result/bin/gleaner snapshot
```

Or with a Go toolchain:

```
go build -o gleaner ./cmd/gleaner
./gleaner snapshot
```

## Layout

- `cmd/gleaner/` — CLI entrypoints (`snapshot`, `drain`, `serve`, `bootstrap`).
- `internal/adapter/quota.go` — `QuotaSource` interface; the minimal protocol
  every provider implements.
- `internal/adapter/claude_oauth/` — Claude adapter (hits
  `/api/oauth/usage`).
- `internal/adapter/codex_journal/` — Codex adapter (walks
  `~/.codex/sessions/`).
- `internal/adapter/github/` — `gh` shell wrapper with auth-switch
  enforcement.
- `internal/config/` — YAML loader with package defaults; the
  profile-stacking shape (`profiles: [{match, run, on_success?}, ...]`).
- `internal/predicate/` — guard evaluator (kill switch → hours → quota
  windows → inflight count → daily cap).
- `internal/executor/` — generic profile runner with template-variable
  substitution (`{prompt}`, `{worktree}`, `{repo}`, …) over `exec.Command`
  (no shell — safe from issue-title injection).
- `internal/hook/` — best-effort fork-exec of a user-supplied dispatch
  script with event JSON on stdin.
- `module.nix` — NixOS module shipping `gleaner.service` + `gleaner.timer`.

## Config example

```yaml
account: nSimonFR-ai
repos:   [nSimonFR-ai/nic-os, nSimonFR-ai/for-sure]

profiles:
  - { match: ["lang:python", "provider:codex"], run: ["codex", "exec", "{prompt}"] }
  - { match: "*",                                run: ["claude", "-p", "{prompt}"] }

hook: /etc/gleaner/hooks/dispatch.sh
```

Everything else (hours, polling cadence, label sets, guard thresholds,
safety caps, profile names, plan inference) has a sane package default.
Override any of them inline only when you need to deviate.

## License

MIT.
