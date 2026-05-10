// Package config loads the user YAML and merges it over package defaults.
// The user-facing surface is deliberately tiny: aggressive defaults absorb
// timezone, hours, polling, require/block labels, plan, model flags, safety
// caps, and `name` derivation. The user only writes what diverges.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Account  string    `yaml:"account"`
	Repos    []string  `yaml:"repos"`
	Require  []string  `yaml:"require"`
	Block    []string  `yaml:"block"`
	Hours    Hours     `yaml:"hours"`
	Guards   Guards    `yaml:"guards"`
	Profiles []Profile `yaml:"profiles"`
	Hook     string    `yaml:"hook"`
	Safety   Safety    `yaml:"safety"`
}

type Hours struct {
	Active string        `yaml:"active"` // "HH:MM-HH:MM"
	Drain  string        `yaml:"drain"`
	Poll   time.Duration `yaml:"poll"`
}

type Guards struct {
	InflightPRs       int     `yaml:"inflight_prs"`
	AbortIfStep       float64 `yaml:"abort_if_step"` // reserved for v0.0.4 delta-safety abort (one tool-call burning > this % of short window)
	ShortWindowIdle   float64 `yaml:"short_window_idle"`
	ShortWindowActive float64 `yaml:"short_window_active"`
	LongWindowCeiling float64 `yaml:"long_window_ceiling"`
}

type Profile struct {
	Name      string        `yaml:"name"`
	Match     StringOrSlice `yaml:"match"`
	Plan      string        `yaml:"plan"`
	Run       []string      `yaml:"run"`
	OnSuccess string        `yaml:"on_success"`
	Cwd       string        `yaml:"cwd"`
	Timeout   time.Duration `yaml:"timeout"`
}

type Safety struct {
	MaxPerDay  int    `yaml:"max_per_day"`
	KillSwitch string `yaml:"kill_switch"`
}

// StringOrSlice accepts either a YAML string or an array of strings.
// Used for `match:` so users can write `match: "*"` or `match: [a, b]`.
type StringOrSlice []string

func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		*s = []string{value.Value}
		return nil
	}
	var slice []string
	if err := value.Decode(&slice); err != nil {
		return err
	}
	*s = slice
	return nil
}

// Defaults returns a Config populated with package defaults. User config
// is merged on top via Load(); zero-valued user fields keep defaults.
func Defaults() Config {
	return Config{
		Require: []string{"afk-ready"},
		Block:   []string{"needs-human", "blocked", "wip"},
		Hours: Hours{
			Active: "09:00-19:00",
			Drain:  "22:00-07:00",
			Poll:   10 * time.Minute,
		},
		Guards: Guards{
			InflightPRs:       3,
			AbortIfStep:       0.15,
			ShortWindowIdle:   0.75,
			ShortWindowActive: 0.30,
			LongWindowCeiling: 0.92,
		},
		Safety: Safety{
			MaxPerDay:  5,
			KillSwitch: "/var/lib/gleaner/disabled",
		},
	}
}

