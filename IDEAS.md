# Ideas — not on the v0.3.x path

Stuff considered, deliberately cut. The v0.3 rescope deletes most of
what used to live here (worktrees, hooks, routing, auto-merge, web UI)
because those concerns moved out of gleaner's scope entirely — they
belong in whatever the user's `run` command invokes (a coding agent, a
skill, a workflow).

What stays interesting:

## Expression language

- Function calls in `when` (`minutes_until(claude.short_reset) > 30`)
  for time-of-day gating beyond "raw percent". Right now the systemd
  timer's `OnCalendar=` is the only time gate.
- Per-provider `*.plan` and `*.extra_usage` reference in `when` so
  policies can differ for Team vs Pro Claude accounts.

## Tick semantics

- Per-trigger cooldown (`min_interval: 1h`) backed by a state file in
  `RuntimeDirectory` — currently the user expresses this via the timer
  cadence or via state inside the `run` command.
- Parallel exec when triggers are mutually independent — currently
  serial, which is fine because the typical `run` is a fast Linear
  reassign.

## Snapshot ergonomics

- `gleaner snapshot --watch` for live-updating utilization in a TUI.
- Cache the last successful snapshot to disk so a transient OAuth fail
  doesn't gate every trigger off.

## Multi-host

- Read snapshots from a remote host (`snapshot --from rpi5:`) so a
  workstation can decide whether the rpi5's quota is free.
