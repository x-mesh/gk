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
	Pull       PullConfig      `mapstructure:"pull"        yaml:"pull"`
	Preflight  PreflightConfig `mapstructure:"preflight"   yaml:"preflight"`
	Clone      CloneConfig     `mapstructure:"clone"       yaml:"clone"`
	Worktree   WorktreeConfig  `mapstructure:"worktree"    yaml:"worktree"`
	AI         AIConfig        `mapstructure:"ai"          yaml:"ai"`
}

// AIConfig controls AI-powered subcommands (commit, pr, review,
// changelog). Enabled is the master switch; flipping it false (or
// exporting GK_AI_DISABLE=1 which viper maps to this field) disables
// every AI subcommand with a clear error.
// Provider is the default AI CLI to use when --provider is not passed;
// empty means auto-detect (gemini → qwen → kiro-cli). Lang is the
// default message/output language (BCP-47 short code). Commit holds
// `gk commit` settings; future ai features add sibling structs.
type AIConfig struct {
	Enabled   bool              `mapstructure:"enabled"   yaml:"enabled"`
	Provider  string            `mapstructure:"provider"  yaml:"provider"`
	Lang      string            `mapstructure:"lang"      yaml:"lang"`
	Commit    AICommitConfig    `mapstructure:"commit"    yaml:"commit"`
	Anthropic AIAnthropicConfig `mapstructure:"anthropic" yaml:"anthropic"`
	OpenAI    AIOpenAIConfig    `mapstructure:"openai"    yaml:"openai"`
	Nvidia    AINvidiaConfig    `mapstructure:"nvidia"    yaml:"nvidia"`
	Groq      AIGroqConfig      `mapstructure:"groq"      yaml:"groq"`
}

// AIAnthropicConfig controls the Claude provider. Empty fields fall
// back to the adapter defaults.
type AIAnthropicConfig struct {
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
}

// AIOpenAIConfig controls the OpenAI provider.
type AIOpenAIConfig struct {
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
}

// AINvidiaConfig controls the NVIDIA AI provider. Model overrides the
// default LLM model identifier; Endpoint overrides the default Chat
// Completions API URL; Timeout is a Go duration string for HTTP requests.
type AINvidiaConfig struct {
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
}

// AIGroqConfig controls the Groq AI provider.
type AIGroqConfig struct {
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
}

// AICommitConfig controls `gk commit`. Mode is the default execution
// mode ("interactive" | "force" | "dry-run"); CLI flags override it.
// DenyPaths is a list of glob patterns (filepath.Match syntax) applied
// to every WIP file before it leaves the process — matches are either
// dropped silently or abort the run, never forwarded to the provider.
// AllowRemote gates providers whose Locality() is "remote"; when false
// only local providers may run (the policy layer may enforce this too).
// Trailer and Audit are opt-in telemetry knobs, both default off.
type AICommitConfig struct {
	Mode        string        `mapstructure:"mode"         yaml:"mode"`
	MaxGroups   int           `mapstructure:"max_groups"   yaml:"max_groups"`
	MaxTokens   int           `mapstructure:"max_tokens"   yaml:"max_tokens"`
	Timeout     string        `mapstructure:"timeout"      yaml:"timeout"`
	DenyPaths   []string      `mapstructure:"deny_paths"   yaml:"deny_paths"`
	AllowRemote bool          `mapstructure:"allow_remote" yaml:"allow_remote"`
	Trailer     bool          `mapstructure:"trailer"      yaml:"trailer"`
	Audit       bool          `mapstructure:"audit"        yaml:"audit"`
	Privacy     PrivacyConfig `mapstructure:"privacy"      yaml:"privacy"`
}

