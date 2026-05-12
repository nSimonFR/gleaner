// Package config loads the user YAML and merges it over package defaults.
// The user-facing surface is deliberately tiny: aggressive defaults absorb
// timezone, hours, polling, require/block labels, plan, model flags, safety
// caps, and `name` derivation. The user only writes what diverges.
//
// Milestone A adds the `tracker:` block (kind = github | linear) with
// back-compat for the original top-level keys (`account`, `repos`,
// `require`, `block`). When `tracker:` is present it takes precedence;
// when only legacy keys are set, gleaner infers `tracker.kind: github`
// and emits a one-line deprecation hint at Load() time.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Legacy top-level keys. Still honored; populated from cfg.Tracker
	// after Load() so existing callers keep reading cfg.Account /
	// cfg.Repos. New code should prefer cfg.Tracker.*.
	Account string   `yaml:"account"`
	Repos   []string `yaml:"repos"`
	Require []string `yaml:"require"`
	Block   []string `yaml:"block"`

	// Tracker is the new, kind-aware tracker config. SPEC §5.3.
	Tracker Tracker `yaml:"tracker"`

	Hours       Hours       `yaml:"hours"`
	Guards      Guards      `yaml:"guards"`
	Profiles    []Profile   `yaml:"profiles"`
	Hook        string      `yaml:"hook"`        // v0.0.3 event hook (async, fire-and-forget)
	Hooks       Hooks       `yaml:"hooks"`       // Milestone B lifecycle hooks (sync, gating)
	Agent       Agent       `yaml:"agent"`       // Milestone C orchestrator settings (SPEC §5.3)
	Concurrency Concurrency `yaml:"concurrency"` // Milestone C per-provider sub-caps
	Server      Server      `yaml:"server"`      // Milestone E HTTP API + dashboard (SPEC §13.7)
	Safety      Safety      `yaml:"safety"`
}

// Server mirrors SPEC §13.7 `server` block. When Port > 0, gleaner
// binds 127.0.0.1:<Port> and serves /api/v1/* + the / dashboard.
// 0 (or unset) disables — gleaner stays headless.
type Server struct {
	Port int `yaml:"port"`
}

// Agent mirrors SPEC §5.3 `agent.*`. Defaults come from SPEC verbatim
// where reasonable; gleaner overrides max_concurrent_agents from 10 to
// 1 by default to preserve v0.0.x serial semantics. Operators opt into
// parallel dispatch by raising this.
type Agent struct {
	MaxConcurrentAgents int           `yaml:"max_concurrent_agents"`
	MaxRetryBackoff     time.Duration `yaml:"max_retry_backoff"`
	MaxTurns            int           `yaml:"max_turns"`     // passed to agent via {max_turns}
	ReadTimeout         time.Duration `yaml:"read_timeout"`  // Milestone D
	TurnTimeout         time.Duration `yaml:"turn_timeout"`  // Milestone D
	StallTimeout        time.Duration `yaml:"stall_timeout"` // Milestone D
}

// Concurrency is gleaner-native (no SPEC counterpart). Per-provider
// sub-cap on running workers, keyed by Profile.Plan. The check is
// applied in addition to Agent.MaxConcurrentAgents. The agnostic-agent
// value-add: Claude can be busy with the user while Codex drains.
type Concurrency struct {
	PerProvider map[string]int `yaml:"per_provider"`
}

// Hooks mirrors Symphony SPEC §5.3.4 — four lifecycle hooks fired by the
// executor around each dispatch attempt. All run via `bash -lc <script>`
// with cwd = workspace and the same GLEANER_* env the executor exports.
//
// Failure semantics per SPEC §9.4 (enforced in executor.Run):
//   - after_create non-zero  → fatal to dispatch (workspace destroyed)
//   - before_run  non-zero  → skip dispatch (denial, not failure)
//   - after_run   non-zero  → log and ignore
//   - before_remove non-zero → log and ignore
//
// SPEC's `hooks.timeout_ms` (default 60000) becomes `hooks.timeout`
// (default 60s) — gleaner uses Go-duration syntax everywhere.
type Hooks struct {
	AfterCreate  string        `yaml:"after_create"`
	BeforeRun    string        `yaml:"before_run"`
	AfterRun     string        `yaml:"after_run"`
	BeforeRemove string        `yaml:"before_remove"`
	Timeout      time.Duration `yaml:"timeout"`
}

