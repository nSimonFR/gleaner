package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
)

// fakeTracker just supplies Kind() for session ID composition.
type fakeTracker struct{ kind string }

func (f *fakeTracker) Kind() string                                                        { return f.kind }
func (f *fakeTracker) EnforceAuth(ctx context.Context) error                               { return nil }
func (f *fakeTracker) ListActive(ctx context.Context) ([]tracker.Issue, error)             { return nil, nil }
func (f *fakeTracker) GetState(ctx context.Context, issueID string) (string, error)        { return "", nil }
func (f *fakeTracker) Comment(ctx context.Context, issueID, body string) error             { return nil }

// TestNewSession asserts the shape: <kind>:<id>:<attempt>:<random4>.
func TestNewSession_Shape(t *testing.T) {
	trk := &fakeTracker{kind: "github"}
	iss := tracker.Issue{ID: "nSimonFR/test#42"}
	s := NewSession(trk, iss, 1)

	parts := strings.SplitN(s.ID, ":", 4)
	if len(parts) != 4 {
		t.Fatalf("ID = %q; want 4 colon-separated parts (got %d)", s.ID, len(parts))
	}
	if parts[0] != "github" {
		t.Errorf("kind part = %q, want github", parts[0])
	}
	if parts[1] != "nSimonFR/test#42" {
		t.Errorf("id part = %q", parts[1])
	}
	if parts[2] != "1" {
		t.Errorf("attempt part = %q, want 1", parts[2])
	}
	if len(parts[3]) != 8 {
		t.Errorf("random part = %q (len %d), want 8 hex chars", parts[3], len(parts[3]))
	}
}

// TestNewSession_LinearKind verifies the kind prefix tracks the
// adapter — Linear sessions look like "linear:abc...".
func TestNewSession_LinearKind(t *testing.T) {
	trk := &fakeTracker{kind: "linear"}
	s := NewSession(trk, tracker.Issue{ID: "abc-def-123"}, 2)
	if !strings.HasPrefix(s.ID, "linear:abc-def-123:2:") {
		t.Errorf("Linear session = %q; want linear:abc-def-123:2:<random>", s.ID)
	}
}

// TestNewSession_Unique: two sessions for the same issue/attempt
// shouldn't collide (random suffix differs).
func TestNewSession_Unique(t *testing.T) {
	trk := &fakeTracker{kind: "github"}
	iss := tracker.Issue{ID: "X"}
	a := NewSession(trk, iss, 1)
	b := NewSession(trk, iss, 1)
	if a.ID == b.ID {
		t.Errorf("two sessions collided: %q vs %q", a.ID, b.ID)
	}
}
