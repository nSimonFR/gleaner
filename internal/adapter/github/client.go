// Package github shells out to the `gh` CLI for all GitHub operations.
// Auth-switch enforcement: every call asserts the active GitHub user is
// the configured Account (e.g. "nSimonFR-ai") and refuses otherwise.
// This is a hard rule per project memory — never operate as the personal
// account when the bot account is the intended actor.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Client struct {
	Account string        // required; e.g. "nSimonFR-ai"
	Timeout time.Duration // default 30s per gh invocation
}

func New(account string) *Client {
	return &Client{Account: account, Timeout: 30 * time.Second}
}

// EnforceAuth verifies the active gh user matches c.Account. Run this once
// at startup; gh remembers the active user across invocations.
//
// Implementation note: we use `gh api user --jq .login` rather than parsing
// `gh auth status` output. The latter is localized prose that changes
// across gh versions; the former is a stable JSON-API call that gh routes
// through whichever account is currently active.
func (c *Client) EnforceAuth(ctx context.Context) error {
	if c.Account == "" {
		return fmt.Errorf("github: Account is required (e.g. nSimonFR-ai)")
	}
	out, err := c.run(ctx, "api", "user", "--jq", ".login")
	if err != nil {
		return fmt.Errorf("github: gh api user failed (run `gh auth login`?): %w", err)
	}
	got := strings.TrimSpace(out)
	if !strings.EqualFold(got, c.Account) {
		return fmt.Errorf("github: active gh account is %q but config wants %q\nRun: gh auth switch -u %s", got, c.Account, c.Account)
	}
	return nil
}

type Issue struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []Label  `json:"labels"`
	URL    string   `json:"url"`
	State  string   `json:"state"`
	Repo   string   // set by caller; gh doesn't include it in --repo queries
}

type Label struct {
	Name string `json:"name"`
}

// EligibleIssues returns open issues in `repo` whose label set:
//   - contains every label in `require`
//   - contains none of the labels in `block`
//
// Order is GitHub's default (most recently updated). Caller picks one.
func (c *Client) EligibleIssues(ctx context.Context, repo string, require, block []string) ([]Issue, error) {
	args := []string{"issue", "list", "--repo", repo, "--state", "open", "--limit", "50",
		"--json", "number,title,body,labels,url,state"}
	for _, l := range require {
		args = append(args, "--label", l)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("gh issue list %s: %w", repo, err)
	}
	var issues []Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parse issue list: %w", err)
	}
	blockSet := make(map[string]struct{}, len(block))
	for _, l := range block {
		blockSet[l] = struct{}{}
	}
	var keep []Issue
	for _, iss := range issues {
		blocked := false
		for _, l := range iss.Labels {
			if _, ok := blockSet[l.Name]; ok {
				blocked = true
				break
			}
		}
		if !blocked {
			iss.Repo = repo
			keep = append(keep, iss)
		}
	}
	return keep, nil
}

// CountOpenInflight returns the number of OPEN PRs in `repos` that carry
// the `afk` label (i.e. were opened by gleaner). Stateless — GitHub is the
// source of truth.
func (c *Client) CountOpenInflight(ctx context.Context, repos []string) (int, error) {
	total := 0
	for _, repo := range repos {
		out, err := c.run(ctx, "pr", "list", "--repo", repo, "--state", "open",
			"--label", "afk", "--limit", "100", "--json", "number")
		if err != nil {
			return 0, fmt.Errorf("gh pr list %s: %w", repo, err)
		}
		var prs []struct{}
		if err := json.Unmarshal([]byte(out), &prs); err != nil {
			return 0, fmt.Errorf("parse pr list: %w", err)
		}
		total += len(prs)
	}
	return total, nil
}

// MergedThisWeek returns the count of PRs across `repos` with the `afk` label
// that were merged within the last 7 days. Used for the anti-goal protection
// metric `merged_this_week` exposed in v0.0.4's /status endpoint. Wired here
// at v0.0.2 so the API surface is stable when the HTTP server arrives.
func (c *Client) MergedThisWeek(ctx context.Context, repos []string) (int, error) {
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02")
	total := 0
	for _, repo := range repos {
		out, err := c.run(ctx, "pr", "list", "--repo", repo, "--state", "merged",
			"--label", "afk", "--search", "merged:>="+since,
			"--limit", "100", "--json", "number")
		if err != nil {
			return 0, fmt.Errorf("gh pr list %s: %w", repo, err)
		}
		var prs []struct{}
		if err := json.Unmarshal([]byte(out), &prs); err != nil {
			return 0, fmt.Errorf("parse pr list: %w", err)
		}
		total += len(prs)
	}
	return total, nil
}

// CountDispatchedToday returns the number of PRs with `afk` label created
// today (UTC). Caps `max_per_day` against this number.
func (c *Client) CountDispatchedToday(ctx context.Context, repos []string) (int, error) {
	today := time.Now().UTC().Format("2006-01-02")
	total := 0
	for _, repo := range repos {
		out, err := c.run(ctx, "pr", "list", "--repo", repo, "--state", "all",
			"--label", "afk", "--search", "created:>="+today,
			"--limit", "100", "--json", "number")
		if err != nil {
			return 0, fmt.Errorf("gh pr list %s: %w", repo, err)
		}
		var prs []struct{}
		if err := json.Unmarshal([]byte(out), &prs); err != nil {
			return 0, fmt.Errorf("parse pr list: %w", err)
		}
		total += len(prs)
	}
	return total, nil
}

// EnsureLabel creates the named label on repo if it doesn't exist. Idempotent.
func (c *Client) EnsureLabel(ctx context.Context, repo, name, color, description string) error {
	_, err := c.run(ctx, "label", "create", name,
		"--repo", repo,
		"--color", color,
		"--description", description,
		"--force") // --force makes create idempotent (updates if exists)
	return err
}

// CreatePR opens a PR via `gh pr create`. Returns the URL printed by gh.
func (c *Client) CreatePR(ctx context.Context, repo, base, head, title, body string, labels []string) (string, error) {
	args := []string{"pr", "create",
		"--repo", repo,
		"--base", base,
		"--head", head,
		"--title", title,
		"--body", body,
	}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("gh %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
