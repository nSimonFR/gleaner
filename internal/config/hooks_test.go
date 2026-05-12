package config

import (
	"testing"
	"time"
)

// TestLoad_HooksBlock verifies the Milestone B hooks block parses cleanly
// and the defaults survive overlay.
func TestLoad_HooksBlock(t *testing.T) {
	p := writeTmp(t, `
tracker:
  kind: github
  account: nSimonFR-ai
  repos: [nSimonFR/nic-os]
hooks:
  after_create:  /etc/gleaner/hooks/init.sh
  before_run:    /etc/gleaner/hooks/quota-gate.sh
  after_run:     /etc/gleaner/hooks/cleanup.sh
  before_remove: /etc/gleaner/hooks/preserve.sh
  timeout: 30s
profiles:
  - { match: "*", run: ["claude", "-p", "{prompt}"] }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hooks.AfterCreate != "/etc/gleaner/hooks/init.sh" {
		t.Errorf("AfterCreate = %q", cfg.Hooks.AfterCreate)
	}
	if cfg.Hooks.BeforeRun != "/etc/gleaner/hooks/quota-gate.sh" {
		t.Errorf("BeforeRun = %q", cfg.Hooks.BeforeRun)
	}
	if cfg.Hooks.AfterRun != "/etc/gleaner/hooks/cleanup.sh" {
		t.Errorf("AfterRun = %q", cfg.Hooks.AfterRun)
	}
	if cfg.Hooks.BeforeRemove != "/etc/gleaner/hooks/preserve.sh" {
		t.Errorf("BeforeRemove = %q", cfg.Hooks.BeforeRemove)
	}
	if cfg.Hooks.Timeout != 30*time.Second {
		t.Errorf("Timeout = %s; want 30s", cfg.Hooks.Timeout)
	}
}

// TestLoad_HooksDefaultTimeout verifies the SPEC default lands even
// when the operator omits hooks.timeout.
func TestLoad_HooksDefaultTimeout(t *testing.T) {
	p := writeTmp(t, `
tracker:
  kind: github
  account: nSimonFR-ai
  repos: [nSimonFR/nic-os]
profiles:
  - { match: "*", run: ["claude"] }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hooks.Timeout != 60*time.Second {
		t.Errorf("default Timeout = %s; want 60s (SPEC §5.3.4)", cfg.Hooks.Timeout)
	}
	// Unset hooks should be empty strings (no inherited script).
	if cfg.Hooks.BeforeRun != "" || cfg.Hooks.AfterCreate != "" {
		t.Errorf("hooks should be empty when omitted: %+v", cfg.Hooks)
	}
}

// TestLoad_HookSingularStillWorks regression-tests the v0.0.3 event hook
// (singular `hook:`) — Milestone B's plural `hooks:` must not break it.
func TestLoad_HookSingularStillWorks(t *testing.T) {
	p := writeTmp(t, `
tracker:
  kind: github
  account: nSimonFR-ai
  repos: [nSimonFR/nic-os]
hook: /etc/gleaner/hooks/dispatch.sh
profiles:
  - { match: "*", run: ["claude"] }
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hook != "/etc/gleaner/hooks/dispatch.sh" {
		t.Errorf("legacy `hook:` not preserved: %q", cfg.Hook)
	}
}
