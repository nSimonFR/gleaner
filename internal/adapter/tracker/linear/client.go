// Package linear implements the tracker.Tracker interface against Linear
// via GraphQL. The orchestrator filters issues by configured active_states
// (default: "Todo", "In Progress") and the configured team key (e.g. "MT").
//
// Auth: Linear API key (`lin_api_...`) loaded from a file path. The
// `Authorization` header carries the key directly — no "Bearer" prefix
// per Linear's docs.
//
// Code lives on GitHub regardless of tracker: the github codehost still
// opens PRs. Comment() writes back to the Linear issue ("PR opened: <url>")
// so the operator sees the result on the Linear board.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
)

const defaultEndpoint = "https://api.linear.app/graphql"

// Client is the linear-tracker implementation. Construct with New().
type Client struct {
	Endpoint     string        // override for tests; defaults to api.linear.app
	APIKey       string        // raw key (use APIKeyFile in production)
	APIKeyFile   string        // path read once at EnforceAuth and cached
	TeamKey      string        // e.g. "MT" (prefix of identifiers like MT-649)
	ActiveStates []string      // Linear state names, e.g. ["Todo", "In Progress"]
	CodehostRepo string        // owner/repo — fills Issue.Repo (Linear has no native repo concept)
	Timeout      time.Duration // per HTTP request; defaults to 15s
	httpClient   *http.Client
}

