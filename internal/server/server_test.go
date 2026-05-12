package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/orchestrator"
)

// fakeTracker satisfies tracker.Tracker for tests.
type fakeTracker struct{ kind string }

func (f *fakeTracker) Kind() string                                              { return f.kind }
func (f *fakeTracker) EnforceAuth(ctx context.Context) error                     { return nil }
func (f *fakeTracker) ListActive(ctx context.Context) ([]tracker.Issue, error)   { return nil, nil }
func (f *fakeTracker) GetState(ctx context.Context, id string) (string, error)   { return "open", nil }
func (f *fakeTracker) Comment(ctx context.Context, id, body string) error        { return nil }
func (f *fakeTracker) SetState(ctx context.Context, id, stateName string) error  { return nil }

func newServerForTest(t *testing.T) (*Server, *orchestrator.State, chan struct{}) {
	t.Helper()
	state := orchestrator.NewState()
	refresh := make(chan struct{}, 1)
	s := &Server{
		Cfg:     &config.Config{Tracker: config.Tracker{Kind: "github"}, Server: config.Server{Port: 0}},
		Tracker: &fakeTracker{kind: "github"},
		State:   state,
		Refresh: refresh,
	}
	return s, state, refresh
}

func TestHandleState_Shape(t *testing.T) {
	s, state, _ := newServerForTest(t)
	state.TryClaim("X")
	state.MarkRunning("X", &orchestrator.Worker{
		Issue:   tracker.Issue{ID: "X", Identifier: "owner/repo#1"},
		Session: orchestrator.Session{ID: "github:X:1:abcd"},
		Profile: &config.Profile{Name: "claude"},
	})

	req := httptest.NewRequest("GET", "/api/v1/state", nil)
	w := httptest.NewRecorder()
	s.handleState(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var got Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, w.Body.String())
	}
	if got.Tracker.Kind != "github" {
		t.Errorf("tracker.kind = %q", got.Tracker.Kind)
	}
	if got.Counts.Running != 1 {
		t.Errorf("counts.running = %d", got.Counts.Running)
	}
	if len(got.Running) != 1 || got.Running[0].IssueIdentifier != "owner/repo#1" {
		t.Errorf("running entry wrong: %+v", got.Running)
	}
	if got.Running[0].SessionID != "github:X:1:abcd" {
		t.Errorf("session_id missing: %+v", got.Running[0])
	}
}

func TestHandleRefresh_CoalescesAndQueues(t *testing.T) {
	s, _, refresh := newServerForTest(t)

	// First request: queues.
	req := httptest.NewRequest("POST", "/api/v1/refresh", nil)
	w := httptest.NewRecorder()
	s.handleRefresh(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("first refresh status = %d", w.Code)
	}
	var resp1 map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp1)
	if got, _ := resp1["coalesced"].(bool); got {
		t.Errorf("first refresh should NOT be coalesced; got %+v", resp1)
	}

	// Without draining, second request: should coalesce.
	req2 := httptest.NewRequest("POST", "/api/v1/refresh", nil)
	w2 := httptest.NewRecorder()
	s.handleRefresh(w2, req2)
	var resp2 map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if got, _ := resp2["coalesced"].(bool); !got {
		t.Errorf("second refresh should be coalesced; got %+v", resp2)
	}

	// Drain the channel, then a third refresh should queue again.
	<-refresh
	req3 := httptest.NewRequest("POST", "/api/v1/refresh", nil)
	w3 := httptest.NewRecorder()
	s.handleRefresh(w3, req3)
	var resp3 map[string]any
	_ = json.Unmarshal(w3.Body.Bytes(), &resp3)
	if got, _ := resp3["coalesced"].(bool); got {
		t.Errorf("third refresh after drain should NOT be coalesced; got %+v", resp3)
	}
}

func TestHandleIssue_NotFound(t *testing.T) {
	s, _, _ := newServerForTest(t)
	req := httptest.NewRequest("GET", "/api/v1/owner%2Frepo%23999", nil)
	w := httptest.NewRecorder()
	s.handleIssue(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleIssue_RetryHit(t *testing.T) {
	s, state, _ := newServerForTest(t)
	state.TryClaim("R")
	state.MarkRunning("R", &orchestrator.Worker{
		Issue:   tracker.Issue{ID: "R", Identifier: "owner/repo#2"},
		Profile: &config.Profile{Name: "claude"},
	})
	state.FailAndQueueRetry("R", 2, time.Now().Add(30*time.Second), "boom",
		tracker.Issue{ID: "R", Identifier: "owner/repo#2"})

	req := httptest.NewRequest("GET", "/api/v1/owner%2Frepo%232", nil)
	w := httptest.NewRecorder()
	s.handleIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "retrying" {
		t.Errorf("status = %v, want retrying", resp["status"])
	}
	attempts, _ := resp["attempts"].(map[string]any)
	if attempts["current_retry_attempt"].(float64) != 2 {
		t.Errorf("current_retry_attempt = %v", attempts["current_retry_attempt"])
	}
	if resp["last_error"] != "boom" {
		t.Errorf("last_error = %v", resp["last_error"])
	}
	retry, _ := resp["retry"].(map[string]any)
	if retry["attempt"].(float64) != 2 {
		t.Errorf("retry.attempt = %v", retry["attempt"])
	}
}

func TestHandleHealthz(t *testing.T) {
	s, _, _ := newServerForTest(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.handleHealthz(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Errorf("/healthz: status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestHandleIssue_RunningHit(t *testing.T) {
	s, state, _ := newServerForTest(t)
	state.TryClaim("A")
	state.MarkRunning("A", &orchestrator.Worker{
		Issue:   tracker.Issue{ID: "A", Identifier: "owner/repo#1"},
		Session: orchestrator.Session{ID: "github:A:1:zzzz"},
		Profile: &config.Profile{Name: "claude"},
	})
	// Identifier is URL-encoded (the # becomes %23).
	req := httptest.NewRequest("GET", "/api/v1/owner%2Frepo%231", nil)
	w := httptest.NewRecorder()
	s.handleIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "running" {
		t.Errorf("status field = %v", resp["status"])
	}
	running, _ := resp["running"].(map[string]any)
	if running["session_id"] != "github:A:1:zzzz" {
		t.Errorf("session_id = %v", running["session_id"])
	}
}

func TestHandleDashboard_RendersHTML(t *testing.T) {
	s, state, _ := newServerForTest(t)
	state.TryClaim("X")
	state.MarkRunning("X", &orchestrator.Worker{
		Issue:   tracker.Issue{ID: "X", Identifier: "owner/repo#1"},
		Session: orchestrator.Session{ID: "github:X:1:abcd"},
		Profile: &config.Profile{Name: "claude"},
	})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s.handleDashboard(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"<title>gleaner", "tracker=github", "owner/repo#1", "github:X:1:abcd"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestHandleMethodNotAllowed(t *testing.T) {
	s, _, _ := newServerForTest(t)
	cases := []struct {
		path string
		fn   http.HandlerFunc
	}{
		{"/api/v1/state", s.handleState},       // GET only
		{"/api/v1/refresh", s.handleRefresh},   // POST only
		{"/", s.handleDashboard},               // GET only
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			req := httptest.NewRequest("DELETE", c.path, nil)
			w := httptest.NewRecorder()
			c.fn(w, req)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("path=%s DELETE got status %d, want 405", c.path, w.Code)
			}
		})
	}
}
