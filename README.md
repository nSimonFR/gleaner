# gleaner

A quota-gated cron dispatcher. Reads Claude + Codex utilization at zero
token cost, evaluates a user-declared `when` predicate per trigger, and
execs the matching `run` command. Drains idle hours into the
subscription window without burning tokens to find out whether tokens
are available.

**Metric**: `useful_work_per_week`
**Anti-goal**: `maximize_tokens_burned`

> The name is the agricultural term for someone who picks up grain left
> in the field after harvest. Value already paid for, recovered before
> it's plowed under. See Millet's *The Gleaners* (1857) for the visual
> cue, or Agnès Varda's *Les Glaneurs et la Glaneuse* (2000) for the
> modern reading.

---

## AI Installation Prompt

> Install `gleaner` on a generic Linux or macOS host. **Done when**
> `gleaner snapshot` prints non-empty utilization for `claude` or
> `codex`, and `gleaner tick --config gleaner.config.yaml --dry-run`
> logs at least one `trigger_would_run` or `trigger_skipped` event.
>
> 1. Clone: `git clone https://github.com/nSimonFR/gleaner && cd gleaner`
> 2. Read first: `README.md`, `go.mod`, `flake.nix`. Toolchain is Go ≥ 1.25.
> 3. Build (try in order, stop at the first that works):
>    - `nix build && cp result/bin/gleaner ~/.local/bin/`
>    - `go build -o ~/.local/bin/gleaner ./cmd/gleaner`
>    Verify: `gleaner --help` lists exactly `snapshot` and `tick`.
> 4. Prerequisites for `snapshot` to return real data:
>    - Claude: `~/.claude/.credentials.json` exists with a fresh OAuth
>      token (run `claude /login` once if missing).
>    - Codex: at least one session journal at
>      `~/.codex/sessions/**/*.jsonl` (run `codex` once if missing).
> 5. For scheduled mode: write a `gleaner.config.yaml` (see **Config**
>    below), then either run `gleaner tick` from your own cron/timer
>    or, on NixOS, use the `services.gleaner` module from `module.nix`.
> 6. Before any GitHub op against the user's repos, run
>    `gh auth switch -u nSimonFR-ai` (the bot account, not the personal one).
>
> **Never invoke `claude` or `codex` as a subprocess to read quota** —
> gleaner does it for free via the journals, and shelling out burns
> tokens against the very window we're measuring.

## Status

**v0.3.0** — rescoped. Gleaner is now two subcommands:

- `gleaner snapshot` — print both providers' quota at zero token cost.
- `gleaner tick --config <yaml>` — one-shot trigger evaluator. Reads
  the snapshot, evaluates each trigger's `when` expression, execs the
  matching ones. Stateless. Designed for a systemd timer.

The pre-0.3 orchestrator (worktrees, hooks, executor, Linear/GitHub
trackers, ranking, PR opener) has been removed — that work belongs in
whatever the user puts in `run`. Typically a `claude -p "…"` or `codex
run "…"` invocation that leans on a vendored skill (e.g. the `linear`
skill ships in `nic-os` at `shared/skills/linear/`).

## The design claim

Both Claude Code and Codex CLI already expose usable utilization at zero
token cost. Gleaner's only load-bearing innovation is **reading the
journals the CLIs already write**, never invoking them as subprocesses.

| Provider | Source                                            | Cost     | Notes                                                            |
|----------|---------------------------------------------------|----------|------------------------------------------------------------------|
| Claude   | `https://api.anthropic.com/api/oauth/usage`        | 0 tokens | OAuth metadata endpoint; does not draw quota. Needs `~/.claude/.credentials.json`. |
| Codex    | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`     | 0 tokens | `event_msg.payload.type=="token_count"` events; Codex pre-computes `used_percent`. |

## Quickstart

```
nix build && ./result/bin/gleaner snapshot
```

```
$ gleaner snapshot
claude (max_5x)   [source: /api/oauth/usage]:
  short   83.0%   resets in 0h 12m
  long    44.0%   resets in 14h 23m
