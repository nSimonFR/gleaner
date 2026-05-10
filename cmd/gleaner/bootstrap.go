package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/nSimonFR/gleaner/internal/adapter/github"
	"github.com/nSimonFR/gleaner/internal/config"
)

// gleanerLabel pairs a label name with the color and description used when
// creating it via `gh label create`. Keep this stable — repo owners may
// edit colors/descriptions; gleaner only sets initial values.
type gleanerLabel struct{ Name, Color, Desc string }

var gleanerLabels = []gleanerLabel{
	{"afk-ready", "0e8a16", "Eligible for autonomous dispatch via gleaner"},
	{"complexity:trivial", "c5def5", "Typo, dep bump, README polish"},
	{"complexity:routine", "5319e7", "Refactor, test addition, small feature"},
	{"complexity:hard", "b60205", "Gnarly bug, architectural change"},
	{"needs-human", "fbca04", "Blocks gleaner — requires human intervention"},
	{"blocked", "d93f0b", "Blocks gleaner — waiting on something else"},
	{"wip", "fef2c0", "Blocks gleaner — work in progress on this issue"},
	{"afk", "1d76db", "PR opened by gleaner"},
	{"needs-review", "0052cc", "PR opened by gleaner; awaiting human review"},
}

func bootstrapCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to YAML config (uses .repos and .account)")
	repoOverride := fs.String("repo", "", "single repo (owner/name) instead of config.repos")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var repos []string
	var account string
	if *repoOverride != "" {
		repos = []string{*repoOverride}
		account = inferAccountFromGH(ctx)
	} else {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			return 1
		}
		repos = cfg.Repos
		account = cfg.Account
	}

	if account == "" {
		fmt.Fprintln(os.Stderr, "bootstrap: account is required (use --config with account: set, or run `gh auth switch` first)")
		return 1
	}
	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "bootstrap: no repos to bootstrap")
		return 1
	}
	gh := github.New(account)
	if err := gh.EnforceAuth(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	for _, repo := range repos {
		fmt.Printf("repo %s:\n", repo)
		for _, l := range gleanerLabels {
			err := gh.EnsureLabel(ctx, repo, l.Name, l.Color, l.Desc)
			if err != nil {
				fmt.Printf("  %s: ERROR %v\n", l.Name, err)
				continue
			}
			fmt.Printf("  %s: ok\n", l.Name)
		}
	}
	return 0
}

// inferAccountFromGH is a placeholder for v0.0.2 — always returns "" so
// `--repo` callers must explicitly pass `--config <yaml>` containing the
// `account:` key. Simpler than parsing `gh auth status` output, and any
// auth mismatch is caught by EnforceAuth before damage is done.
func inferAccountFromGH(_ context.Context) string {
	return ""
}
