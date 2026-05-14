// Gleaner is a quota-gated cron dispatcher.
//
//   - `gleaner snapshot` prints Claude + Codex utilization at zero token
//     cost by reading the OAuth metadata endpoint and the Codex session
//     journals — never invoking the CLIs as subprocesses.
//   - `gleaner tick` reads a YAML config of `triggers`, each with a
//     `when` predicate over the snapshot and a `run` shell command, and
//     execs the matching ones. The systemd timer is the only driver.
//
// Linear / GitHub / agent orchestration are out of scope — the user's
// `run` command owns those (typically `claude -p "…"` or `codex run
// "…"`, leaning on the appropriate skill).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const version = "0.3.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch cmd {
	case "snapshot":
		os.Exit(snapshotCmd(ctx, args))
	case "tick":
		os.Exit(tickCmd(ctx, args))
	case "version", "--version", "-v":
		fmt.Println("gleaner", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "gleaner: unknown subcommand %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `gleaner %s — quota-gated cron dispatcher

usage:
  gleaner snapshot [--json] [--timeout N]
        print current Claude + Codex quota utilization (zero token cost)

  gleaner tick --config <yaml> [--dry-run] [--only NAME]
        evaluate each trigger's when-expression against the live snapshot;
        exec the matching run commands. Stateless. Designed for a systemd
        timer.

  gleaner version
  gleaner help
`, version)
}