// Tracker mirrors the SPEC §5.3 `tracker` block. Kind selects the adapter;
// each adapter consumes a subset of fields. Validation rejects fields that
// don't apply to the active kind only when they conflict with required ones;
// otherwise extra keys are tolerated for forward compatibility (per SPEC).
type Tracker struct {
	Kind string `yaml:"kind"` // "github" | "linear"

	// GitHub-specific. When omitted at this level but the legacy top-level
	// equivalents are set, Load() copies them in.
	Account string   `yaml:"account"`
	Repos   []string `yaml:"repos"`
	Require []string `yaml:"require"`
	Block   []string `yaml:"block"`

	// Linear-specific.
	APIKeyFile   string `yaml:"api_key_file"`
	TeamKey      string `yaml:"team_key"`      // e.g. "MT" (issue prefix)
	CodehostRepo string `yaml:"codehost_repo"` // owner/repo for PRs

	// Shared (SPEC §5.3). Used by the orchestrator's reconciliation step.
	ActiveStates   []string `yaml:"active_states"`
	TerminalStates []string `yaml:"terminal_states"`

	// Board state names for SPEC §7.1 SetState calls. Defaults match
	// Linear's stock workflow / GitHub Projects v2 default Status options.
	// Set either to "" to disable the corresponding transition (no
	// board write-back).
	InProgressState string `yaml:"in_progress_state"`
	ReviewState     string `yaml:"review_state"`

	// GitHub Projects v2 wiring for SetState. Both optional: when unset,
	// the adapter queries the issue's projectItems on first SetState to
	// auto-discover. Operators with multiple projects per repo should set
	// ProjectID explicitly to avoid picking the wrong one.
	ProjectID       string `yaml:"project_id"`
	StatusFieldID   string `yaml:"status_field_id"`
	StatusFieldName string `yaml:"status_field_name"` // default "Status"
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
		Tracker: Tracker{
			ActiveStates:    []string{"open"},   // GitHub default; Linear users override
			TerminalStates:  []string{"closed"}, // GitHub default; Linear users override
			InProgressState: "In Progress",      // SPEC §7.1; both trackers
			ReviewState:     "In Review",        // SPEC §7.1; both trackers
			StatusFieldName: "Status",           // GitHub Projects v2 default field name
		},
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
		Hooks: Hooks{
			Timeout: 60 * time.Second, // SPEC §5.3.4 default (hooks.timeout_ms = 60000)
		},
		Agent: Agent{
			MaxConcurrentAgents: 1,                // gleaner override: preserve v0.0.x serial pacing
			MaxRetryBackoff:     5 * time.Minute,  // SPEC §8.4 max_retry_backoff_ms = 300000
			MaxTurns:            20,               // SPEC §5.3 default
			ReadTimeout:         5 * time.Second,  // SPEC §5.3.6 (Milestone D)
			TurnTimeout:         1 * time.Hour,    // SPEC §5.3.6
			StallTimeout:        5 * time.Minute,  // SPEC §5.3.6
		},
	}
}

// Load reads a YAML file and overlays it onto Defaults(). After overlay,
// Load reconciles the legacy top-level keys (`account`, `repos`,
// `require`, `block`) with the new `tracker:` block: when tracker.kind is
// unset and legacy fields are present, it backfills tracker as kind=github
// and emits a deprecation hint to stderr.
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

	// Reconcile legacy keys ↔ Tracker block.
	if err := reconcileTracker(&cfg); err != nil {
		return nil, err
	}

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

// reconcileTracker normalizes the relationship between cfg.Tracker and the
// legacy top-level keys.
//
//   - If cfg.Tracker.Kind is set, it wins. Legacy keys are backfilled FROM
//     the tracker block (so cfg.Account / cfg.Repos / cfg.Require / cfg.Block
//     remain populated for legacy callers).
//   - If cfg.Tracker.Kind is unset but legacy keys are set, infer kind=github
//     and copy legacy → tracker. Print a one-line deprecation hint.
//   - If neither is set, leave cfg.Tracker.Kind = "" — Validate will reject
//     it later with a clear error.
func reconcileTracker(cfg *Config) error {
	t := &cfg.Tracker
	hasLegacy := cfg.Account != "" || len(cfg.Repos) > 0
	hasTracker := t.Kind != ""

	if hasTracker && hasLegacy {
		// Both set — tracker block wins, but warn so users don't silently
		// edit the wrong one.
		fmt.Fprintln(os.Stderr, "config: tracker.* and top-level account/repos both set; tracker.* takes precedence")
	}

	if hasTracker {
		// Backfill legacy top-level from tracker block (for old callers).
		switch t.Kind {
		case "github":
			if cfg.Account == "" {
				cfg.Account = t.Account
			}
			if len(cfg.Repos) == 0 {
				cfg.Repos = t.Repos
			}
			if len(cfg.Require) == 0 && len(t.Require) > 0 {
				cfg.Require = t.Require
			}
			if len(cfg.Block) == 0 && len(t.Block) > 0 {
				cfg.Block = t.Block
			}
			// GitHub state defaults match SPEC §5.3 active/terminal pair
			// for issue lifecycle (open / closed). Operators with custom
			// labels still override.
			if len(t.ActiveStates) == 0 {
				t.ActiveStates = []string{"open"}
			}
			if len(t.TerminalStates) == 0 {
				t.TerminalStates = []string{"closed"}
			}
		case "linear":
			// linear doesn't populate the legacy github fields; that's OK,
			// codehost callers will use cfg.Tracker.CodehostRepo.
			if cfg.Account == "" {
				cfg.Account = t.Account // operator may set explicitly for codehost auth
			}
			// SPEC §5.3 defaults for tracker.kind=linear. Without these,
			// reconciliation (Milestone D) silently has no terminal set
			// and can't notice when an issue went Done externally.
			if len(t.ActiveStates) == 0 {
				t.ActiveStates = []string{"Todo", "In Progress"}
			}
			if len(t.TerminalStates) == 0 {
				t.TerminalStates = []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"}
			}
		}
		return nil
	}

	// No tracker block. Infer from legacy if possible.
	if hasLegacy {
		fmt.Fprintln(os.Stderr, "config: top-level account/repos are deprecated; please migrate to `tracker: {kind: github, ...}`")
		t.Kind = "github"
		t.Account = cfg.Account
		t.Repos = cfg.Repos
		if len(t.Require) == 0 {
			t.Require = cfg.Require
		}
		if len(t.Block) == 0 {
			t.Block = cfg.Block
		}
	}
	return nil
}

