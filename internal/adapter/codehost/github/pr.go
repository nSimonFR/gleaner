// Package github implements the "codehost" half of GitHub interaction —
// the things that are not tracker concerns: PR creation, label bootstrap,
// inflight/merged/daily-dispatch PR counts.
//
// Why split from the tracker package? Code lives on GitHub regardless of
// where issues live. When tracker.kind=linear, gleaner still opens GitHub
// PRs; only the issue source changes. Keeping these methods in a separate
// package makes that boundary explicit and lets the predicate import only
// what it needs.
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

// Client is the github codehost. Construct with New().
type Client struct {
	Account string        // for auth context (assumes EnforceAuth ran via tracker)
	Timeout time.Duration // defaults to 30s
}

// New constructs a github codehost client. Auth is enforced upstream
// (typically by the github tracker's EnforceAuth at startup); gh is
// stateful re: active account so both packages see the same identity.
func New(account string) *Client {
	return &Client{Account: account, Timeout: 30 * time.Second}
}

// CreatePR opens a PR via `gh pr create`. Returns the URL that gh prints.
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

// CountOpenInflight returns the number of OPEN PRs across `repos` carrying
// the `afk` label (i.e. opened by gleaner). Stateless — GitHub is the
// source of truth for the in-flight count used by the predicate.
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

// MergedThisWeek returns the count of `afk`-labeled PRs across `repos`
// merged within the last 7 days. Surfaced via the v0.0.4 /status endpoint
// for the `merged_pr_equivalent_per_week` metric — gleaner's load-bearing
// number.
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

// CountDispatchedToday returns the count of `afk`-labeled PRs across
// `repos` created today (UTC). Used by the daily-cap safety guard.
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

// EnsureLabel creates the named label on `repo` if it doesn't exist. With
// `--force` gh updates color/description if the label already exists, so
// this is idempotent across boots. Used by the bootstrap subcommand.
func (c *Client) EnsureLabel(ctx context.Context, repo, name, color, description string) error {
	_, err := c.run(ctx, "label", "create", name,
		"--repo", repo,
		"--color", color,
		"--description", description,
		"--force")
	return err
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
