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
//
// Overlay semantics: explicit user values override defaults; omitted keys
// inherit defaults. Implementation uses a pointer-bearing overlay struct
// so a user can disable a guard with `inflight_prs: 0` (or any other
// zero value) — the merger only copies fields whose pointer is non-nil.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	if path == "" {
		return &cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var overlay configOverlay
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	overlay.applyTo(&cfg)

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

// configOverlay mirrors Config with pointer-bearing scalars so the YAML
// decoder leaves un-set fields as nil — distinguishing "user omitted" from
// "user wrote 0". Each non-nil field is copied through to the merged Config.
type configOverlay struct {
	Account  *string             `yaml:"account"`
	Repos    *[]string           `yaml:"repos"`
	Require  *[]string           `yaml:"require"`
	Block    *[]string           `yaml:"block"`
	Hours    *hoursOverlay       `yaml:"hours"`
	Guards   *guardsOverlay      `yaml:"guards"`
	Profiles *[]Profile          `yaml:"profiles"`
	Hook     *string             `yaml:"hook"`
	Safety   *safetyOverlay      `yaml:"safety"`
}

type hoursOverlay struct {
	Active *string        `yaml:"active"`
	Drain  *string        `yaml:"drain"`
	Poll   *time.Duration `yaml:"poll"`
}

type guardsOverlay struct {
	InflightPRs       *int     `yaml:"inflight_prs"`
	AbortIfStep       *float64 `yaml:"abort_if_step"`
	ShortWindowIdle   *float64 `yaml:"short_window_idle"`
	ShortWindowActive *float64 `yaml:"short_window_active"`
	LongWindowCeiling *float64 `yaml:"long_window_ceiling"`
}

type safetyOverlay struct {
	MaxPerDay  *int    `yaml:"max_per_day"`
	KillSwitch *string `yaml:"kill_switch"`
}

func (ov *configOverlay) applyTo(base *Config) {
	if ov.Account != nil {
		base.Account = *ov.Account
	}
	if ov.Repos != nil {
		base.Repos = *ov.Repos
	}
	if ov.Require != nil {
		base.Require = *ov.Require
	}
	if ov.Block != nil {
		base.Block = *ov.Block
	}
	if ov.Hours != nil {
		if ov.Hours.Active != nil {
			base.Hours.Active = *ov.Hours.Active
		}
		if ov.Hours.Drain != nil {
			base.Hours.Drain = *ov.Hours.Drain
		}
		if ov.Hours.Poll != nil {
			base.Hours.Poll = *ov.Hours.Poll
		}
	}
	if ov.Guards != nil {
		if ov.Guards.InflightPRs != nil {
			base.Guards.InflightPRs = *ov.Guards.InflightPRs
		}
		if ov.Guards.AbortIfStep != nil {
			base.Guards.AbortIfStep = *ov.Guards.AbortIfStep
		}
		if ov.Guards.ShortWindowIdle != nil {
			base.Guards.ShortWindowIdle = *ov.Guards.ShortWindowIdle
		}
		if ov.Guards.ShortWindowActive != nil {
			base.Guards.ShortWindowActive = *ov.Guards.ShortWindowActive
		}
		if ov.Guards.LongWindowCeiling != nil {
			base.Guards.LongWindowCeiling = *ov.Guards.LongWindowCeiling
		}
	}
	if ov.Profiles != nil {
		base.Profiles = *ov.Profiles
	}
	if ov.Hook != nil {
		base.Hook = *ov.Hook
	}
	if ov.Safety != nil {
		if ov.Safety.MaxPerDay != nil {
			base.Safety.MaxPerDay = *ov.Safety.MaxPerDay
		}
		if ov.Safety.KillSwitch != nil {
			base.Safety.KillSwitch = *ov.Safety.KillSwitch
		}
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
