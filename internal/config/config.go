// Package config loads the user YAML and merges it over package defaults.
// The surface is deliberately tiny: gleaner is a Linear-only picker that
// hands tickets off to Cyrus, so the only inputs are tracker credentials,
// the Cyrus user id to assign to, hours-of-day, quota ceilings, and a
// kill-switch path.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Tracker Tracker `yaml:"tracker"`
	Hours   Hours   `yaml:"hours"`
	Guards  Guards  `yaml:"guards"`
	Safety  Safety  `yaml:"safety"`
}

// Tracker is the Linear-tracker config block.
type Tracker struct {
	APIKeyFile   string   `yaml:"api_key_file"`
	TeamKey      string   `yaml:"team_key"`      // e.g. "NSI" (issue prefix)
	CyrusUserID  string   `yaml:"cyrus_user_id"` // Linear user id of the Cyrus agent — assignee target for handoff
	ActiveStates []string `yaml:"active_states"` // states the picker considers (default: Todo + In Progress)
}

type Hours struct {
	Active string        `yaml:"active"` // "HH:MM-HH:MM" — stricter quota ceiling applies inside this window
	Drain  string        `yaml:"drain"`  // "HH:MM-HH:MM" — permissive quota ceiling applies inside this window
	Poll   time.Duration `yaml:"poll"`   // timer cadence (informational; the systemd timer is the actual driver)
}

type Guards struct {
	ShortWindowIdle   float64 `yaml:"short_window_idle"`
	ShortWindowActive float64 `yaml:"short_window_active"`
	LongWindowCeiling float64 `yaml:"long_window_ceiling"`
}

type Safety struct {
	KillSwitch string `yaml:"kill_switch"`
}

// Defaults returns a Config populated with package defaults.
func Defaults() Config {
	return Config{
		Tracker: Tracker{
			ActiveStates: []string{"Todo", "In Progress"},
		},
		Hours: Hours{
			Active: "09:00-19:00",
			Drain:  "22:00-07:00",
			Poll:   10 * time.Minute,
		},
		Guards: Guards{
			ShortWindowIdle:   0.75,
			ShortWindowActive: 0.30,
			LongWindowCeiling: 0.92,
		},
		Safety: Safety{
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

	var overlay configOverlay
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	overlay.applyTo(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Tracker.APIKeyFile == "" {
		return fmt.Errorf("config: tracker.api_key_file is required")
	}
	if c.Tracker.TeamKey == "" {
		return fmt.Errorf("config: tracker.team_key is required (e.g. \"NSI\")")
	}
	if c.Tracker.CyrusUserID == "" {
		return fmt.Errorf("config: tracker.cyrus_user_id is required (the Linear user id the picker assigns tickets to)")
	}
	return nil
}

// configOverlay mirrors Config with pointer-bearing scalars so the YAML
// decoder leaves un-set fields as nil — distinguishing "user omitted"
// from "user wrote 0".
type configOverlay struct {
	Tracker *trackerOverlay `yaml:"tracker"`
	Hours   *hoursOverlay   `yaml:"hours"`
	Guards  *guardsOverlay  `yaml:"guards"`
	Safety  *safetyOverlay  `yaml:"safety"`
}

type trackerOverlay struct {
	APIKeyFile   *string   `yaml:"api_key_file"`
	TeamKey      *string   `yaml:"team_key"`
	CyrusUserID  *string   `yaml:"cyrus_user_id"`
	ActiveStates *[]string `yaml:"active_states"`
}

type hoursOverlay struct {
	Active *string        `yaml:"active"`
	Drain  *string        `yaml:"drain"`
	Poll   *time.Duration `yaml:"poll"`
}

type guardsOverlay struct {
	ShortWindowIdle   *float64 `yaml:"short_window_idle"`
	ShortWindowActive *float64 `yaml:"short_window_active"`
	LongWindowCeiling *float64 `yaml:"long_window_ceiling"`
}

type safetyOverlay struct {
	KillSwitch *string `yaml:"kill_switch"`
}

func (ov *configOverlay) applyTo(base *Config) {
	if ov.Tracker != nil {
		ov.Tracker.applyTo(&base.Tracker)
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
	if ov.Safety != nil && ov.Safety.KillSwitch != nil {
		base.Safety.KillSwitch = *ov.Safety.KillSwitch
	}
}

func (ov *trackerOverlay) applyTo(base *Tracker) {
	if ov.APIKeyFile != nil {
		base.APIKeyFile = *ov.APIKeyFile
	}
	if ov.TeamKey != nil {
		base.TeamKey = *ov.TeamKey
	}
	if ov.CyrusUserID != nil {
		base.CyrusUserID = *ov.CyrusUserID
	}
	if ov.ActiveStates != nil {
		base.ActiveStates = *ov.ActiveStates
	}
}