// configOverlay mirrors Config with pointer-bearing scalars so the YAML
// decoder leaves un-set fields as nil — distinguishing "user omitted" from
// "user wrote 0". Each non-nil field is copied through to the merged Config.
type configOverlay struct {
	Account     *string             `yaml:"account"`
	Repos       *[]string           `yaml:"repos"`
	Require     *[]string           `yaml:"require"`
	Block       *[]string           `yaml:"block"`
	Tracker     *trackerOverlay     `yaml:"tracker"`
	Hours       *hoursOverlay       `yaml:"hours"`
	Guards      *guardsOverlay      `yaml:"guards"`
	Profiles    *[]Profile          `yaml:"profiles"`
	Hook        *string             `yaml:"hook"`
	Hooks       *hooksOverlay       `yaml:"hooks"`
	Agent       *agentOverlay       `yaml:"agent"`
	Concurrency *concurrencyOverlay `yaml:"concurrency"`
	Server      *serverOverlay      `yaml:"server"`
	Safety      *safetyOverlay      `yaml:"safety"`
}

type serverOverlay struct {
	Port *int `yaml:"port"`
}

type agentOverlay struct {
	MaxConcurrentAgents *int           `yaml:"max_concurrent_agents"`
	MaxRetryBackoff     *time.Duration `yaml:"max_retry_backoff"`
	MaxTurns            *int           `yaml:"max_turns"`
	ReadTimeout         *time.Duration `yaml:"read_timeout"`
	TurnTimeout         *time.Duration `yaml:"turn_timeout"`
	StallTimeout        *time.Duration `yaml:"stall_timeout"`
}

type concurrencyOverlay struct {
	PerProvider *map[string]int `yaml:"per_provider"`
}

type hooksOverlay struct {
	AfterCreate  *string        `yaml:"after_create"`
	BeforeRun    *string        `yaml:"before_run"`
	AfterRun     *string        `yaml:"after_run"`
	BeforeRemove *string        `yaml:"before_remove"`
	Timeout      *time.Duration `yaml:"timeout"`
}

