// Gleaner is a quota-aware Linear ticket picker that hands tickets off
// to Cyrus by reassigning them. It does not execute coding agents itself
// — Cyrus listens for Linear Agent session events and does the work.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const version = "0.2.0"

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
	fmt.Fprintf(os.Stderr, `gleaner %s — quota-aware Linear ticket picker

usage:
  gleaner snapshot [--json]            print current quota utilization
  gleaner tick --config <yaml> [--dry-run]
                                       run one picker pass: read Linear,
                                       check quota, hand off top candidate
                                       to Cyrus via assignment. Idempotent.
  gleaner version
  gleaner help
`, version)
}
