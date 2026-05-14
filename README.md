# gleaner

A quota-aware Linear ticket picker. Reads the board, ranks by priority,
checks Claude/Codex quota windows, and hands the top candidate off to
[Cyrus](https://github.com/cyrusagents/cyrus) by reassigning the ticket —
which fires Cyrus's Linear Agent session webhook. Cyrus does the coding.

**Metric**: `merged_pr_equivalent_per_week`
**Anti-goal**: `maximize_tokens_burned`

> The name is the agricultural term for someone who picks up grain left in
> the field after harvest. Value already paid for, recovered before it's
> plowed under. See Millet's *The Gleaners* (1857) for the visual cue, or
> Agnès Varda's *Les Glaneurs et la Glaneuse* (2000) for the modern
> reading.

---

## AI Installation Prompt

> Install `gleaner` on a Linux or macOS host. **Done when** `gleaner
> snapshot` prints a non-empty utilization line for `claude` or `codex`,
> and `gleaner tick --config gleaner.config.yaml --dry-run` against a
> live Linear team prints the issue identifier it would have assigned.
>
> 1. Clone: `git clone https://github.com/nSimonFR/gleaner && cd gleaner`
> 2. Read first: `README.md`, `go.mod`, `flake.nix`, `examples/` if
>    present. Toolchain is Go ≥ 1.25.
> 3. Build (try in this order, stop at the first that works):
>    - `nix build && cp result/bin/gleaner ~/.local/bin/`
>    - `go build -o ~/.local/bin/gleaner ./cmd/gleaner`
>    Verify: `gleaner --help` lists `snapshot` and `tick`.
> 4. Prerequisites for `snapshot` to return real data:
>    - Claude: `~/.claude/.credentials.json` with a fresh OAuth token
>      (run `claude /login` once if missing).
>    - Codex: at least one session journal at
>      `~/.codex/sessions/**/*.jsonl` (run `codex` once if missing).
> 5. Write `gleaner.config.yaml` (see **Config** below — three required
>    fields). The Cyrus user id is the Linear `userId` of your Cyrus
>    agent; find it via `linearClient.users` or the Linear admin UI.
> 6. Dry-run first: `gleaner tick --config gleaner.config.yaml
>    --dry-run`. Confirms the ranking and quota gate against the live
>    board without touching state. Then drop `--dry-run` for the real
>    handoff.
>
> **Never invoke `claude` or `codex` as a subprocess to read quota** —
> gleaner does it for free via the journals, and shelling out burns
> tokens against the very window we're measuring.

---

## What it does

```
   timer (every 10 min)
     └── gleaner tick
           ├── kill-switch + hours-of-day gate
           ├── Linear ListActive (filter: unassigned OR owned by Cyrus,
           │                       no outstanding blockers)
           ├── sort: priority asc (0 last), createdAt asc, identifier lex
           ├── quota gate: short window (active vs idle ceiling) + long
           │              window ceiling, per provider — first denial wins
           └── Linear issueUpdate { assigneeId: cyrus } → Cyrus webhook
```

Gleaner does NOT execute coding agents. It doesn't worktree, run
`claude`, open PRs, post comments, or move ticket states. Once a ticket
is assigned to the Cyrus user, Linear's Agent session event protocol
hands the rest to Cyrus.

## Subcommands

- `gleaner snapshot` — print Claude + Codex quota at zero token cost.
- `gleaner tick --config <yaml> [--dry-run]` — one picker pass. The
  systemd timer fires this every 10 min.

## The design claim

Both Claude Code and Codex CLI already expose usable utilization at zero
token cost. Gleaner reads those journals to gate handoff: don't dispatch
a ticket when the next 5-hour window is already burning down.

| Provider | Source                                            | Cost     | Notes |
|----------|---------------------------------------------------|----------|-------|
| Claude   | `https://api.anthropic.com/api/oauth/usage`        | 0 tokens | OAuth metadata endpoint; does not draw quota. Needs `~/.claude/.credentials.json`. |
| Codex    | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`     | 0 tokens | `event_msg.payload.type=="token_count"` events. |

## Config

```yaml
# gleaner.config.yaml — minimum useful deployment config
tracker:
  api_key_file: /run/agenix/linear-api-key   # `lin_api_…` key
  team_key: NSI                              # issue prefix (NSI-42)
  cyrus_user_id: 1bba8e2e-…                  # Linear user id of the Cyrus agent
  # active_states defaults to ["Todo", "In Progress"]

# All optional; sensible defaults shown.
# hours:
#   active: "09:00-19:00"   # stricter short_window_active ceiling applies
#   drain:  "22:00-07:00"   # permissive short_window_idle ceiling applies
# guards:
#   short_window_idle:   0.75
#   short_window_active: 0.30
#   long_window_ceiling: 0.92
# safety:
#   kill_switch: /var/lib/gleaner/disabled   # touch this file to halt picks
```

## Quickstart

```
nix build && ./result/bin/gleaner snapshot
```

or with a Go 1.25+ toolchain:

```
go build -o gleaner ./cmd/gleaner && ./gleaner snapshot
```

```
$ gleaner snapshot
claude (max_5x)   [source: /api/oauth/usage]:
  short   83.0%   resets in 0h 12m
  long    44.0%   resets in 14h 23m
codex (plus)   [source: rollout-2026-05-10T14-34-49.jsonl]:
  short    1.0%   resets in 4h 15m
  long     0.0%   resets in 6d 18h
```

## NixOS module

```nix
{
  inputs.gleaner.url = "github:nSimonFR/gleaner";

  outputs = { self, nixpkgs, gleaner, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      modules = [
        gleaner.nixosModules.gleaner
        ({
          services.gleaner = {
            enable     = true;
            user       = "nsimon";              # must own ~/.claude and ~/.codex
            configFile = ./gleaner.config.yaml;
            # timer.onUnitActiveSec = "10min";  # default
            # timer.onBootSec       = "2min";   # default
          };
        })
      ];
    };
  };
}
```

The module ships `gleaner.service` (Type=oneshot, runs `gleaner tick
--config <configFile>`) plus `gleaner.timer` (`Persistent=true`).
`ProtectHome` is off because gleaner must read the operator user's
`~/.claude/.credentials.json` and `~/.codex/sessions/`.

## Layout

- `cmd/gleaner/` — `snapshot`, `tick`
- `internal/adapter/{quota,claude_oauth,codex_journal}/` — quota engine
- `internal/adapter/tracker/linear/` — Linear GraphQL client
- `internal/predicate/` — kill-switch / hours / quota gate
- `internal/picker/` — the tick logic
- `internal/{config,logging}/`
- `flake.nix` + `module.nix`

## See also

- [IDEAS.md](./IDEAS.md) — the next pickers (PRs needing review, PRs
  needing test runs), per-model sub-bucket routing, multi-team backlog
  pooling.

## License

MIT.