type trackerOverlay struct {
	Kind            *string   `yaml:"kind"`
	Account         *string   `yaml:"account"`
	Repos           *[]string `yaml:"repos"`
	Require         *[]string `yaml:"require"`
	Block           *[]string `yaml:"block"`
	APIKeyFile      *string   `yaml:"api_key_file"`
	TeamKey         *string   `yaml:"team_key"`
	CodehostRepo    *string   `yaml:"codehost_repo"`
	ActiveStates    *[]string `yaml:"active_states"`
	TerminalStates  *[]string `yaml:"terminal_states"`
	InProgressState *string   `yaml:"in_progress_state"`
	ReviewState     *string   `yaml:"review_state"`
	ProjectID       *string   `yaml:"project_id"`
	StatusFieldID   *string   `yaml:"status_field_id"`
	StatusFieldName *string   `yaml:"status_field_name"`
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
	if ov.Hooks != nil {
		if ov.Hooks.AfterCreate != nil {
			base.Hooks.AfterCreate = *ov.Hooks.AfterCreate
		}
		if ov.Hooks.BeforeRun != nil {
			base.Hooks.BeforeRun = *ov.Hooks.BeforeRun
		}
		if ov.Hooks.AfterRun != nil {
			base.Hooks.AfterRun = *ov.Hooks.AfterRun
		}
		if ov.Hooks.BeforeRemove != nil {
			base.Hooks.BeforeRemove = *ov.Hooks.BeforeRemove
		}
		if ov.Hooks.Timeout != nil {
			base.Hooks.Timeout = *ov.Hooks.Timeout
		}
	}
	if ov.Agent != nil {
		if ov.Agent.MaxConcurrentAgents != nil {
			base.Agent.MaxConcurrentAgents = *ov.Agent.MaxConcurrentAgents
		}
		if ov.Agent.MaxRetryBackoff != nil {
			base.Agent.MaxRetryBackoff = *ov.Agent.MaxRetryBackoff
		}
		if ov.Agent.MaxTurns != nil {
			base.Agent.MaxTurns = *ov.Agent.MaxTurns
		}
		if ov.Agent.ReadTimeout != nil {
			base.Agent.ReadTimeout = *ov.Agent.ReadTimeout
		}
		if ov.Agent.TurnTimeout != nil {
			base.Agent.TurnTimeout = *ov.Agent.TurnTimeout
		}
		if ov.Agent.StallTimeout != nil {
			base.Agent.StallTimeout = *ov.Agent.StallTimeout
		}
	}
	if ov.Concurrency != nil && ov.Concurrency.PerProvider != nil {
		base.Concurrency.PerProvider = *ov.Concurrency.PerProvider
	}
	if ov.Server != nil && ov.Server.Port != nil {
		base.Server.Port = *ov.Server.Port
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

func (ov *trackerOverlay) applyTo(base *Tracker) {
	if ov.Kind != nil {
		base.Kind = *ov.Kind
	}
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
	if ov.APIKeyFile != nil {
		base.APIKeyFile = *ov.APIKeyFile
	}
	if ov.TeamKey != nil {
		base.TeamKey = *ov.TeamKey
	}
	if ov.CodehostRepo != nil {
		base.CodehostRepo = *ov.CodehostRepo
	}
	if ov.ActiveStates != nil {
		base.ActiveStates = *ov.ActiveStates
	}
	if ov.TerminalStates != nil {
		base.TerminalStates = *ov.TerminalStates
	}
	if ov.InProgressState != nil {
		base.InProgressState = *ov.InProgressState
	}
	if ov.ReviewState != nil {
		base.ReviewState = *ov.ReviewState
	}
	if ov.ProjectID != nil {
		base.ProjectID = *ov.ProjectID
	}
	if ov.StatusFieldID != nil {
		base.StatusFieldID = *ov.StatusFieldID
	}
	if ov.StatusFieldName != nil {
		base.StatusFieldName = *ov.StatusFieldName
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
	// Tracker validation.
	switch c.Tracker.Kind {
	case "":
		return fmt.Errorf("config: tracker.kind is required (set `tracker: {kind: github, ...}` or legacy `account`/`repos` top-level)")
	case "github":
		if c.Tracker.Account == "" {
			return fmt.Errorf("config: tracker.account is required for kind=github (e.g. nSimonFR-ai)")
		}
		if len(c.Tracker.Repos) == 0 {
			return fmt.Errorf("config: tracker.repos must not be empty for kind=github")
		}
	case "linear":
		if c.Tracker.APIKeyFile == "" {
			return fmt.Errorf("config: tracker.api_key_file is required for kind=linear")
		}
		if c.Tracker.TeamKey == "" {
			return fmt.Errorf("config: tracker.team_key is required for kind=linear (e.g. \"MT\")")
		}
		if c.Tracker.CodehostRepo == "" {
			return fmt.Errorf("config: tracker.codehost_repo is required for kind=linear (where to open PRs)")
		}
	default:
		return fmt.Errorf("config: tracker.kind %q is not supported (want github | linear)", c.Tracker.Kind)
	}
	return nil
}

// MatchProfile returns the first profile whose `match` matches at least one
// of the given labels, or has match "*". Returns nil if no profile matches.
//
// Matching is case-insensitive: incoming labels and configured matches are
// both lowercased before comparison. This handles both the GitHub case
// (labels are case-insensitive in the UI but case-sensitive in the API,
// which surfaces in unexpected casing) and the Linear case (operators
// often capitalize labels — `Complexity:Routine` rather than the gleaner
// convention `complexity:routine`). Without this, a Linear board using
// PascalCase silently misses every profile match.
func (c *Config) MatchProfile(labels []string) *Profile {
	labelSet := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		labelSet[strings.ToLower(l)] = struct{}{}
	}
	for i := range c.Profiles {
		p := &c.Profiles[i]
		for _, m := range p.Match {
			if m == "*" {
				return p
			}
			if _, ok := labelSet[strings.ToLower(m)]; ok {
				return p
			}
		}
	}
	return nil
}
