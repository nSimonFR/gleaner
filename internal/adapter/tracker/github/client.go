// Package github implements the tracker.Tracker interface against GitHub
// Issues via the `gh` CLI. Issues are filtered by require/block labels
// configured at the tracker level — the "afk-ready" workflow gleaner has
// shipped since v0.0.2.
//
// Auth-switch enforcement is mandatory: every gh op runs as the configured
// Account (e.g. "nSimonFR-ai") to prevent accidentally operating from the
// owner account. EnforceAuth() checks once at startup.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
)

// Client is the github-tracker implementation. Construct with New().
type Client struct {
	Account string        // required; e.g. "nSimonFR-ai"
	Repos   []string      // owner/name pairs the tracker watches
	Require []string      // labels every active issue must carry
	Block   []string      // labels that disqualify an issue
	Timeout time.Duration // per gh invocation; defaults to 30s
}

// New constructs a github tracker. All fields except Timeout are required
// for ListActive to return useful results; Timeout defaults to 30s.
func New(account string, repos, require, block []string) *Client {
	return &Client{
		Account: account,
		Repos:   repos,
		Require: require,
		Block:   block,
		Timeout: 30 * time.Second,
	}
}

// Kind returns "github" — used for session_id prefix and log context.
func (c *Client) Kind() string { return "github" }

// EnforceAuth verifies the active gh user matches c.Account. Uses
// `gh api user --jq .login` (stable JSON) rather than parsing the
// localized prose of `gh auth status`.
func (c *Client) EnforceAuth(ctx context.Context) error {
	if c.Account == "" {
		return fmt.Errorf("github tracker: Account is required (e.g. nSimonFR-ai)")
	}
	out, err := c.run(ctx, "api", "user", "--jq", ".login")
	if err != nil {
		return fmt.Errorf("github tracker: gh api user failed (run `gh auth login`?): %w", err)
	}
	got := strings.TrimSpace(out)
	if !strings.EqualFold(got, c.Account) {
		return fmt.Errorf("github tracker: active gh account is %q but config wants %q\nRun: gh auth switch -u %s", got, c.Account, c.Account)
	}
	return nil
}

// ghIssue mirrors the gh JSON shape we request (--json number,title,...).
type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Labels    []ghLabel `json:"labels"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ghLabel struct {
	Name string `json:"name"`
}

// ListActive walks every configured repo and returns open issues that
// carry every Require label and none of the Block labels.
//
// Order is gh's default (most recently updated). The orchestrator re-sorts
// per SPEC §8.1 step 4.
func (c *Client) ListActive(ctx context.Context) ([]tracker.Issue, error) {
	var out []tracker.Issue
	for _, repo := range c.Repos {
		issues, err := c.listRepo(ctx, repo)
		if err != nil {
			return nil, err
		}
		out = append(out, issues...)
	}
	return out, nil
}

func (c *Client) listRepo(ctx context.Context, repo string) ([]tracker.Issue, error) {
	args := []string{"issue", "list", "--repo", repo, "--state", "open", "--limit", "50",
		"--json", "number,title,body,labels,url,state,createdAt,updatedAt"}
	for _, l := range c.Require {
		args = append(args, "--label", l)
	}
	stdout, err := c.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("gh issue list %s: %w", repo, err)
	}
	var raw []ghIssue
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return nil, fmt.Errorf("parse issue list %s: %w", repo, err)
	}
	blockSet := make(map[string]struct{}, len(c.Block))
	for _, l := range c.Block {
		blockSet[l] = struct{}{}
	}
	var keep []tracker.Issue
	for _, r := range raw {
		blocked := false
		labels := make([]string, 0, len(r.Labels))
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
			if _, ok := blockSet[l.Name]; ok {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		keep = append(keep, tracker.Issue{
			ID:         fmt.Sprintf("%s#%d", repo, r.Number),
			Identifier: fmt.Sprintf("%s#%d", repo, r.Number),
			Repo:       repo,
			Number:     r.Number,
			Title:      r.Title,
			Body:       r.Body,
			Labels:     labels,
			State:      r.State,
			URL:        r.URL,
			CreatedAt:  r.CreatedAt,
			UpdatedAt:  r.UpdatedAt,
		})
	}
	return keep, nil
}

// GetState fetches the current open/closed state of a single issue. The
// issueID is the gleaner-canonical "<repo>#<number>" form produced by
// ListActive.
func (c *Client) GetState(ctx context.Context, issueID string) (string, error) {
	repo, num, err := parseGitHubIssueID(issueID)
	if err != nil {
		return "", err
	}
	stdout, err := c.run(ctx, "issue", "view", fmt.Sprintf("%d", num),
		"--repo", repo, "--json", "state")
	if err != nil {
		return "", fmt.Errorf("gh issue view %s: %w", issueID, err)
	}
	var resp struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		return "", fmt.Errorf("parse issue view %s: %w", issueID, err)
	}
	return resp.State, nil
}

// Comment posts a comment back on the issue. Used by the orchestrator to
// announce the resulting PR ("PR opened: <url>"). For github-tracker mode
// the comment goes on the GitHub issue itself.
func (c *Client) Comment(ctx context.Context, issueID, body string) error {
	repo, num, err := parseGitHubIssueID(issueID)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, "issue", "comment", fmt.Sprintf("%d", num),
		"--repo", repo, "--body", body)
	if err != nil {
		return fmt.Errorf("gh issue comment %s: %w", issueID, err)
	}
	return nil
}

// parseGitHubIssueID splits "owner/repo#N" into ("owner/repo", N).
func parseGitHubIssueID(id string) (string, int, error) {
	idx := strings.LastIndex(id, "#")
	if idx < 0 {
		return "", 0, fmt.Errorf("github tracker: invalid issue id %q (want owner/repo#N)", id)
	}
	repo := id[:idx]
	var num int
	if _, err := fmt.Sscanf(id[idx+1:], "%d", &num); err != nil {
		return "", 0, fmt.Errorf("github tracker: invalid issue id %q: %w", id, err)
	}
	return repo, num, nil
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

// Compile-time check that *Client implements tracker.Tracker.
var _ tracker.Tracker = (*Client)(nil)
