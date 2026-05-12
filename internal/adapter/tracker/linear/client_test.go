// Package linear tests use net/http/httptest to record the GraphQL
// queries our client emits and serve canned responses. The goal is to
// catch query-shape regressions — e.g. `relations` vs `inverseRelations`,
// missing fields, wrong auth header style — without touching the live
// Linear API.
package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLinear records the last request body and dispatches a response
// from `responses` keyed by a substring of the query.
type fakeLinear struct {
	t            *testing.T
	lastBody     string
	lastAuth     string
	responsesFor map[string]string
}

func (f *fakeLinear) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			f.t.Errorf("unexpected method %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		f.lastBody = string(body)
		f.lastAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		for needle, resp := range f.responsesFor {
			if strings.Contains(f.lastBody, needle) {
				_, _ = w.Write([]byte(resp))
				return
			}
		}
		f.t.Errorf("no canned response matched body: %s", f.lastBody)
		_, _ = w.Write([]byte(`{"data":null}`))
	})
}

func newClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		Endpoint:     srv.URL,
		APIKey:       "lin_api_testkey",
		TeamKey:      "MT",
		ActiveStates: []string{"Todo", "In Progress"},
		CodehostRepo: "nSimonFR/test-repo",
		httpClient:   srv.Client(),
	}
}

func TestEnforceAuth(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"viewer": `{"data":{"viewer":{"id":"u_123","email":"x@y.z"}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	if err := c.EnforceAuth(context.Background()); err != nil {
		t.Fatalf("EnforceAuth: %v", err)
	}
	// Auth header must be the raw key, no "Bearer" prefix (Linear convention).
	if fake.lastAuth != "lin_api_testkey" {
		t.Errorf("Authorization = %q; want raw key (no Bearer prefix)", fake.lastAuth)
	}
}

func TestEnforceAuth_BadKey(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"viewer": `{"errors":[{"message":"Authentication failed"}]}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	err := c.EnforceAuth(context.Background())
	if err == nil {
		t.Fatal("expected error on bad key")
	}
	if !strings.Contains(err.Error(), "Authentication failed") {
		t.Errorf("error did not surface server message: %v", err)
	}
}

