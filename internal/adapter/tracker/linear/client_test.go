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
		TeamKey:      "NSI",
		ActiveStates: []string{"Todo", "In Progress"},
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
// `relations` vs `inverseRelations` bug class.
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

	if !strings.Contains(fake.lastBody, "inverseRelations") {
		t.Errorf("query missing inverseRelations field; body:\n%s", fake.lastBody)
	}
	if !strings.Contains(fake.lastBody, "assignee") {
		t.Errorf("query must request assignee for picker filter; body:\n%s", fake.lastBody)
	}
	if strings.Contains(fake.lastBody, "relations { nodes { type relatedIssue") {
		t.Errorf("query uses wrong-direction `relations { relatedIssue }`")
	}
	for _, needle := range []string{"team:", "state:", `"team":"NSI"`, `"Todo"`, `"In Progress"`} {
		if !strings.Contains(fake.lastBody, needle) {
			t.Errorf("query/vars missing %q; body:\n%s", needle, fake.lastBody)
		}
	}
}

func TestListActive_BlockerAndAssignee(t *testing.T) {
	resp := `{"data":{"issues":{"nodes":[
		{
			"id":"i1","identifier":"NSI-1","title":"a","description":"",
			"url":"","priority":2,
			"state":{"name":"Todo","type":"unstarted"},
			"assignee":{"id":"user_cyrus"},
			"labels":{"nodes":[{"name":"Complexity:Routine"}]},
			"inverseRelations":{"nodes":[
				{"type":"blocks","issue":{"id":"blocker1","state":{"type":"started"}}},
				{"type":"blocks","issue":{"id":"blocker2","state":{"type":"completed"}}},
				{"type":"duplicate","issue":{"id":"dup","state":{"type":"started"}}}
			]},
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"
		},
		{
			"id":"i2","identifier":"NSI-2","title":"b","description":"",
			"url":"","priority":0,
			"state":{"name":"Todo","type":"unstarted"},
			"assignee":null,
			"labels":{"nodes":[]},
			"inverseRelations":{"nodes":[]},
			"createdAt":"2026-01-02T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z"
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
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}
	if issues[0].AssigneeID != "user_cyrus" {
		t.Errorf("issue0 AssigneeID = %q; want user_cyrus", issues[0].AssigneeID)
	}
	if issues[1].AssigneeID != "" {
		t.Errorf("issue1 AssigneeID = %q; want empty (unassigned)", issues[1].AssigneeID)
	}
	if len(issues[0].BlockedBy) != 1 || issues[0].BlockedBy[0] != "blocker1" {
		t.Errorf("issue0 BlockedBy = %v; want [blocker1]", issues[0].BlockedBy)
	}
	if len(issues[0].Labels) != 1 || issues[0].Labels[0] != "complexity:routine" {
		t.Errorf("issue0 Labels = %v; want [complexity:routine]", issues[0].Labels)
	}
}

func TestAssign(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"issueUpdate": `{"data":{"issueUpdate":{"success":true}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	if err := c.Assign(context.Background(), "issue1", "user_cyrus"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	for _, needle := range []string{"issueUpdate", `"id":"issue1"`, `"assignee":"user_cyrus"`, "assigneeId"} {
		if !strings.Contains(fake.lastBody, needle) {
			t.Errorf("Assign body missing %q; got:\n%s", needle, fake.lastBody)
		}
	}
}

func TestAssign_FailureSurfaced(t *testing.T) {
	fake := &fakeLinear{
		t: t,
		responsesFor: map[string]string{
			"issueUpdate": `{"data":{"issueUpdate":{"success":false}}}`,
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newClient(t, srv)
	err := c.Assign(context.Background(), "issue1", "user_cyrus")
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

func TestUnmarshalNode(t *testing.T) {
	raw := `{
		"id":"x","identifier":"NSI-2","title":"t","description":"d",
		"url":"u","priority":2,
		"state":{"name":"In Progress","type":"started"},
		"assignee":{"id":"u1"},
		"labels":{"nodes":[{"name":"a"},{"name":"B"}]},
		"inverseRelations":{"nodes":[]},
		"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z"
	}`
	var n gqlIssueNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n.Priority != 2 || n.State.Type != "started" || n.Identifier != "NSI-2" {
		t.Errorf("decoded fields wrong: %+v", n)
	}
	if n.Assignee == nil || n.Assignee.ID != "u1" {
		t.Errorf("Assignee = %+v; want id u1", n.Assignee)
	}
}
