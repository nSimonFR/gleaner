// Package config loads the gleaner YAML config.
//
// The whole surface is a list of `triggers` — each one declares a
// quota predicate (`when`) and a command to exec (`run`) when it
// holds. The systemd timer fires `gleaner tick` and that's the entire
// driver. There are no Linear, GitHub, ranking, or orchestrator
// concerns at this layer — those belong in whatever the user puts in
// `run`.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Triggers []Trigger `yaml:"triggers"`
}

// Trigger is one quota-gated command.
//
//	triggers:
//	  - name: cyrus-handoff
//	    when: "claude.long_pct < 50 && codex.short_pct < 80"
//	    timeout: 10m
//	    env:
//	      LINEAR_KEY_FILE: /run/agenix/linear-key
//	    run:
//	      - claude
//	      - -p
//	      - "Pick the next NSI ticket labelled cyrus-ready, assign it to cyrus. Use the linear skill."
type Trigger struct {
	Name    string            `yaml:"name"`
	When    string            `yaml:"when"`
	Run     []string          `yaml:"run"`
	Timeout time.Duration     `yaml:"timeout,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

const DefaultTriggerTimeout = 5 * time.Minute

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) validate() error {
	seen := make(map[string]bool, len(c.Triggers))
	for i, t := range c.Triggers {
		if t.Name == "" {
			return fmt.Errorf("triggers[%d]: name is required", i)
		}
		if seen[t.Name] {
			return fmt.Errorf("triggers[%d]: duplicate name %q", i, t.Name)
		}
		seen[t.Name] = true
		if t.When == "" {
			return fmt.Errorf("triggers[%d] %q: when is required", i, t.Name)
		}
		if len(t.Run) == 0 {
			return fmt.Errorf("triggers[%d] %q: run must have at least argv[0]", i, t.Name)
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	for i := range c.Triggers {
		if c.Triggers[i].Timeout == 0 {
			c.Triggers[i].Timeout = DefaultTriggerTimeout
		}
	}
}
