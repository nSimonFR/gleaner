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

**v0.0.3** — four subcommands ship; the daemon is deployed on the author's
rpi5 as `services.gleaner`. A reviewer pass closed twelve issues on top of
the initial v0.0.3 work.

- `gleaner snapshot` — read both providers' quota at zero token cost.
- `gleaner drain --config <yaml>` — single-shot dispatch (predicate →
  one issue → one PR labeled `afk` + `needs-review` with a metadata block).
- `gleaner serve --config <yaml>` — long-running daemon for non-systemd
  setups. The shipped NixOS module uses the simpler timer-fires-drain
  pattern instead; `serve` is for testing or other init systems.
- `gleaner bootstrap --config <yaml>` — idempotent label creation
  across configured repos (`afk-ready`, `complexity:{trivial,routine,hard}`,
  `needs-human`, `blocked`, `wip`, `afk`, `needs-review`).
- NixOS module: `services.gleaner.{enable, user, configFile, ...}`.

Next: HTTP `/status` for a homepage widget (v0.0.4); worked example of
the Codex executor profile (v0.0.5; the executor mechanism already
supports it via `run: ["codex", "exec", "{prompt}"]`). Everything
deliberately deferred past v0.0.5 lives in [IDEAS.md](./IDEAS.md).

## The design claim

Both Claude Code and Codex CLI already expose usable utilization at zero
token cost. Gleaner's load-bearing innovation is **reading the journals
the CLIs already write**, never invoking them as subprocesses to ask
"how much quota do I have left?".

