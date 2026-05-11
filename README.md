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

## AI Installation Prompt

> Install `gleaner` on a generic Linux or macOS host. **Done when** `gleaner snapshot` prints a non-empty utilization line for `claude` or `codex`.
>
> 1. Clone: `git clone https://github.com/nSimonFR/gleaner && cd gleaner`
> 2. Read first: `README.md`, `go.mod`, `flake.nix`, `examples/` if present. Toolchain is Go ≥ 1.25.
> 3. Build (try in this order, stop at the first that works):
>    - `nix build && cp result/bin/gleaner ~/.local/bin/`
>    - `go build -o ~/.local/bin/gleaner ./cmd/gleaner`
>    Verify: `gleaner --help` lists `snapshot`, `drain`, `serve`, `bootstrap`.
> 4. Prerequisites for `snapshot` to return real data:
>    - Claude: `~/.claude/.credentials.json` exists with a fresh OAuth token (run `claude /login` once if missing).
>    - Codex: at least one session journal at `~/.codex/sessions/**/*.jsonl` (run `codex` once if missing).
> 5. For daemon mode: write a `gleaner.config.yaml` (see the **Config** section below; the 7-line example is enough), then `gleaner serve --config gleaner.config.yaml`. NixOS hosts can use `services.gleaner` instead — see `module.nix`.
> 6. Before any GitHub op against the user's repos, run `gh auth switch -u nSimonFR-ai` (the bot account, not the personal one).
>
> **Never invoke `claude` or `codex` as a subprocess to read quota** — gleaner does it for free via the journals, and shelling out burns tokens against the very window we're measuring.
## Status

**v0.0.3** — four subcommands ship; the daemon is deployed on the author's
rpi5 as `services.gleaner`.

- `gleaner snapshot` — print both providers' quota at zero token cost.
- `gleaner drain --config <yaml>` — single-shot dispatch (predicate →
  one issue → one PR labeled `afk` + `needs-review`).
- `gleaner serve --config <yaml>` — long-running daemon for non-systemd
  setups. The NixOS module uses timer-fires-drain instead; `serve` is
  for testing or other init systems.
- `gleaner bootstrap --config <yaml>` — idempotent label creation
  (`afk-ready`, `complexity:{trivial,routine,hard}`, `needs-human`,
  `blocked`, `wip`, `afk`, `needs-review`).

Next: HTTP `/status` for a homepage widget (v0.0.4); a worked Codex
executor profile (v0.0.5; the executor already supports it). Everything
deferred past v0.0.5 lives in [IDEAS.md](./IDEAS.md).

## The design claim

Both Claude Code and Codex CLI already expose usable utilization at zero
token cost. Gleaner's load-bearing innovation is **reading the journals
the CLIs already write**, never invoking them as subprocesses.

| Provider | Source                                            | Cost     | Notes                                                            |
|----------|---------------------------------------------------|----------|------------------------------------------------------------------|
| Claude   | `https://api.anthropic.com/api/oauth/usage`        | 0 tokens | OAuth metadata endpoint; does not draw quota. Needs `~/.claude/.credentials.json`. |
| Codex    | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`     | 0 tokens | `event_msg.payload.type=="token_count"` events; Codex pre-computes `used_percent`. |

Validating that one `QuotaSource` interface fits both providers cleanly
was v0.0.1's acceptance test for the multi-provider premise. It did.

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
  sub-buckets: omelette  0.0% / sonnet  0.0% /

codex (plus)   [source: rollout-2026-05-10T14-34-49-019e11e2.jsonl]:
  short    1.0%   resets in 4h 15m
  long     0.0%   resets in 6d 18h
```

`snapshot` needs no config. `drain`, `serve`, `bootstrap` take `--config <yaml>`.

## Config

The user-facing surface is deliberately tiny: aggressive package
defaults absorb timezone, hours, polling, label sets, guard
thresholds, safety caps, profile names, and plan inference. Users
write only what diverges.

