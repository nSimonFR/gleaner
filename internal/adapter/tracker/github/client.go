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
	"sync"
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

	// Projects v2 wiring for SetState. ProjectID and StatusFieldID are
	// optional config overrides; when empty, both are auto-discovered
	// from the first issue passed to SetState. Cache is process-local;
	// if you run gleaner across repos whose issues live in DIFFERENT
	// projects, set ProjectID explicitly per Client.
	ProjectID       string // ProjectV2 node id; auto-discovered when empty
	StatusFieldName string // option-set field name; defaults to "Status"
	StatusFieldID   string // ProjectV2SingleSelectField id; auto-discovered when empty

	projectMu      sync.Mutex
	projectID      string            // resolved
	statusFieldID  string            // resolved
	optionIDByName map[string]string // status option name (lower) → ID
}

// New constructs a github tracker. All fields except Timeout are required
// for ListActive to return useful results; Timeout defaults to 30s.
// Projects v2 wiring (ProjectID / StatusFieldID / StatusFieldName) is set
// by the caller on the returned *Client when configured.
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

// SetState moves the issue's Status field on its Projects v2 board to
// `stateName`. SPEC §7.1. Best-effort: returns nil silently when the issue
// is not on any Project v2 (logs at debug level via stderr) — operators
// without a Projects board get no-ops, no errors.
//
// On first call the project + Status field option IDs are auto-discovered
// from the issue's projectItems (unless ProjectID / StatusFieldID are set
// in config) and cached for subsequent calls.
func (c *Client) SetState(ctx context.Context, issueID, stateName string) error {
	if stateName == "" {
		return nil
	}
	repo, num, err := parseGitHubIssueID(issueID)
	if err != nil {
		return err
	}

	// Step 1: resolve issue.node_id (needed for projectItems query).
	issueNodeID, err := c.issueNodeID(ctx, repo, num)
	if err != nil {
		return fmt.Errorf("github tracker: resolve node_id for %s: %w", issueID, err)
	}

	// Step 2: ensure project + field + option cache.
	if err := c.loadProjectAndFields(ctx, issueNodeID); err != nil {
		return err
	}
	c.projectMu.Lock()
	projectID := c.projectID
	fieldID := c.statusFieldID
	optionID, optionOK := c.optionIDByName[strings.ToLower(stateName)]
	c.projectMu.Unlock()
	if projectID == "" {
		// loadProjectAndFields couldn't find any project for this issue;
		// silent no-op already logged inside.
		return nil
	}
	if !optionOK {
		return fmt.Errorf("github tracker: no Status option %q on project %s", stateName, projectID)
	}

	// Step 3: locate this issue's ProjectV2Item.id (must re-query per issue
	// because item IDs are issue-specific).
	itemID, err := c.projectItemID(ctx, issueNodeID, projectID)
	if err != nil {
		return err
	}
	if itemID == "" {
		// Issue not on the cached project — log+skip without erroring.
		fmt.Fprintf(&bytes.Buffer{}, "github tracker: issue %s not on project %s, skip set_state\n", issueID, projectID)
		return nil
	}

	// Step 4: mutation.
	const mut = `mutation($p: ID!, $i: ID!, $f: ID!, $v: String!) {
		updateProjectV2ItemFieldValue(input: {
			projectId: $p,
			itemId: $i,
			fieldId: $f,
			value: { singleSelectOptionId: $v }
		}) { projectV2Item { id } }
	}`
	if err := c.graphql(ctx, mut, map[string]any{
		"p": projectID, "i": itemID, "f": fieldID, "v": optionID,
	}, nil); err != nil {
		return fmt.Errorf("github tracker: updateProjectV2ItemFieldValue: %w", err)
	}
	return nil
}

