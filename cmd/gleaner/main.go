// Gleaner is a quota-aware coding-agent dispatcher.
// v0.0.1 ships the `snapshot` subcommand only — it reads each provider's
// canonical, zero-cost quota source and prints utilization.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const version = "0.0.1"

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
	fmt.Fprintf(os.Stderr, `gleaner %s — quota-aware coding-agent dispatcher

usage:
  gleaner snapshot [--json]     print current quota utilization for both providers
  gleaner version
  gleaner help

v0.0.1: snapshot is the only subcommand. drain/serve land in v0.0.2/v0.0.3.
`, version)
}
