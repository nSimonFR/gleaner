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

**v0.0.1** — quota snapshot only. The minimum thing that validates the
load-bearing design claim: both Claude Code and Codex CLI expose usable,
zero-token-cost utilization data without requiring you to invoke the CLI.

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

Roadmap toward a usable dispatcher: `drain` subcommand (v0.0.2), daemon mode
with NixOS module and shell hooks (v0.0.3), HTTP `/status` for a homepage
widget (v0.0.4), Codex executor profile (v0.0.5). See [IDEAS.md](./IDEAS.md)
for everything deliberately deferred past v0.0.5.

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

- `cmd/gleaner/` — CLI entrypoint and subcommands.
- `internal/adapter/quota.go` — `QuotaSource` interface; the minimal protocol
  every provider implements.
- `internal/adapter/claude_oauth/` — Claude adapter (hits
  `/api/oauth/usage`).
- `internal/adapter/codex_journal/` — Codex adapter (walks
  `~/.codex/sessions/`).
- `internal/plans/` — plan-specific constants and metadata.

## License

MIT.
