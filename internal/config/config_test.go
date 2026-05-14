package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestLoad_Minimal(t *testing.T) {
	p := writeTmp(t, `
tracker:
  api_key_file: /run/agenix/linear-api-key
  team_key: NSI
  cyrus_user_id: user_cyrus_abc
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracker.TeamKey != "NSI" {
		t.Errorf("TeamKey = %q", cfg.Tracker.TeamKey)
	}
	if cfg.Tracker.CyrusUserID != "user_cyrus_abc" {
		t.Errorf("CyrusUserID = %q", cfg.Tracker.CyrusUserID)
	}
	if !reflect.DeepEqual(cfg.Tracker.ActiveStates, []string{"Todo", "In Progress"}) {
		t.Errorf("ActiveStates defaults = %v", cfg.Tracker.ActiveStates)
	}
	if cfg.Guards.LongWindowCeiling != 0.92 {
		t.Errorf("LongWindowCeiling default = %v", cfg.Guards.LongWindowCeiling)
	}
}

func TestLoad_Overrides(t *testing.T) {
	p := writeTmp(t, `
tracker:
  api_key_file: /x
  team_key: NSI
  cyrus_user_id: u1
  active_states: [Todo]
guards:
  short_window_idle: 0.5
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Tracker.ActiveStates, []string{"Todo"}) {
		t.Errorf("ActiveStates = %v", cfg.Tracker.ActiveStates)
	}
	if cfg.Guards.ShortWindowIdle != 0.5 {
		t.Errorf("ShortWindowIdle = %v", cfg.Guards.ShortWindowIdle)
	}
	if cfg.Guards.LongWindowCeiling != 0.92 {
		t.Errorf("LongWindowCeiling should keep default; got %v", cfg.Guards.LongWindowCeiling)
	}
}

func TestValidate_Missing(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"no api_key_file", `tracker: { team_key: NSI, cyrus_user_id: u }`, "api_key_file"},
		{"no team_key", `tracker: { api_key_file: /x, cyrus_user_id: u }`, "team_key"},
		{"no cyrus_user_id", `tracker: { api_key_file: /x, team_key: NSI }`, "cyrus_user_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := writeTmp(t, tt.yaml)
			_, err := Load(p)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}