// issueNodeID resolves an issue's GraphQL node ID from its REST coordinates.
// Uses `gh api repos/{owner}/{repo}/issues/{number} --jq .node_id` which
// returns the bare string ID, no envelope.
func (c *Client) issueNodeID(ctx context.Context, repo string, num int) (string, error) {
	out, err := c.run(ctx, "api", fmt.Sprintf("repos/%s/issues/%d", repo, num), "--jq", ".node_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// loadProjectAndFields populates the project / field / option-id cache on
// first call. When ProjectID and StatusFieldID are both set on the Client,
// we skip the discovery query. Otherwise we read the issue's projectItems
// to find a project, then enumerate the project's fields to find Status.
func (c *Client) loadProjectAndFields(ctx context.Context, issueNodeID string) error {
	c.projectMu.Lock()
	if c.projectID != "" && c.statusFieldID != "" && c.optionIDByName != nil {
		c.projectMu.Unlock()
		return nil
	}
	c.projectMu.Unlock()

	projectID := c.ProjectID
	if projectID == "" {
		// Auto-discover: first project this issue is in.
		const q = `query($id: ID!) {
			node(id: $id) {
				... on Issue {
					projectItems(first: 1) {
						nodes { project { id title } }
					}
				}
			}
		}`
		var resp struct {
			Node struct {
				ProjectItems struct {
					Nodes []struct {
						Project struct {
							ID    string `json:"id"`
							Title string `json:"title"`
						} `json:"project"`
					} `json:"nodes"`
				} `json:"projectItems"`
			} `json:"node"`
		}
		if err := c.graphql(ctx, q, map[string]any{"id": issueNodeID}, &resp); err != nil {
			return fmt.Errorf("github tracker: discover project: %w", err)
		}
		if len(resp.Node.ProjectItems.Nodes) == 0 {
			// Issue isn't on a project; nothing to cache. Subsequent
			// SetState calls will re-attempt discovery for OTHER issues
			// (which may be on a project).
			return nil
		}
		projectID = resp.Node.ProjectItems.Nodes[0].Project.ID
	}

	// Enumerate fields → pick Status (or configured StatusFieldName).
	fieldName := c.StatusFieldName
	if fieldName == "" {
		fieldName = "Status"
	}
	const fq = `query($p: ID!) {
		node(id: $p) {
			... on ProjectV2 {
				fields(first: 50) {
					nodes {
						... on ProjectV2SingleSelectField {
							id name
							options { id name }
						}
					}
				}
			}
		}
	}`
	var fresp struct {
		Node struct {
			Fields struct {
				Nodes []struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					Options []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"options"`
				} `json:"nodes"`
			} `json:"fields"`
		} `json:"node"`
	}
	if err := c.graphql(ctx, fq, map[string]any{"p": projectID}, &fresp); err != nil {
		return fmt.Errorf("github tracker: load project fields: %w", err)
	}
	var statusFieldID string
	options := map[string]string{}
	for _, f := range fresp.Node.Fields.Nodes {
		if f.ID == "" {
			continue // a non-SingleSelect field (Number, Date, …) — fragment yielded zero values
		}
		if c.StatusFieldID != "" && f.ID == c.StatusFieldID {
			statusFieldID = f.ID
		} else if c.StatusFieldID == "" && strings.EqualFold(f.Name, fieldName) {
			statusFieldID = f.ID
		}
		if f.ID == statusFieldID {
			for _, o := range f.Options {
				options[strings.ToLower(o.Name)] = o.ID
			}
		}
	}
	if statusFieldID == "" {
		return fmt.Errorf("github tracker: project %s has no SingleSelect field named %q", projectID, fieldName)
	}

	c.projectMu.Lock()
	c.projectID = projectID
	c.statusFieldID = statusFieldID
	c.optionIDByName = options
	c.projectMu.Unlock()
	return nil
}

// projectItemID locates the ProjectV2Item for `issueNodeID` on `projectID`.
// Returns "" (no error) when the issue isn't on the project — caller
// treats that as a silent no-op.
func (c *Client) projectItemID(ctx context.Context, issueNodeID, projectID string) (string, error) {
	const q = `query($id: ID!) {
		node(id: $id) {
			... on Issue {
				projectItems(first: 10) {
					nodes { id project { id } }
				}
			}
		}
	}`
	var resp struct {
		Node struct {
			ProjectItems struct {
				Nodes []struct {
					ID      string `json:"id"`
					Project struct {
						ID string `json:"id"`
					} `json:"project"`
				} `json:"nodes"`
			} `json:"projectItems"`
		} `json:"node"`
	}
	if err := c.graphql(ctx, q, map[string]any{"id": issueNodeID}, &resp); err != nil {
		return "", fmt.Errorf("github tracker: locate project item: %w", err)
	}
	for _, n := range resp.Node.ProjectItems.Nodes {
		if n.Project.ID == projectID {
			return n.ID, nil
		}
	}
	return "", nil
}

// graphql wraps `gh api graphql -f query=... -F var=...` for typed responses.
// Variables are passed as repeated `-F key=value` flags (gh handles JSON
// scalars correctly; for object inputs we serialize them as JSON strings
// that the GraphQL server parses — but SetState uses only scalar inputs).
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	args := []string{"api", "graphql", "-f", "query=" + query}
	for k, v := range vars {
		args = append(args, "-F", fmt.Sprintf("%s=%v", k, v))
	}
	stdout, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		return fmt.Errorf("decode graphql envelope: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("graphql errors: %s", strings.Join(msgs, "; "))
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode graphql data: %w", err)
		}
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
