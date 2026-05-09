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
	Sync       SyncConfig      `mapstructure:"sync"        yaml:"sync"`
	Preflight  PreflightConfig `mapstructure:"preflight"   yaml:"preflight"`
	Clone      CloneConfig     `mapstructure:"clone"       yaml:"clone"`
	Worktree   WorktreeConfig  `mapstructure:"worktree"    yaml:"worktree"`
	AI         AIConfig        `mapstructure:"ai"          yaml:"ai"`
	Output     OutputConfig    `mapstructure:"output"      yaml:"output"`
}

// OutputConfig controls Easy Mode output behaviour.
//   - Easy: master switch for Easy Mode (default false).
//   - Lang: message catalogue language, BCP-47 short code (default "ko").
//   - Emoji: prepend emoji to status lines (default true).
//   - Hints: hint verbosity level — "verbose", "minimal", or "off" (default "verbose").
type OutputConfig struct {
	Easy  bool   `mapstructure:"easy"  yaml:"easy"`
	Lang  string `mapstructure:"lang"  yaml:"lang"`
	Emoji bool   `mapstructure:"emoji" yaml:"emoji"`
	Hints string `mapstructure:"hints" yaml:"hints"`
}

// AIConfig controls AI-powered subcommands (commit, pr, review,
// changelog). Enabled is the master switch; flipping it false (or
// exporting GK_AI_DISABLE=1 which viper maps to this field) disables
// every AI subcommand with a clear error.
// Provider is the default AI CLI to use when --provider is not passed;
// empty means auto-detect (gemini → qwen → kiro-cli). Lang is the
// default message/output language (BCP-47 short code). Commit holds
// `gk commit` settings; Chat holds `gk do/explain/ask` settings;
// future ai features add sibling structs.
type AIConfig struct {
	Enabled   bool              `mapstructure:"enabled"   yaml:"enabled"`
	Provider  string            `mapstructure:"provider"  yaml:"provider"`
	Lang      string            `mapstructure:"lang"      yaml:"lang"`
	Assist    AIAssistConfig    `mapstructure:"assist"    yaml:"assist"`
	Commit    AICommitConfig    `mapstructure:"commit"    yaml:"commit"`
	Chat      AIChatConfig      `mapstructure:"chat"      yaml:"chat"`
	Anthropic AIAnthropicConfig `mapstructure:"anthropic" yaml:"anthropic"`
	OpenAI    AIOpenAIConfig    `mapstructure:"openai"    yaml:"openai"`
	Nvidia    AINvidiaConfig    `mapstructure:"nvidia"    yaml:"nvidia"`
	Groq      AIGroqConfig      `mapstructure:"groq"      yaml:"groq"`
}

// AIAssistConfig controls AI help that is attached to existing commands.
// Mode accepts:
//   - "off": never attach AI help unless a CLI flag explicitly asks for it.
//   - "suggest": print a lightweight hint pointing to `gk next`.
//   - "auto": run the configured AI assistant automatically for enabled
//     surfaces such as `gk status`.
//
// Status gates the `gk status` surface. IncludeDiff is reserved for future
// richer prompts; the current status assistant sends structured repo facts
// only, not patch contents.
type AIAssistConfig struct {
	Mode        string `mapstructure:"mode"         yaml:"mode"`
	Status      bool   `mapstructure:"status"       yaml:"status"`
	IncludeDiff bool   `mapstructure:"include_diff" yaml:"include_diff"`
}