codex (plus)   [source: rollout-2026-05-14T14-34-49.jsonl]:
  short    1.0%   resets in 4h 15m
  long     0.0%   resets in 6d 18h
```

`snapshot` needs no config. `tick` takes `--config <yaml>`.

## Config

```yaml
triggers:
  - name: cyrus-handoff
    when: "claude.long_pct < 50 && codex.short_pct < 80"
    timeout: 10m
    env:
      LINEAR_KEY_FILE: /run/agenix/linear-key
    run:
      - claude
      - -p
      - "Pick the next NSI ticket labelled cyrus-ready (oldest first), assign it to the cyrus user, and post a one-line ack comment. Use the linear skill. Quit immediately after."
```

Top-level: a list of `triggers`. Each trigger:

| field    | meaning                                                                       |
|----------|-------------------------------------------------------------------------------|
| `name`   | required, unique within the config; used in log lines and `--only`            |
| `when`   | required; quota expression (see grammar below)                                |
| `run`    | required; argv. `run[0]` is the command, the rest are args                    |
| `timeout`| optional; Go duration (default `5m`)                                          |
| `env`    | optional; extra env merged on top of the parent (overrides on collision)      |

### `when` expression grammar

```
expr  := and ('||' and)*
and   := cmp  ('&&' cmp )*
cmp   := atom OP atom        OP ∈ {<, <=, >, >=, ==, !=}
atom  := ident | number | bool
ident := <provider>.<field>
```

Identifiers resolved against the live snapshot:

| ident                  | value                                          |
|------------------------|------------------------------------------------|
| `claude.short_pct`     | `windows.short.used_percent × 100`             |
| `claude.long_pct`      | `windows.long.used_percent × 100`              |
| `claude.extra_usage`   | `extra_usage_enabled` (bool)                   |
| `claude.ok`            | snapshot fetched without error (bool)          |
| `codex.short_pct`      | same                                           |
| `codex.long_pct`       | same                                           |
| `codex.extra_usage`    | same                                           |
| `codex.ok`             | same                                           |

A provider whose snapshot failed gets `*.ok = false` and
`*_pct = +Inf`, so `<` predicates naturally exclude it.

Anything fancier than this grammar belongs in your `run` command, not
here.

## NixOS

```nix
{
  imports = [ gleaner.nixosModules.gleaner ];
  services.gleaner = {
    enable = true;
    user = "nsimon";
    configFile = ./gleaner.config.yaml;
    timer.onUnitActiveSec = "10min";
  };
}
```

The unit's environment doesn't carry trigger secrets — pass those via
each trigger's `env:` block so they live in your YAML next to the
command that needs them.

## Tick flow

```
gleaner tick --config gleaner.config.yaml [--dry-run] [--only NAME]
```

1. Load config. Empty `triggers` list → exit 0 with a log line.
2. Fetch snapshots from both providers in parallel.
3. For each trigger (in order):
   - Evaluate `when`. Parse / type error → log `trigger_parse_error`, skip.
   - False → log `trigger_skipped`.
   - True + `--dry-run` → log `trigger_would_run`.
   - True otherwise → exec `run` with the merged env and timeout. Log
     `trigger_ok` on success or `trigger_failed` (with truncated
     stderr) on non-zero exit / timeout.
4. Exit 0 even if some triggers failed — one bad trigger should never
   wedge the timer.

Triggers run sequentially within a tick. There is no shared state
between ticks; if a trigger needs to dedup or rate-limit itself, push
that into the `run` command (the skills that talk to Linear/GitHub
already do this).

## Logs

All output is on stderr in `event=value key=value …` format:

```
event=snapshot_ok provider=claude short_pct=83.0 long_pct=44.0
event=snapshot_ok provider=codex short_pct=1.0 long_pct=0.0
event=trigger_skipped name=cyrus-handoff reason=when_false
event=trigger_ok name=other duration_ms=482 stdout_bytes=120
```

Read with `journalctl -u gleaner.service -f`.
