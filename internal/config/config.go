package config

// Config holds the full resolved configuration for gk.
type Config struct {
	BaseBranch string          `mapstructure:"base_branch" yaml:"base_branch"`
	Remote     string          `mapstructure:"remote"      yaml:"remote"`
	Log        LogConfig       `mapstructure:"log"         yaml:"log"`
	Status     StatusConfig    `mapstructure:"status"      yaml:"status"`
	UI         UIConfig        `mapstructure:"ui"          yaml:"ui"`
	Branch     BranchConfig    `mapstructure:"branch"      yaml:"branch"`
	Commit     CommitConfig    `mapstructure:"commit"      yaml:"commit"`
	Push       PushConfig      `mapstructure:"push"        yaml:"push"`
	Preflight  PreflightConfig `mapstructure:"preflight"   yaml:"preflight"`
}

// LogConfig controls git log output format. Vis is the default set of
// visualization layers applied when the caller does not pass any viz flag;
// pass `--vis none` or a comma-list to override for a single invocation.
type LogConfig struct {
	Format string   `mapstructure:"format" yaml:"format"`
	Graph  bool     `mapstructure:"graph"  yaml:"graph"`
	Limit  int      `mapstructure:"limit"  yaml:"limit"`
	Vis    []string `mapstructure:"vis"    yaml:"vis"`
}

// StatusConfig controls gk status defaults. Vis is the default set of
// visualization layers applied when the caller does not pass --vis. Pass
// --vis none on the CLI to turn them all off for a single invocation.
//
// AutoFetch, when true, makes `gk status` attempt a short, quiet fetch of
// the current branch's upstream before reading the porcelain output so
// that ↑N ↓N counts reflect the actual remote state. Fetch is strictly
// bounded (timeout, no prompts, no submodule recursion, no LFS side
// effects) and silent on failure, falling back to the local cached view.
// Disable globally with `auto_fetch: false`, per-invocation with
// `--no-fetch`, or via `GK_NO_FETCH=1`.
type StatusConfig struct {
	Vis       []string `mapstructure:"vis"        yaml:"vis"`
	AutoFetch bool     `mapstructure:"auto_fetch" yaml:"auto_fetch"`
}

// UIConfig controls terminal UI behaviour.
type UIConfig struct {
	Color  string `mapstructure:"color"  yaml:"color"`
	Prefer string `mapstructure:"prefer" yaml:"prefer"`
}

// BranchConfig controls branch management + naming policy.
type BranchConfig struct {
	StaleDays     int      `mapstructure:"stale_days"     yaml:"stale_days"`
	Protected     []string `mapstructure:"protected"      yaml:"protected"`
	Patterns      []string `mapstructure:"patterns"       yaml:"patterns"`
	AllowDetached bool     `mapstructure:"allow_detached" yaml:"allow_detached"`
}

// CommitConfig controls commit-message linting rules.
type CommitConfig struct {
	Types            []string `mapstructure:"types"              yaml:"types"`
	ScopeRequired    bool     `mapstructure:"scope_required"     yaml:"scope_required"`
	MaxSubjectLength int      `mapstructure:"max_subject_length" yaml:"max_subject_length"`
}

// PushConfig controls gk push safety rails.
type PushConfig struct {
	Protected      []string `mapstructure:"protected"       yaml:"protected"`
	SecretPatterns []string `mapstructure:"secret_patterns" yaml:"secret_patterns"`
	AllowForce     bool     `mapstructure:"allow_force"     yaml:"allow_force"`
}

// PreflightConfig controls the sequence of checks gk preflight runs.
type PreflightConfig struct {
	Steps []PreflightStep `mapstructure:"steps" yaml:"steps"`
}

// PreflightStep is one check in the preflight sequence.
// Command can be a shell command (e.g., "make test") or a built-in
// alias: "commit-lint", "branch-check", "no-conflict".
type PreflightStep struct {
	Name              string `mapstructure:"name"         yaml:"name"`
	Command           string `mapstructure:"command"      yaml:"command"`
	ContinueOnFailure bool   `mapstructure:"continue_on_failure" yaml:"continue_on_failure"`
}

// Defaults returns a Config populated with default values.
func Defaults() Config {
	return Config{
		BaseBranch: "",
		Remote:     "origin",
		Log: LogConfig{
			// Color tokens require `--color=always` (or a TTY); gk handles that in runLog.
			Format: "%C(yellow)%h%C(reset) %C(green)(%ar)%C(reset) %C(bold blue)<%an>%C(reset) %s%C(auto)%d%C(reset)",
			Graph:  false,
			Limit:  20,
			Vis:    []string{"cc", "safety", "tags-rule"},
		},
		Status: StatusConfig{
			Vis:       []string{"gauge", "bar", "progress"},
			AutoFetch: true,
		},
		UI: UIConfig{
			Color:  "auto",
			Prefer: "",
		},
		Branch: BranchConfig{
			StaleDays:     30,
			Protected:     []string{"main", "master", "develop"},
			Patterns:      []string{`^(feat|fix|chore|docs|refactor|test|perf|build|ci|revert)/[a-z0-9._-]+$`},
			AllowDetached: false,
		},
		Commit: CommitConfig{
			Types:            []string{"feat", "fix", "chore", "docs", "style", "refactor", "perf", "test", "build", "ci", "revert"},
			ScopeRequired:    false,
			MaxSubjectLength: 72,
		},
		Push: PushConfig{
			Protected:      []string{"main", "master"},
			SecretPatterns: nil, // user-added; built-ins live in internal/secrets
			AllowForce:     false,
		},
		Preflight: PreflightConfig{
			Steps: []PreflightStep{
				{Name: "commit-lint", Command: "commit-lint"},
				{Name: "branch-check", Command: "branch-check"},
				{Name: "no-conflict", Command: "no-conflict"},
			},
		},
	}
}
