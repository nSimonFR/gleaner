package tracker

import "testing"

func TestIsActive(t *testing.T) {
	states := []string{"Todo", "In Progress"}
	if !IsActive("Todo", states) {
		t.Error("Todo should be active")
	}
	if !IsActive("In Progress", states) {
		t.Error("In Progress should be active")
	}
	if IsActive("Done", states) {
		t.Error("Done should not be active")
	}
	if IsActive("Todo", nil) {
		t.Error("empty state list should not match")
	}
}

func TestIsTerminal(t *testing.T) {
	states := []string{"Done", "Cancelled", "Duplicate"}
	if !IsTerminal("Done", states) {
		t.Error("Done should be terminal")
	}
	if IsTerminal("Todo", states) {
		t.Error("Todo should not be terminal")
	}
	// Case sensitivity is intentional — SPEC names are exact matches.
	if IsTerminal("done", states) {
		t.Error("case-insensitive match should NOT trigger")
	}
}