// Load reads a YAML file and overlays it onto Defaults().
func Load(path string) (*Config, error) {
	cfg := Defaults()
	if path == "" {
		return &cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Decode user file into an overlay struct, then merge non-zero fields.
	var overlay Config
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	mergeOverlay(&cfg, &overlay)

	// Derive profile names and plans where omitted.
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		if p.Name == "" {
			p.Name = deriveProfileName(p.Run)
		}
		if p.Plan == "" {
			p.Plan = derivePlanFromRun(p.Run)
		}
		if p.Timeout == 0 {
			p.Timeout = 30 * time.Minute
		}
		if p.Cwd == "" {
			p.Cwd = "{worktree}"
		}
		if p.OnSuccess == "" {
			p.OnSuccess = "open_pr"
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func mergeOverlay(base, ov *Config) {
	if ov.Account != "" {
		base.Account = ov.Account
	}
	if len(ov.Repos) > 0 {
		base.Repos = ov.Repos
	}
	if len(ov.Require) > 0 {
		base.Require = ov.Require
	}
	if len(ov.Block) > 0 {
		base.Block = ov.Block
	}
	if ov.Hours.Active != "" {
		base.Hours.Active = ov.Hours.Active
	}
	if ov.Hours.Drain != "" {
		base.Hours.Drain = ov.Hours.Drain
	}
	if ov.Hours.Poll != 0 {
		base.Hours.Poll = ov.Hours.Poll
	}
	if ov.Guards.InflightPRs != 0 {
		base.Guards.InflightPRs = ov.Guards.InflightPRs
	}
	if ov.Guards.AbortIfStep != 0 {
		base.Guards.AbortIfStep = ov.Guards.AbortIfStep
	}
	if ov.Guards.ShortWindowIdle != 0 {
		base.Guards.ShortWindowIdle = ov.Guards.ShortWindowIdle
	}
	if ov.Guards.ShortWindowActive != 0 {
		base.Guards.ShortWindowActive = ov.Guards.ShortWindowActive
	}
	if ov.Guards.LongWindowCeiling != 0 {
		base.Guards.LongWindowCeiling = ov.Guards.LongWindowCeiling
	}
	if len(ov.Profiles) > 0 {
		base.Profiles = ov.Profiles
	}
	if ov.Hook != "" {
		base.Hook = ov.Hook
	}
	if ov.Safety.MaxPerDay != 0 {
		base.Safety.MaxPerDay = ov.Safety.MaxPerDay
	}
	if ov.Safety.KillSwitch != "" {
		base.Safety.KillSwitch = ov.Safety.KillSwitch
	}
}

// derivePlanFromRun maps the first command of `run:` to a plan identifier.
// Used when the user omits `plan:`. Unknown commands return "".
func derivePlanFromRun(run []string) string {
	if len(run) == 0 {
		return ""
	}
	switch strings.ToLower(run[0]) {
	case "claude":
		return "claude/team"
	case "codex":
		return "codex/plus"
	}
	return ""
}

// deriveProfileName derives a sensible name from `run:` when the user
// omits `name:`. Falls back to "run[0]" if the verb isn't recognized.
func deriveProfileName(run []string) string {
	if len(run) == 0 {
		return "unnamed"
	}
	base := strings.ToLower(run[0])
	// `npx opencastle run X.convoy.yml` → "opencastle-X"
	if base == "npx" && len(run) >= 4 && strings.ToLower(run[1]) == "opencastle" {
		convoy := run[3]
		if i := strings.Index(convoy, "."); i > 0 {
			convoy = convoy[:i]
		}
		return "opencastle-" + convoy
	}
	// `claude -p ...` → "claude"; `codex exec ...` → "codex"
	return base
}

func (c *Config) Validate() error {
	if len(c.Profiles) == 0 {
		return fmt.Errorf("config: at least one profile is required")
	}
	for i, p := range c.Profiles {
		if len(p.Run) == 0 {
			return fmt.Errorf("config: profile %q (#%d): run must be a non-empty array", p.Name, i)
		}
		if len(p.Match) == 0 {
			return fmt.Errorf("config: profile %q (#%d): match is required", p.Name, i)
		}
		switch p.OnSuccess {
		case "open_pr", "comment", "none":
		default:
			return fmt.Errorf("config: profile %q: on_success must be open_pr | comment | none, got %q", p.Name, p.OnSuccess)
		}
	}
	return nil
}

// MatchProfile returns the first profile whose `match` matches at least one
// of the given labels, or has match "*". Returns nil if no profile matches.
func (c *Config) MatchProfile(labels []string) *Profile {
	labelSet := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		labelSet[l] = struct{}{}
	}
	for i := range c.Profiles {
		p := &c.Profiles[i]
		for _, m := range p.Match {
			if m == "*" {
				return p
			}
			if _, ok := labelSet[m]; ok {
				return p
			}
		}
	}
	return nil
}