| Provider | Source                                            | Cost     | Notes                                                            |
|----------|---------------------------------------------------|----------|------------------------------------------------------------------|
| Claude   | `https://api.anthropic.com/api/oauth/usage`        | 0 tokens | OAuth metadata endpoint; does not draw quota. Needs `~/.claude/.credentials.json`. |
| Codex    | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`     | 0 tokens | `event_msg.payload.type=="token_count"` events; Codex pre-computes `used_percent`. |

Validating that one `QuotaSource` interface fits both providers cleanly
was v0.0.1's acceptance test for the multi-provider premise. It did.

## Quickstart

```
nix build
./result/bin/gleaner snapshot
```

Or with a Go toolchain (1.25+):

```
go build -o gleaner ./cmd/gleaner
./gleaner snapshot
```

Example output:

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

`snapshot` requires no config. `drain`, `serve`, and `bootstrap` all
take `--config <yaml>`.

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

## Setup for AI coding agents

You will land in this repo cold. Read this before doing anything.

### Toolchain, dev shell, build

- **Go 1.25.8** (`go.mod`). Single direct dep: `gopkg.in/yaml.v3`,
  vendored under `vendor/` (`vendorHash = null` in `flake.nix`). Run
  `go mod vendor` after any dependency change.
- `nix develop` provides `go`, `gopls`, `golangci-lint`. The flake
  also exposes `packages.default` and `nixosModules.gleaner`.
- Build with `nix build` (produces `./result/bin/gleaner`) or
  `go build -o gleaner ./cmd/gleaner`.

### Tests

**None yet.** Reserved for v0.1 — integration tests using `gh`-stub
binaries and a fake credentials file will land under `internal/*/_test.go`.
Don't add tests opportunistically; the discipline is "tests when the
failure mode they prevent has been seen once".

### Smoke-test against a safe config

`gleaner.test.yaml` shape (point at a throwaway repo, never burn quota):

```yaml
account: nSimonFR-ai
repos:    [nSimonFR/gleaner-test]
profiles: [{ match: "*", run: ["sh", "-c", "echo dry-run: $GLEANER_PROMPT >&2"], on_success: none }]
hours:    { drain: "00:00-24:00", poll: 1m }
guards:   { inflight_prs: 99 }
```

```
./result/bin/gleaner drain --config /tmp/gleaner.test.yaml --dry-run
./result/bin/gleaner drain --config /tmp/gleaner.test.yaml --worktree-root /tmp/gleaner-wt
```

`--dry-run` evaluates the predicate and exits; the second invocation
runs the executor but `on_success: none` means no PR opens.

### Vetting before commit

```
go vet ./...
go build ./...
```

`golangci-lint run` is available in the dev shell but not yet wired
to a hook.

### Hard rules

- **Never invoke `claude` or `codex` as subprocesses to read quota.**
  Zero-cost reads from local journals are the load-bearing innovation.
  An adapter that shells out to a CLI defeats the whole project.
- **`gh auth switch -u nSimonFR-ai` before any GitHub op.** The
  `internal/adapter/github` client enforces this via `EnforceAuth` —
  every `drain` and `bootstrap` invocation asserts the active user
  matches `cfg.Account` and refuses otherwise. Don't disable this
  check.
- **No `state.json`, no SQLite, no embedded KV.** Gleaner is stateless
  by design: inflight count comes from `gh pr list --label afk --state open`,
  daily count from `gh pr list --search created:>=<today>`, merge count
  from `gh pr list --state merged --search merged:>=<7d>`. GitHub is the
  source of truth. If you find yourself reaching for persistent state,
  you're solving the wrong problem — the loop is already idempotent.
- **`IDEAS.md` is one-way.** Anything past v0.0.5 must be promoted out
  of `IDEAS.md` by articulating which existing failure mode the idea
  addresses, not added directly to v0.0.x. Speculative features rot
  under `IDEAS.md` until reality justifies them.

### Adding a new executor profile

Config-only — no Go code. Append to the user's `profiles:` list:

```yaml
- { match: "review",
    run:   ["npx", "opencastle", "run", "review.convoy.yml"],
    name:  opencastle-review,
    on_success: comment }
```

If `name` is omitted, it's derived from `run[0]` (or
`opencastle-<convoy>` for `npx opencastle run X.convoy.yml`). If
`plan` is omitted, it's inferred from `run[0]` (`claude`,
`codex`). See `internal/config/config.go: deriveProfileName`,
`derivePlanFromRun`.

### Adding a new provider adapter

1. Implement `QuotaSource` (`internal/adapter/quota.go`) in
   `internal/adapter/<name>/`. The interface is two methods:
   `Snapshot(ctx) (*UsageSnapshot, error)` and `Provider() string`.
2. Register the adapter in both `cmd/gleaner/snapshot.go` and
   `cmd/gleaner/drain.go` — each builds a `[]adapter.QuotaSource`
   slice for the predicate and snapshot pipelines. (`serve.go`
   builds the same slice; update it too.)
3. Cost gate: if your adapter needs to invoke the provider's CLI or
   spend tokens to read utilization, the design is wrong. Find the
   on-disk artifact the CLI already writes.

### Commit messages

Look at `git log --oneline` and mimic the pattern. v0.0.x commits use:

- `vX.Y.Z: <short description>` for version-shipping commits
- `fix: <what>` for bug fixes
- `docs: <what>` for README / IDEAS / report updates

No conventional-commit scopes, no trailers. Subagent-reviewed commits
are folded into a single `fix:` rather than per-issue commits.

---

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

The module ships `gleaner.service` (Type=oneshot, runs
`gleaner drain --config <configFile>`) plus `gleaner.timer`
(`OnUnitActiveSec=10min`, `Persistent=true`). `ProtectHome` is
disabled because gleaner must read the operator user's
`~/.claude/.credentials.json` and `~/.codex/sessions/`.

The configured `user` is intentionally a real human user, not a
synthetic service account — gleaner reads *that user's* quota
journals, and a synthetic user has none.

---

## See also

- [IDEAS.md](./IDEAS.md) — explicitly deferred past v0.0.5: extra
  executor profiles, hook recipes, auto-merge, quota optimization,
  observability extensions, routing, storage.

## License

MIT.
