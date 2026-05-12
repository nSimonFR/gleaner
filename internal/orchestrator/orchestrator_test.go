package orchestrator

import (
	"testing"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
)

func TestProviderFromPlan(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"claude/team", "claude"},
		{"codex/plus", "codex"},
		{"claude/max", "claude"},
		{"pi", "pi"},
		{"", ""},
		// "/team" is malformed (no provider prefix). The current
		// function returns it unchanged; callers should validate plan
		// format upstream so this edge case never reaches dispatch.
		{"/team", "/team"},
	}
	for _, c := range cases {
		if got := providerFromPlan(c.in); got != c.want {
			t.Errorf("providerFromPlan(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePriority(t *testing.T) {
	// Linear: 0=none (lowest), 1=urgent (highest), 2=high, 3=medium, 4=low.
	// SortBySpec sorts ascending, so urgent (1) should come BEFORE
	// "no priority" (0 → 999) — which is exactly what normalizePriority does.
	if normalizePriority(0) != 999 {
		t.Errorf("0 should normalize to 999 (sort-last sentinel), got %d", normalizePriority(0))
	}
	for _, p := range []int{1, 2, 3, 4} {
		if normalizePriority(p) != p {
			t.Errorf("normalizePriority(%d) should be %d", p, p)
		}
	}
}

// TestSortBySpec asserts SPEC §8.1 step 4: priority asc, then
// created_at oldest first, then identifier lex.
func TestSortBySpec(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	issues := []tracker.Issue{
		{Identifier: "B", Priority: 0, CreatedAt: t1},  // priority=0 should sort LAST
		{Identifier: "A", Priority: 2, CreatedAt: t3},  // high, newer
		{Identifier: "C", Priority: 1, CreatedAt: t2},  // urgent
		{Identifier: "D", Priority: 1, CreatedAt: t1},  // urgent, older — should be first
		{Identifier: "E", Priority: 1, CreatedAt: t1},  // urgent, older, lex after D
	}
	sortBySpec(issues)

	wantOrder := []string{"D", "E", "C", "A", "B"}
	for i, w := range wantOrder {
		if issues[i].Identifier != w {
			t.Errorf("position %d: got %q, want %q (full order: %v)",
				i, issues[i].Identifier, w, identList(issues))
		}
	}
}

func identList(issues []tracker.Issue) []string {
	out := make([]string, len(issues))
	for i, iss := range issues {
		out[i] = iss.Identifier
	}
	return out
}