// TestListActive_QueryShape is the regression guard for the
// `relations` vs `inverseRelations` bug class. It asserts the GraphQL
// query our client emits contains the exact correct field names —
// `inverseRelations` with `issue` (not `relations` with `relatedIssue`).
func TestListActive_QueryShape(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"issues(": `{"data":{"issues":{"nodes":[]}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	if _, err := c.ListActive(context.Background()); err != nil {
		t.Fatalf("ListActive: %v", err)
	}

	// The query must use inverseRelations { issue } to fetch blockers.
	// `relations { relatedIssue }` would return the opposite direction
	// (issues this one blocks, not blockers).
	if !strings.Contains(fake.lastBody, "inverseRelations") {
		t.Errorf("query missing inverseRelations field; body:\n%s", fake.lastBody)
	}
	if strings.Contains(fake.lastBody, "relations { nodes { type relatedIssue") {
		t.Errorf("query uses wrong-direction `relations { relatedIssue }`; should be `inverseRelations { issue }`")
	}
	// Sanity: the filter shape must include team key + state name.
	for _, needle := range []string{"team:", "state:", `"team":"MT"`, `"Todo"`, `"In Progress"`} {
		if !strings.Contains(fake.lastBody, needle) {
			t.Errorf("query/vars missing %q; body:\n%s", needle, fake.lastBody)
		}
	}
}

// TestListActive_BlockerSemantics verifies that an issue is marked
// BlockedBy when an inverseRelations entry of type "blocks" points to a
// related issue whose state.type is neither "completed" nor "canceled".
func TestListActive_BlockerSemantics(t *testing.T) {
	resp := `{"data":{"issues":{"nodes":[
		{
			"id":"i1","identifier":"MT-1","title":"a","description":"",
			"branchName":"","url":"","priority":0,
			"state":{"name":"Todo","type":"unstarted"},
			"labels":{"nodes":[{"name":"Complexity:Routine"}]},
			"inverseRelations":{"nodes":[
				{"type":"blocks","issue":{"id":"blocker1","state":{"type":"started"}}},
				{"type":"blocks","issue":{"id":"blocker2","state":{"type":"completed"}}},
				{"type":"duplicate","issue":{"id":"dup","state":{"type":"started"}}}
			]},
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"
		}
	]}}}`
	fake := &fakeLinear{t: t, responsesFor: map[string]string{"issues(": resp}}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	issues, err := c.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}
	iss := issues[0]
	if iss.ID != "i1" || iss.Identifier != "MT-1" {
		t.Errorf("issue id/identifier = %q/%q", iss.ID, iss.Identifier)
	}
	// Only the active "blocks" relation should count. The completed
	// blocker is filtered; the "duplicate" relation is the wrong type.
	if len(iss.BlockedBy) != 1 || iss.BlockedBy[0] != "blocker1" {
		t.Errorf("BlockedBy = %v; want [blocker1]", iss.BlockedBy)
	}
	// Labels should be lower-cased per gleaner convention.
	if len(iss.Labels) != 1 || iss.Labels[0] != "complexity:routine" {
		t.Errorf("Labels = %v; want [complexity:routine]", iss.Labels)
	}
	// CodehostRepo should be backfilled from client config.
	if iss.Repo != "nSimonFR/test-repo" {
		t.Errorf("Repo = %q; want nSimonFR/test-repo", iss.Repo)
	}
}

func TestGetState(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"issue(id:":   `{"data":{"issue":{"state":{"name":"Done"}}}}`,
			`"id":"abc"`:  `{"data":{"issue":{"state":{"name":"Done"}}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	state, err := c.GetState(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if state != "Done" {
		t.Errorf("state = %q; want Done", state)
	}
}

func TestComment(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"commentCreate": `{"data":{"commentCreate":{"success":true,"comment":{"id":"c1"}}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	if err := c.Comment(context.Background(), "issue1", "PR opened: https://x"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	// Verify the mutation carries both fields and the right name.
	for _, needle := range []string{"commentCreate", `"id":"issue1"`, "PR opened"} {
		if !strings.Contains(fake.lastBody, needle) {
			t.Errorf("Comment body missing %q; got:\n%s", needle, fake.lastBody)
		}
	}
}

func TestComment_FailureSurfaced(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"commentCreate": `{"data":{"commentCreate":{"success":false}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	err := c.Comment(context.Background(), "issue1", "body")
	if err == nil {
		t.Fatal("expected error when success=false")
	}
	if !strings.Contains(err.Error(), "success=false") {
		t.Errorf("error should surface success=false; got: %v", err)
	}
}

func TestQuery_GraphQLErrorsSurfaced(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"viewer": `{"errors":[{"message":"rate limited"}]}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	err := c.EnforceAuth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("expected graphql error surfaced; got: %v", err)
	}
}

// TestSetState_HappyPath drives a Todo→In Progress transition end to
// end: loadStates queries the team workflow once, SetState issues the
// issueUpdate mutation with the resolved stateID, and the cache prevents
// a second team query.
func TestSetState_HappyPath(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			// loadStates query — match by the unique "teams(filter:" snippet
			// since GraphQL pretty-prints variants of "team" in many places.
			"teams(filter:": `{"data":{"teams":{"nodes":[{"states":{"nodes":[
				{"id":"st-todo","name":"Todo"},
				{"id":"st-prog","name":"In Progress"},
				{"id":"st-review","name":"In Review"}
			]}}]}}}`,
			"issueUpdate": `{"data":{"issueUpdate":{"success":true}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	if err := c.SetState(context.Background(), "iss-abc", "In Progress"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	// Last body should be the issueUpdate mutation carrying st-prog.
	if !strings.Contains(fake.lastBody, "issueUpdate") || !strings.Contains(fake.lastBody, "st-prog") {
		t.Errorf("expected issueUpdate w/ stateId=st-prog; got: %s", fake.lastBody)
	}

	// Case-insensitive lookup: passing the lower-case name should still
	// resolve to st-prog without re-querying loadStates.
	fake.lastBody = ""
	if err := c.SetState(context.Background(), "iss-xyz", "in progress"); err != nil {
		t.Fatalf("SetState lowercase: %v", err)
	}
	if strings.Contains(fake.lastBody, "teams(filter:") {
		t.Errorf("loadStates re-queried on second call; cache not honored")
	}
	if !strings.Contains(fake.lastBody, "st-prog") {
		t.Errorf("case-insensitive name didn't resolve; got: %s", fake.lastBody)
	}
}

// TestSetState_UnknownState: a state name that isn't in the team's workflow
// must produce a clear error (no mutation is issued).
func TestSetState_UnknownState(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"teams(filter:": `{"data":{"teams":{"nodes":[{"states":{"nodes":[
				{"id":"st-todo","name":"Todo"}
			]}}]}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	err := c.SetState(context.Background(), "iss-abc", "Done")
	if err == nil {
		t.Fatal("expected error for unknown state name")
	}
	if !strings.Contains(err.Error(), "Done") || !strings.Contains(err.Error(), "workflow state") {
		t.Errorf("error should name the missing state; got: %v", err)
	}
}

// TestSetState_EmptyName: empty state name is a no-op (operator disabled
// the transition via tracker.in_progress_state: ""). Must NOT hit Linear.
func TestSetState_EmptyName(t *testing.T) {
	fake := &fakeLinear{t: t, responsesFor: map[string]string{}}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	if err := c.SetState(context.Background(), "iss-abc", ""); err != nil {
		t.Errorf("empty state name should be silent no-op; got: %v", err)
	}
	if fake.lastBody != "" {
		t.Errorf("empty state name issued a request: %s", fake.lastBody)
	}
}

// TestSetState_MutationSuccessFalse: surfacing a non-success response.
func TestSetState_MutationSuccessFalse(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"teams(filter:": `{"data":{"teams":{"nodes":[{"states":{"nodes":[
				{"id":"st-prog","name":"In Progress"}
			]}}]}}}`,
			"issueUpdate": `{"data":{"issueUpdate":{"success":false}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	err := c.SetState(context.Background(), "iss-abc", "In Progress")
	if err == nil || !strings.Contains(err.Error(), "success=false") {
		t.Errorf("expected success=false error; got: %v", err)
	}
}

// JSON-decode check: ensure our gqlIssueNode struct can round-trip
// the canned response shape without nil-deref or silent field drops.
func TestUnmarshalNode(t *testing.T) {
	raw := `{
		"id":"x","identifier":"MT-2","title":"t","description":"d",
		"branchName":"b","url":"u","priority":2,
		"state":{"name":"In Progress","type":"started"},
		"labels":{"nodes":[{"name":"a"},{"name":"B"}]},
		"inverseRelations":{"nodes":[]},
		"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z"
	}`
	var n gqlIssueNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n.Priority != 2 || n.State.Type != "started" || n.Identifier != "MT-2" {
		t.Errorf("decoded fields wrong: %+v", n)
	}
}
