package main

import (
	"fmt"

	codehost "github.com/nSimonFR/gleaner/internal/adapter/codehost/github"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	tgithub "github.com/nSimonFR/gleaner/internal/adapter/tracker/github"
	tlinear "github.com/nSimonFR/gleaner/internal/adapter/tracker/linear"
	"github.com/nSimonFR/gleaner/internal/config"
)

// buildTracker constructs the adapter selected by cfg.Tracker.Kind. Validate()
// ensures Kind is set to a supported value, so the default arm is unreachable
// at runtime — it exists as a defensive fallback.
func buildTracker(cfg *config.Config) (tracker.Tracker, error) {
	switch cfg.Tracker.Kind {
	case "github":
		c := tgithub.New(
			cfg.Tracker.Account,
			cfg.Tracker.Repos,
			cfg.Tracker.Require,
			cfg.Tracker.Block,
		)
		// Projects v2 wiring for SetState (SPEC §7.1). All optional —
		// when ProjectID/StatusFieldID are empty the adapter auto-discovers
		// from the first issue passed to SetState.
		c.ProjectID = cfg.Tracker.ProjectID
		c.StatusFieldID = cfg.Tracker.StatusFieldID
		c.StatusFieldName = cfg.Tracker.StatusFieldName
		return c, nil
	case "linear":
		return tlinear.New(
			cfg.Tracker.APIKeyFile,
			cfg.Tracker.TeamKey,
			cfg.Tracker.CodehostRepo,
			cfg.Tracker.ActiveStates,
		), nil
	default:
		return nil, fmt.Errorf("wire: unsupported tracker.kind %q", cfg.Tracker.Kind)
	}
}

// codehostRepos returns the set of GitHub repos against which PR-counting
// guards (inflight, daily) are evaluated. For kind=github this is just
// cfg.Tracker.Repos; for kind=linear it's the single CodehostRepo (the
// repo where gleaner-opened PRs actually land).
func codehostRepos(cfg *config.Config) []string {
	if cfg.Tracker.Kind == "linear" && cfg.Tracker.CodehostRepo != "" {
		return []string{cfg.Tracker.CodehostRepo}
	}
	return cfg.Tracker.Repos
}

// buildCodeHost constructs the GitHub codehost client. The account it
// authenticates against is the same `gh` active account validated by the
// github tracker's EnforceAuth — even when tracker.kind=linear, code lives
// on GitHub, so an authenticated gh CLI is still required.
func buildCodeHost(cfg *config.Config) *codehost.Client {
	account := cfg.Tracker.Account
	if account == "" {
		account = cfg.Account // legacy fallback
	}
	return codehost.New(account)
}
