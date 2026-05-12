package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTmp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write tmp config: %v", err)
	}
	return p
}

// TestLoad_LegacyTopLevel verifies the pre-Milestone-A config shape
// (top-level account/repos, no tracker block) still loads and is
// inferred as kind=github.
func TestLoad_LegacyTopLevel(t *testing.T) {
	p := writeTmp(t, `
account: nSimonFR-ai
repos: [nSimonFR/nic-os, nSimonFR/gleaner]
profiles:
  - { match: "*", run: ["claude", "-p", "{prompt}"] }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracker.Kind != "github" {
		t.Errorf("Tracker.Kind = %q, want %q", cfg.Tracker.Kind, "github")
	}
	if cfg.Tracker.Account != "nSimonFR-ai" {
		t.Errorf("Tracker.Account = %q, want %q", cfg.Tracker.Account, "nSimonFR-ai")
	}
	if !reflect.DeepEqual(cfg.Tracker.Repos, []string{"nSimonFR/nic-os", "nSimonFR/gleaner"}) {
		t.Errorf("Tracker.Repos = %v", cfg.Tracker.Repos)
	}
	// Legacy mirror should still be populated.
	if cfg.Account != "nSimonFR-ai" {
		t.Errorf("legacy cfg.Account = %q", cfg.Account)
	}
}

// TestLoad_NewTrackerBlock verifies the Milestone-A native shape.
func TestLoad_NewTrackerBlock(t *testing.T) {
	p := writeTmp(t, `
tracker:
  kind: github
  account: nSimonFR-ai
  repos: [nSimonFR/nic-os]
profiles:
  - { match: "*", run: ["claude", "-p", "{prompt}"] }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracker.Kind != "github" {
		t.Errorf("Tracker.Kind = %q", cfg.Tracker.Kind)
	}
	if !reflect.DeepEqual(cfg.Tracker.Repos, []string{"nSimonFR/nic-os"}) {
		t.Errorf("Tracker.Repos = %v", cfg.Tracker.Repos)
	}
	// Legacy fields backfilled from tracker block.
	if cfg.Account != "nSimonFR-ai" {
		t.Errorf("backfilled cfg.Account = %q", cfg.Account)
	}
	if !reflect.DeepEqual(cfg.Repos, []string{"nSimonFR/nic-os"}) {
		t.Errorf("backfilled cfg.Repos = %v", cfg.Repos)
	}
}

// TestLoad_LinearTracker verifies the linear kind requires api_key_file,
// team_key, and codehost_repo.
func TestLoad_LinearTracker(t *testing.T) {
	p := writeTmp(t, `
tracker:
  kind: linear
  api_key_file: /run/agenix/linear-api-key
  team_key: MT
  codehost_repo: nSimonFR/nic-os
  active_states: [Todo, "In Progress"]
profiles:
  - { match: "*", run: ["claude", "-p", "{prompt}"] }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracker.Kind != "linear" {
		t.Errorf("Kind = %q", cfg.Tracker.Kind)
	}
	if cfg.Tracker.TeamKey != "MT" {
		t.Errorf("TeamKey = %q", cfg.Tracker.TeamKey)
	}
	if cfg.Tracker.CodehostRepo != "nSimonFR/nic-os" {
		t.Errorf("CodehostRepo = %q", cfg.Tracker.CodehostRepo)
	}
	if !reflect.DeepEqual(cfg.Tracker.ActiveStates, []string{"Todo", "In Progress"}) {
		t.Errorf("ActiveStates = %v", cfg.Tracker.ActiveStates)
	}
}

// TestValidate rejects configurations missing required fields.
func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string // substring expected in error message
	}{
		{
			name: "missing tracker.kind",
			yaml: `
profiles:
  - { match: "*", run: ["claude", "-p", "x"] }
`,
			want: "tracker.kind is required",
		},
		{
			name: "github without account",
			yaml: `
tracker: { kind: github, repos: [a/b] }
profiles:
  - { match: "*", run: ["claude"] }
`,
			want: "tracker.account is required for kind=github",
		},
		{
			name: "github without repos",
			yaml: `
tracker: { kind: github, account: nSimonFR-ai }
profiles:
  - { match: "*", run: ["claude"] }
`,
			want: "tracker.repos must not be empty",
		},
		{
			name: "linear without api_key_file",
			yaml: `
tracker: { kind: linear, team_key: MT, codehost_repo: a/b }
profiles:
  - { match: "*", run: ["claude"] }
`,
			want: "tracker.api_key_file is required",
		},
		{
			name: "linear without team_key",
			yaml: `
tracker: { kind: linear, api_key_file: /x, codehost_repo: a/b }
profiles:
  - { match: "*", run: ["claude"] }
`,
			want: "tracker.team_key is required",
		},
		{
			name: "linear without codehost_repo",
			yaml: `
tracker: { kind: linear, api_key_file: /x, team_key: MT }
profiles:
  - { match: "*", run: ["claude"] }
`,
			want: "tracker.codehost_repo is required",
		},
		{
			name: "unsupported tracker.kind",
			yaml: `
tracker: { kind: jira }
profiles:
  - { match: "*", run: ["claude"] }
`,
			want: `tracker.kind "jira" is not supported`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := writeTmp(t, tt.yaml)
			_, err := Load(p)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}

// TestMatchProfile_Wildcard ensures the existing "*" semantics work
// unchanged (regression guard for Milestone A's config rewrite).
func TestMatchProfile_Wildcard(t *testing.T) {
	cfg := &Config{Profiles: []Profile{
		{Name: "codex", Match: StringOrSlice{"provider:codex"}, Run: []string{"codex"}},
		{Name: "default", Match: StringOrSlice{"*"}, Run: []string{"claude"}},
	}}
	got := cfg.MatchProfile([]string{"complexity:trivial", "no-match"})
	if got == nil || got.Name != "default" {
		t.Fatalf("wildcard match: got %v", got)
	}
	got = cfg.MatchProfile([]string{"provider:codex", "complexity:routine"})
	if got == nil || got.Name != "codex" {
		t.Fatalf("explicit match: got %v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