// AIChatConfig controls the AI chat subcommands (`gk do`, `gk explain`,
// `gk ask`). Timeout is a Go duration string for AI provider calls
// (default "30s"). MaxTokens caps the response token budget (default
// 4096). SafetyConfirm controls whether `gk do` requires an extra
// confirmation prompt for dangerous commands (default true).
type AIChatConfig struct {
	Timeout       string `mapstructure:"timeout"        yaml:"timeout"`
	MaxTokens     int    `mapstructure:"max_tokens"     yaml:"max_tokens"`
	SafetyConfirm bool   `mapstructure:"safety_confirm" yaml:"safety_confirm"`
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
	Mode        string        `mapstructure:"mode"          yaml:"mode"`
	MaxGroups   int           `mapstructure:"max_groups"    yaml:"max_groups"`
	MaxTokens   int           `mapstructure:"max_tokens"    yaml:"max_tokens"`
	Timeout     string        `mapstructure:"timeout"       yaml:"timeout"`
	DenyPaths   []string      `mapstructure:"deny_paths"    yaml:"deny_paths"`
	AllowRemote bool          `mapstructure:"allow_remote"  yaml:"allow_remote"`
	Trailer     bool          `mapstructure:"trailer"       yaml:"trailer"`
	Audit       bool          `mapstructure:"audit"         yaml:"audit"`
	Privacy     PrivacyConfig `mapstructure:"privacy"       yaml:"privacy"`
	// WIPPatterns are EXTRA subject regexes that mark a commit as a
	// WIP-like save-point eligible for chain unwrap. They ADD to the
	// hard-coded defaults (`--wip--`, `wip:`, `tmp`, `save`,
	// `checkpoint`, `fixup!`, `squash!`) — empty list keeps just the
	// defaults.
	WIPPatterns []string `mapstructure:"wip_patterns"  yaml:"wip_patterns"`
	// WIPMaxChain caps how many recent commits the unwrap pass will
	// consider. 0 falls back to 10. Stops a runaway chain on a branch
	// that is entirely save-point commits.
	WIPMaxChain int `mapstructure:"wip_max_chain" yaml:"wip_max_chain"`
	// WIPEnabled is the global on/off switch for the chain unwrap
	// pass. Defaults true. The CLI flag --no-wip-unwrap is OR-ed with
	// `!WIPEnabled` per invocation, so users can disable entirely via
	// config or one-shot via flag.
	WIPEnabled bool `mapstructure:"wip_enabled"   yaml:"wip_enabled"`
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
	// Density controls how much information `gk status` packs into the
	// terminal. "normal" (default) keeps the legacy compact output;
	// "rich" wraps the branch + working-tree + next-action sections in
	// square boxes, enables the divergence diagram and 7-day activity
	// sparkline, and removes the 24h gate on the "last commit" tag so
	// the SHA + age are always shown. The CLI flag `-v` / `--verbose`
	// (count ≥ 1) escalates to rich for a single invocation regardless
	// of the config value.
	Density string `mapstructure:"density" yaml:"density"`
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

// SyncConfig controls gk sync behaviour. Strategy is the integration
// mode used when the current branch has diverged from its base:
// "rebase" (default), "merge", or "ff-only" (refuse divergence).
// Kept separate from PullConfig.Strategy because sync (catch-up to base)
// and pull (sync with @{u}) have different intents — a user may want to
// merge-pull from collaborators on the same branch but rebase-sync onto
// main, or vice versa.
type SyncConfig struct {
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

// DefaultDenyPaths returns the baked-in deny_paths used when the user
// has none configured. Globs are matched with filepath.Match against
// both basename and full path; entries containing "/" require the path
// shape, bare entries match anywhere.
//
// Categories:
//   - secrets: .env, SSH keys, TLS material, cloud credentials
//   - infra state: terraform state, ansible/hashicorp vault
//   - data: sqlite DBs, raw dumps (often contain PII)
//   - compiled artifacts: __pycache__, *.class — useless for commit
//     prose and bloat token usage
func DefaultDenyPaths() []string {
	return []string{
		// secrets / credentials
		".env", ".env.*",
		"*.pem", "*.key", "*.crt", "*.cer",
		"*.p12", "*.pfx", "*.jks", "*.keystore", "*.kdbx",
		"id_rsa*", "id_dsa*", "id_ecdsa*", "id_ed25519*", "*.ppk",
		"credentials.json",
		"service-account*.json",
		"firebase-adminsdk*.json",
		"gcp-*-key*.json",

		// auth / config files
		".netrc", "_netrc",
		".npmrc", ".pypirc",
		".docker/config.json",
		".git-credentials",
		".aws/credentials", ".aws/config",
		".kube/config", "kubeconfig", "*.kubeconfig",

		// infra state
		"*.tfstate", "*.tfstate.*",
		"terraform.tfvars", "*.auto.tfvars",
		".vault_pass*", "vault-token*",
		"vault.yml", "vault.yaml", "*-vault.yml", "*-vault.yaml",
		"secrets.dec.yaml", "secrets.dec.yml",

		// data with potential PII
		"*.sqlite", "*.sqlite3", "*.db",
		"*.dump", "dump.sql",

		// compiled artifacts (token waste)
		"*.pyc", "*.pyo",
		"__pycache__", "__pycache__/*",
		"*.class",
	}
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
			//
			// `base` is on by default so users see how their branch sits
			// against its base branch (`from main ↑3 ↓0 → ready to merge
			// into main`) — the signal most often used to decide whether
			// to merge or sync. Cost is one local rev-list (~10ms); no
			// network. Hidden automatically on detached HEAD or when the
			// branch IS the base.
			Vis:       []string{"gauge", "progress", "base", "tree", "staleness"},
			AutoFetch: false,
			XYStyle:   "labels",
			Density:   "normal",
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
			// Empty by default. resolveIntegrationStrategy treats an unset
			// value as "no explicit user choice" and falls through to the
			// rebase default with source="default" — which gk pull uses to
			// decide whether to refuse on diverged history. Pre-filling
			// "rebase" here would mask that signal and silently auto-rebase.
			Strategy: "",
		},
		Sync: SyncConfig{
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
			Assist: AIAssistConfig{
				Mode:        "off",
				Status:      true,
				IncludeDiff: false,
			},
			Chat: AIChatConfig{
				Timeout:       "30s",
				MaxTokens:     4096,
				SafetyConfirm: true,
			},
			Commit: AICommitConfig{
				Mode:        "interactive",
				MaxGroups:   10,
				MaxTokens:   24000,
				Timeout:     "30s",
				DenyPaths:   DefaultDenyPaths(),
				AllowRemote: true,
				Trailer:     false,
				Audit:       false,
				WIPMaxChain: 10,
				WIPEnabled:  true,
			},
			Nvidia: AINvidiaConfig{
				Timeout: "60s",
			},
			Groq: AIGroqConfig{
				Timeout: "60s",
			},
		},
		Output: OutputConfig{
			Easy:  false,
			Lang:  "ko",
			Emoji: true,
			Hints: "verbose",
		},
	}
}