// PrivacyConfig tunes the Privacy Gate that runs before remote AI
// providers see any payload. MaxSecrets is the abort threshold for the
// builtin regex-based secret count (default 10); set to a negative value
// to disable the abort while still redacting matches.
type PrivacyConfig struct {
	MaxSecrets int `mapstructure:"max_secrets" yaml:"max_secrets"`
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
// AutoFetch controls whether `gk status` fetches the current branch's
// upstream before reading porcelain output. Default false — status does no
// network activity unless the caller passes `--fetch` / `-f`. Set
// `auto_fetch: true` in config to opt-in globally (equivalent to passing
// the flag on every invocation); the fetch itself remains strictly bounded
// (timeout, no prompts, no submodule recursion, no LFS side effects) and
// silent on failure, falling back to the local cached view.
type StatusConfig struct {
	Vis       []string `mapstructure:"vis"        yaml:"vis"`
	AutoFetch bool     `mapstructure:"auto_fetch" yaml:"auto_fetch"`
	// XYStyle controls how the two-letter porcelain code is rendered per
	// entry. "labels" (default) → word labels ("new", "mod", "staged",
	// "conflict"); "glyphs" → single-char markers (+ ~ ● ⚔ #); "raw"
	// keeps the literal git code (`??`, `.M`, `UU`). Overridable per call
	// via `--xy-style`.
	XYStyle string `mapstructure:"xy_style" yaml:"xy_style"`
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

// PullConfig controls gk pull behaviour.
// Strategy accepts: "rebase" (default), "merge", "ff-only", "auto".
// "auto" reads git config pull.rebase; if unset, falls back to "rebase".
type PullConfig struct {
	Strategy string `mapstructure:"strategy" yaml:"strategy"`
}

// PreflightConfig controls the sequence of checks gk preflight runs.
type PreflightConfig struct {
	Steps []PreflightStep `mapstructure:"steps" yaml:"steps"`
}

// CloneConfig controls `gk clone` shorthand expansion and post-clone hooks.
//
//   - DefaultProtocol: "ssh" (default) or "https". Determines the URL form
//     used when the caller passes a bare `owner/repo`.
//   - DefaultHost: the hostname inserted when only `owner/repo` is given
//     ("github.com" by default).
//   - Root: optional filesystem root for Go-style layout. When non-empty,
//     `gk clone owner/repo` places the checkout at
//     `<root>/<host>/<owner>/<repo>` unless an explicit target is passed.
//     Empty means "let git pick a directory in cwd" (the standard default).
//   - Hosts: alias table for multi-host users. `gk clone gl:group/repo`
//     looks up `gl` here, using the per-alias host + protocol (falling
//     back to DefaultProtocol when the alias omits it).
//   - PostActions: subcommands to run inside the freshly-cloned checkout.
//     Supported values: "hooks-install" (invokes `gk hooks install --all`)
//     and "doctor" (invokes `gk doctor`). Default empty — opt-in only.
type CloneConfig struct {
	DefaultProtocol string               `mapstructure:"default_protocol" yaml:"default_protocol"`
	DefaultHost     string               `mapstructure:"default_host"     yaml:"default_host"`
	Root            string               `mapstructure:"root"             yaml:"root"`
	Hosts           map[string]HostAlias `mapstructure:"hosts"            yaml:"hosts"`
	PostActions     []string             `mapstructure:"post_actions"     yaml:"post_actions"`
}

// HostAlias names a custom clone shorthand like `gl:` or `work:`.
// Protocol is optional; when empty, CloneConfig.DefaultProtocol wins.
type HostAlias struct {
	Host     string `mapstructure:"host"     yaml:"host"`
	Protocol string `mapstructure:"protocol" yaml:"protocol"`
}

// WorktreeConfig controls how `gk worktree add <name>` maps a relative
// name argument into a real filesystem path. Default layout:
//
//	<Base>/<Project>/<name>
//
// Base defaults to `~/.gk/worktree` so worktrees live outside the main
// checkout (safer for IDEs and backup sweeps). Project defaults to the
// basename of the repo's toplevel directory — override when two clones
// share the same basename (e.g. `work/gk` and `personal/gk`). An
// absolute path passed to `gk worktree add` always wins and is used
// verbatim; the managed layout only applies to bare/relative names.
type WorktreeConfig struct {
	Base    string `mapstructure:"base"    yaml:"base"`
	Project string `mapstructure:"project" yaml:"project"`
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
			// `bar` and `progress` overlap heavily (bar shows composition,
			// progress shows remaining-verbs over the same counts). Drop
			// `bar` from the default to cut a row; users who want both
			// can add `bar` back via .gk.yaml.
			Vis:       []string{"gauge", "progress", "tree", "staleness"},
			AutoFetch: false,
			XYStyle:   "labels",
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
		Pull: PullConfig{
			Strategy: "rebase",
		},
		Preflight: PreflightConfig{
			Steps: []PreflightStep{
				{Name: "commit-lint", Command: "commit-lint"},
				{Name: "branch-check", Command: "branch-check"},
				{Name: "no-conflict", Command: "no-conflict"},
			},
		},
		Clone: CloneConfig{
			DefaultProtocol: "ssh",
			DefaultHost:     "github.com",
			Root:            "",
			Hosts:           nil,
			PostActions:     nil,
		},
		Worktree: WorktreeConfig{
			Base:    "~/.gk/worktree",
			Project: "",
		},
		AI: AIConfig{
			Enabled:  true,
			Provider: "",
			Lang:     "en",
			Commit: AICommitConfig{
				Mode:      "interactive",
				MaxGroups: 10,
				MaxTokens: 24000,
				Timeout:   "30s",
				DenyPaths: []string{
					".env",
					".env.*",
					"*.pem",
					"id_rsa*",
					"credentials.json",
					"*.pfx",
					"*.kdbx",
					"*.keystore",
					"service-account*.json",
					"terraform.tfstate",
					"terraform.tfstate.*",
				},
				AllowRemote: true,
				Trailer:     false,
				Audit:       false,
			},
			Nvidia: AINvidiaConfig{
				Timeout: "60s",
			},
			Groq: AIGroqConfig{
				Timeout: "60s",
			},
		},
	}
}
