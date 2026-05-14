package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gleaner.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_Minimal(t *testing.T) {
	p := writeTemp(t, `
triggers:
  - name: tap
    when: "claude.long_pct < 90"
    run: ["echo", "hi"]
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Triggers) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(c.Triggers))
	}
	got := c.Triggers[0]
	if got.Name != "tap" || got.When != "claude.long_pct < 90" {
		t.Fatalf("unexpected trigger: %+v", got)
	}
	if got.Timeout != DefaultTriggerTimeout {
		t.Fatalf("default timeout not applied: %v", got.Timeout)
	}
}

func TestLoad_FullTrigger(t *testing.T) {
	p := writeTemp(t, `
triggers:
  - name: cyrus
    when: "claude.long_pct < 50"
    timeout: 30s
    env:
      FOO: bar
    run: ["claude", "-p", "go"]
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	tr := c.Triggers[0]
	if tr.Timeout != 30*time.Second {
		t.Fatalf("timeout: %v", tr.Timeout)
	}
	if tr.Env["FOO"] != "bar" {
		t.Fatalf("env: %v", tr.Env)
	}
	if len(tr.Run) != 3 {
		t.Fatalf("run: %v", tr.Run)
	}
}

func TestLoad_Empty(t *testing.T) {
	p := writeTemp(t, "")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Triggers) != 0 {
		t.Fatalf("want 0 triggers, got %d", len(c.Triggers))
	}
}

func TestLoad_Errors(t *testing.T) {
	cases := map[string]string{
		"missing name": `triggers: [{when: "true == true", run: ["x"]}]`,
		"missing when": `triggers: [{name: a, run: ["x"]}]`,
		"missing run":  `triggers: [{name: a, when: "true == true"}]`,
		"dup name": `triggers:
  - {name: a, when: "true == true", run: ["x"]}
  - {name: a, when: "true == true", run: ["y"]}`,
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			p := writeTemp(t, body)
			if _, err := Load(p); err == nil {
				t.Fatalf("%s: expected error, got nil", label)
			}
		})
	}
}