```yaml
# gleaner.config.yaml — minimum useful deployment config
account: nSimonFR-ai
repos:   [nSimonFR/nic-os, nSimonFR/for-sure]

profiles:
  - { match: ["lang:python", "provider:codex"], run: ["codex", "exec", "{prompt}"] }
  - { match: "*",                                run: ["claude", "-p", "{prompt}"] }

hook: /etc/gleaner/hooks/dispatch.sh
```

- `match` — string or list of GitHub label names. The literal `"*"`
  matches everything. **First match wins.**
- `run` — argv list; **no shell**, so issue titles can't inject. Template
  variables are substituted in each element: `{prompt}`, `{worktree}`,
  `{repo}`, `{issue_title}`, `{issue_body}`, `{issue_number}`. The same
  values are also exported as `GLEANER_PROMPT`, `GLEANER_WORKTREE`,
  `GLEANER_REPO`, `GLEANER_ISSUE` in the child env.
- `on_success` — one of `open_pr` (default), `comment`, `none`. Only
  `open_pr` pushes the branch and calls `gh pr create`.
- `hook` — optional path to a script gleaner fork-execs on lifecycle
  events (`pr_opened`, `dispatch_failed`, …). Event name is `$1`,
  JSON payload arrives on stdin. Hook failures are logged, never block.

Issues missing a `complexity:*` label are skipped on purpose — the
`"*"` profile must not silently route un-triaged work to the default
model. Run `gleaner bootstrap` to create the label set.

## Architecture

Stateless by design. GitHub is the source of truth for inflight count,
daily count, label state, and merge history.

```
  timer → drain
    ├── predicate (internal/predicate)
    │     kill-switch → hours → quota windows → inflight PRs → daily cap
    │     each QuotaSource snapshots short + long; ceilings differ by hour
    ├── pickIssue (internal/adapter/github)
    │     walk repos, require/block labels, skip missing complexity:*,
    │     MatchProfile() over labels (first match wins)
    ├── executor.Run (internal/executor)
    │     git worktree add → template-sub {prompt}/{worktree}/… →
    │     exec profile.run → status --porcelain
    └── PR opener
          push branch → gh pr create with [afk, needs-review]
          → hook.Fire("pr_opened", …) best-effort
```

`serve` wraps the same `drain` machinery in a `time.Ticker` loop —
identical evaluation, longer-lived process. Layout:

- `cmd/gleaner/` — `snapshot`, `drain`, `serve`, `bootstrap`
- `internal/adapter/{quota,claude_oauth,codex_journal,github}/`
- `internal/{config,predicate,executor,hook}/`
- `flake.nix` + `module.nix`

---

## NixOS module

```nix
{
  inputs.gleaner.url = "github:nSimonFR-ai/gleaner";

  outputs = { self, nixpkgs, gleaner, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      modules = [
        gleaner.nixosModules.gleaner
        ({
          services.gleaner = {
            enable     = true;
            user       = "nsimon";              # must own ~/.claude and ~/.codex
            configFile = ./gleaner.config.yaml; # the YAML above
            # workTreeRoot = "/var/lib/gleaner/worktrees";  # default
            # timer.onUnitActiveSec = "10min";              # default
            # timer.onBootSec       = "2min";               # default
          };
        })
      ];
    };
  };
}
```

The module ships `gleaner.service` (Type=oneshot, runs `gleaner drain
--config <configFile>`) plus `gleaner.timer` (`Persistent=true`).
`ProtectHome` is off because gleaner must read the operator user's
`~/.claude/.credentials.json` and `~/.codex/sessions/`. The configured
`user` is intentionally a real human user — gleaner reads *that user's*
quota journals.

---

## See also

- [IDEAS.md](./IDEAS.md) — explicitly deferred past v0.0.5: extra
  executor profiles, hook recipes, auto-merge, quota optimization,
  observability extensions, routing.

## License

MIT.
