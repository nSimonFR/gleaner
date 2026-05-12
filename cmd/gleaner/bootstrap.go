package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	codehost "github.com/nSimonFR/gleaner/internal/adapter/codehost/github"
	tgithub "github.com/nSimonFR/gleaner/internal/adapter/tracker/github"
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

// bootstrapCmd creates the 9 gleaner labels on each configured GitHub
// codehost repo. Labels live on GitHub regardless of the tracker kind —
// for kind=linear, we bootstrap labels on cfg.Tracker.CodehostRepo so the
// PRs gleaner opens are correctly tagged.
func bootstrapCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to YAML config (uses tracker.repos and tracker.account)")
	repoOverride := fs.String("repo", "", "single repo (owner/name); overrides config.repos but still requires --config for .account")
	accountFlag := fs.String("account", "", "GitHub account (e.g. nSimonFR-ai); overrides config.account. Required if --config is not given.")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var repos []string
	var account string
	if *cfgPath != "" {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			return 1
		}
		switch cfg.Tracker.Kind {
		case "github":
			repos = cfg.Tracker.Repos
			account = cfg.Tracker.Account
		case "linear":
			if cfg.Tracker.CodehostRepo != "" {
				repos = []string{cfg.Tracker.CodehostRepo}
			}
			account = cfg.Tracker.Account
		default:
			repos = cfg.Repos
			account = cfg.Account
		}
	}
	if *repoOverride != "" {
		repos = []string{*repoOverride}
	}
	if *accountFlag != "" {
		account = *accountFlag
	}

	if account == "" {
		fmt.Fprintln(os.Stderr, "bootstrap: account is required (use --config with tracker.account set, or pass --account)")
		return 1
	}
	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "bootstrap: no repos to bootstrap")
		return 1
	}

	// Use the github tracker's EnforceAuth just to validate the active
	// gh user matches. We don't actually use the tracker for anything
	// else in bootstrap.
	authCheck := tgithub.New(account, nil, nil, nil)
	if err := authCheck.EnforceAuth(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	ch := codehost.New(account)
	for _, repo := range repos {
		fmt.Printf("repo %s:\n", repo)
		for _, l := range gleanerLabels {
			err := ch.EnsureLabel(ctx, repo, l.Name, l.Color, l.Desc)
			if err != nil {
				fmt.Printf("  %s: ERROR %v\n", l.Name, err)
				continue
			}
			fmt.Printf("  %s: ok\n", l.Name)
		}
	}
	return 0
}