// New constructs a linear tracker. APIKey and APIKeyFile are mutually
// exclusive; provide one. TeamKey is required. ActiveStates defaults
// to ["Todo", "In Progress"] when empty (matches Symphony SPEC §5.3).
func New(apiKeyFile, teamKey, codehostRepo string, activeStates []string) *Client {
	if len(activeStates) == 0 {
		activeStates = []string{"Todo", "In Progress"}
	}
	return &Client{
		Endpoint:     defaultEndpoint,
		APIKeyFile:   apiKeyFile,
		TeamKey:      teamKey,
		ActiveStates: activeStates,
		CodehostRepo: codehostRepo,
		Timeout:      15 * time.Second,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Kind returns "linear". Used for session_id prefix (SPEC §13.1).
func (c *Client) Kind() string { return "linear" }

// EnforceAuth loads the API key (if APIKeyFile is set) and issues a
// minimal GraphQL ping (`viewer { id }`) to validate it. Run once at
// startup.
func (c *Client) EnforceAuth(ctx context.Context) error {
	if err := c.loadKey(); err != nil {
		return err
	}
	const q = `query { viewer { id email } }`
	var resp struct {
		Viewer struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"viewer"`
	}
	if err := c.query(ctx, q, nil, &resp); err != nil {
		return fmt.Errorf("linear tracker: viewer ping failed: %w", err)
	}
	if resp.Viewer.ID == "" {
		return fmt.Errorf("linear tracker: viewer ping returned no id (bad key?)")
	}
	return nil
}

func (c *Client) loadKey() error {
	if c.APIKey != "" {
		return nil
	}
	if c.APIKeyFile == "" {
		return fmt.Errorf("linear tracker: APIKeyFile is required (or set APIKey directly)")
	}
	raw, err := os.ReadFile(c.APIKeyFile)
	if err != nil {
		return fmt.Errorf("linear tracker: read api key file %s: %w", c.APIKeyFile, err)
	}
	c.APIKey = strings.TrimSpace(string(raw))
	if c.APIKey == "" {
		return fmt.Errorf("linear tracker: api key file %s is empty", c.APIKeyFile)
	}
	return nil
}

// gqlIssueNode mirrors the fields requested in the ListActive query.
type gqlIssueNode struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	BranchName  string `json:"branchName"`
	URL         string `json:"url"`
	Priority    int    `json:"priority"`
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	// inverseRelations on issue X = "relations where X is the target".
	// For relation type "blocks", that means "issues that block X" —
	// the actual blockers. Querying `relations` would return the
	// inverse (issues X blocks), which is the wrong direction for
	// SPEC §8.1 step 5 (Todo issues with non-terminal blockers are
	// ineligible). Matches Symphony Elixir's linear/client.ex shape.
	InverseRelations struct {
		Nodes []struct {
			Type  string `json:"type"`
			Issue struct {
				ID    string `json:"id"`
				State struct {
					Type string `json:"type"`
				} `json:"state"`
			} `json:"issue"`
		} `json:"nodes"`
	} `json:"inverseRelations"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ListActive returns issues whose state name is in c.ActiveStates and whose
// team key matches c.TeamKey. SPEC §8.1 step 3.
//
// Order: most-recently-updated-first (Linear default). Orchestrator re-sorts.
func (c *Client) ListActive(ctx context.Context) ([]tracker.Issue, error) {
	if err := c.loadKey(); err != nil {
		return nil, err
	}
	if c.TeamKey == "" {
		return nil, fmt.Errorf("linear tracker: TeamKey is required (e.g. \"MT\")")
	}
	const q = `query($team: String!, $states: [String!]) {
		issues(
			first: 100,
			filter: {
				team: { key: { eq: $team } },
				state: { name: { in: $states } }
			}
		) {
			nodes {
				id identifier title description branchName url priority
				state { name type }
				labels { nodes { name } }
				inverseRelations { nodes { type issue { id state { type } } } }
				createdAt updatedAt
			}
		}
	}`
	vars := map[string]any{"team": c.TeamKey, "states": c.ActiveStates}
	var resp struct {
		Issues struct {
			Nodes []gqlIssueNode `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.query(ctx, q, vars, &resp); err != nil {
		return nil, err
	}
	out := make([]tracker.Issue, 0, len(resp.Issues.Nodes))
	for _, n := range resp.Issues.Nodes {
		// Labels lower-cased to match gleaner's profile-matching convention.
		// GitHub labels are conventionally lowercase; Linear lets operators
		// store mixed case. cfg.MatchProfile is case-insensitive, but
		// lowercasing here gives a single canonical form in logs and the
		// /status JSON (Milestone E). Matches Symphony Elixir's
		// linear/client.ex:extract_labels.
		labels := make([]string, 0, len(n.Labels.Nodes))
		for _, l := range n.Labels.Nodes {
			labels = append(labels, strings.ToLower(l.Name))
		}
		// SPEC §8.1 step 5: Todo issues with non-terminal blockers are
		// ineligible. inverseRelations on this issue = "relations where
		// this issue is the target" — for type="blocks", the source is
		// the actual blocker.
		var blockedBy []string
		for _, r := range n.InverseRelations.Nodes {
			if r.Type == "blocks" && r.Issue.State.Type != "completed" && r.Issue.State.Type != "canceled" {
				blockedBy = append(blockedBy, r.Issue.ID)
			}
		}
		out = append(out, tracker.Issue{
			ID:         n.ID,
			Identifier: n.Identifier,
			Repo:       c.CodehostRepo,
			Title:      n.Title,
			Body:       n.Description,
			Labels:     labels,
			State:      n.State.Name,
			Priority:   n.Priority,
			BranchName: n.BranchName,
			URL:        n.URL,
			BlockedBy:  blockedBy,
			CreatedAt:  n.CreatedAt,
			UpdatedAt:  n.UpdatedAt,
		})
	}
	return out, nil
}

// GetState fetches the current state name for a single issue. SPEC §8.1 Part B.
func (c *Client) GetState(ctx context.Context, issueID string) (string, error) {
	if err := c.loadKey(); err != nil {
		return "", err
	}
	const q = `query($id: String!) { issue(id: $id) { state { name } } }`
	var resp struct {
		Issue struct {
			State struct {
				Name string `json:"name"`
			} `json:"state"`
		} `json:"issue"`
	}
	if err := c.query(ctx, q, map[string]any{"id": issueID}, &resp); err != nil {
		return "", err
	}
	return resp.Issue.State.Name, nil
}

// Comment writes a comment to the Linear issue. The orchestrator uses this
// to post the resulting GitHub PR URL back so the Linear board reflects it.
func (c *Client) Comment(ctx context.Context, issueID, body string) error {
	if err := c.loadKey(); err != nil {
		return err
	}
	const q = `mutation($id: String!, $body: String!) {
		commentCreate(input: { issueId: $id, body: $body }) {
			success
			comment { id }
		}
	}`
	var resp struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	if err := c.query(ctx, q, map[string]any{"id": issueID, "body": body}, &resp); err != nil {
		return err
	}
	if !resp.CommentCreate.Success {
		return fmt.Errorf("linear tracker: commentCreate returned success=false for issue %s", issueID)
	}
	return nil
}

// query is the single GraphQL transport. All public methods route through it.
func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: c.Timeout}
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{Query: q, Variables: vars})
	if err != nil {
		return fmt.Errorf("linear tracker: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("linear tracker: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("linear tracker: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("linear tracker: http %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("linear tracker: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("linear tracker: graphql errors: %s", strings.Join(msgs, "; "))
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("linear tracker: decode data: %w", err)
		}
	}
	return nil
}

// Compile-time check that *Client implements tracker.Tracker.
var _ tracker.Tracker = (*Client)(nil)
