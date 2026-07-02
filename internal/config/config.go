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
	Forget     ForgetConfig    `mapstructure:"forget"      yaml:"forget"`
	Sync       SyncConfig      `mapstructure:"sync"        yaml:"sync"`
	Refresh    RefreshConfig   `mapstructure:"refresh"     yaml:"refresh"`
	Preflight  PreflightConfig `mapstructure:"preflight"   yaml:"preflight"`
	Ship       ShipConfig      `mapstructure:"ship"        yaml:"ship"`
	Land       LandConfig      `mapstructure:"land"        yaml:"land"`
	Promote    PromoteConfig   `mapstructure:"promote"     yaml:"promote"`
	Clone      CloneConfig     `mapstructure:"clone"       yaml:"clone"`
	Worktree   WorktreeConfig  `mapstructure:"worktree"    yaml:"worktree"`
	AI         AIConfig        `mapstructure:"ai"          yaml:"ai"`
	Output     OutputConfig    `mapstructure:"output"      yaml:"output"`
	Fleet      FleetConfig     `mapstructure:"fleet"       yaml:"fleet"`
}

// FleetConfig configures multi-repo `gk fleet`. Repos lists explicit repo paths;
// Scan lists directory roots searched (Depth levels deep) for git repos; Exclude
// are directory-name globs skipped while scanning; Interval is the TUI poll
// interval in seconds. With every field empty, fleet stays single-repo unless
// --repos/--scan/--all is passed — config alone never auto-switches a bare
// `gk fleet` run from inside a repo.
type FleetConfig struct {
	Repos    []string `mapstructure:"repos"    yaml:"repos,omitempty"`
	Scan     []string `mapstructure:"scan"     yaml:"scan,omitempty"`
	Depth    int      `mapstructure:"depth"    yaml:"depth,omitempty"`
	Exclude  []string `mapstructure:"exclude"  yaml:"exclude,omitempty"`
	Interval int      `mapstructure:"interval" yaml:"interval,omitempty"`
	// FeedStats opts the change feed into +/- line counts (extra `git diff
	// --numstat` calls per dirty worktree per poll). Same as --feed-stats.
	FeedStats bool `mapstructure:"feed_stats" yaml:"feed_stats,omitempty"`
	// Notify maps a fleet transition to a shell command (`sh -c`), run with
	// GK_FLEET_* context env. Keys: conflict, paused, land_ready. Opt-in.
	Notify map[string]string `mapstructure:"notify" yaml:"notify,omitempty"`
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
	// Providers registers custom, user-named providers keyed by the name
	// used in `provider:`. A name absent from the built-in whitelist
	// (anthropic/openai/nvidia/groq/gemini/qwen/kiro) is looked up here and
	// built from the named entry's Format wire protocol. Lets a user point
	// `provider: kiro-api` (or any name) at an OpenAI-compatible gateway
	// without colliding with the real `openai` section.
	Providers map[string]AICustomProviderConfig `mapstructure:"providers" yaml:"providers"`
	// Extra captures any `ai.<name>` block that is not a known field above
	// (",remain" collects the leftover keys). It lets a custom provider be
	// written one level shallower — `ai.kiro-api:` instead of
	// `ai.providers.kiro-api:` — which reads naturally next to the built-in
	// `ai.openai:` / `ai.groq:` blocks. customProvider() consults Providers
	// first, then Extra.
	Extra map[string]AICustomProviderConfig `mapstructure:",remain" yaml:"-"`
}

// CustomProvider resolves a user-named provider by the name used in
// `provider:`. It prefers an explicit `ai.providers.<name>` entry, then
// falls back to a shallow `ai.<name>` block captured in Extra. The bool
// reports whether a registration was found.
func (a AIConfig) CustomProvider(name string) (AICustomProviderConfig, bool) {
	if c, ok := a.Providers[name]; ok {
		return c, true
	}
	if c, ok := a.Extra[name]; ok {
		return c, true
	}
	return AICustomProviderConfig{}, false
}

// AICustomProviderConfig defines a user-named provider built on top of an
// existing HTTP wire protocol. Format selects which built-in adapter speaks
// the protocol ("openai" | "anthropic" | "nvidia" | "groq"); empty defaults
// to "openai". The remaining fields override that adapter's defaults exactly
// like the dedicated provider sections do. APIKey, when set, takes precedence
// over the underlying adapter's env var (see AIAnthropicConfig for the leak
// caveat).
type AICustomProviderConfig struct {
	Format   string `mapstructure:"format"   yaml:"format"`
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
	APIKey   string `mapstructure:"api_key"  yaml:"api_key"`
}

// AIAssistConfig controls AI help that is attached to existing commands.
// Mode accepts:
//   - "off": never attach AI help unless a CLI flag explicitly asks for it.
//   - "suggest": print a lightweight hint pointing to `gk next`.
//   - "auto": run the configured AI assistant automatically for enabled
//     surfaces such as `gk status`.
//
// Status gates the `gk status` surface.
//
// IncludeDiff adds the working-tree diff (truncated to DiffBudget bytes and
// run through the privacy gate) to the prompt, so the assistant can reason
// about *what* changed rather than only file counts. Off by default — it
// raises token cost and widens the data sent to remote providers.
//
// DiffBudget caps the diff size in bytes when IncludeDiff is on (default
// 8000). MaxTokens is the advisory response cap (default 1200). TimeoutSecs
// bounds a single status AI call so `gk status` never hangs on a slow
// provider (default 8). Cache stores the result keyed by the repo state so
// repeated/auto invocations on an unchanged tree skip the provider entirely
// (default true).
type AIAssistConfig struct {
	Mode        string `mapstructure:"mode"         yaml:"mode"`
	Status      bool   `mapstructure:"status"       yaml:"status"`
	IncludeDiff bool   `mapstructure:"include_diff" yaml:"include_diff"`
	DiffBudget  int    `mapstructure:"diff_budget"  yaml:"diff_budget"`
	MaxTokens   int    `mapstructure:"max_tokens"   yaml:"max_tokens"`
	TimeoutSecs int    `mapstructure:"timeout_secs" yaml:"timeout_secs"`
	Cache       bool   `mapstructure:"cache"        yaml:"cache"`
}

// AIChatConfig controls the AI chat subcommands (`gk do`, `gk explain`,
// `gk ask`). Timeout is a Go duration string for AI provider calls
// (default "30s"). MaxTokens caps the response token budget (default
// 4096).
//
// Dangerous `gk do` commands always require an extra confirmation (unless
// --force); that gate is not configurable. A former `safety_confirm` field
// implied it could be turned off but never actually did, so it was removed.
type AIChatConfig struct {
	Timeout   string `mapstructure:"timeout"    yaml:"timeout"`
	MaxTokens int    `mapstructure:"max_tokens" yaml:"max_tokens"`
}

// AIAnthropicConfig controls the Claude provider. Empty fields fall
// back to the adapter defaults.
//
// APIKey, when set, takes precedence over the ANTHROPIC_API_KEY env var.
// Prefer the env var for shared/committed configs — a key written here
// lands in a plaintext yaml that is easy to leak.
type AIAnthropicConfig struct {
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
	APIKey   string `mapstructure:"api_key"  yaml:"api_key"`
}

// AIOpenAIConfig controls the OpenAI provider. APIKey, when set, takes
// precedence over the OPENAI_API_KEY env var (see AIAnthropicConfig for
// the leak caveat).
type AIOpenAIConfig struct {
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
	APIKey   string `mapstructure:"api_key"  yaml:"api_key"`
}

// AINvidiaConfig controls the NVIDIA AI provider. Model overrides the
// default LLM model identifier; Endpoint overrides the default Chat
// Completions API URL; Timeout is a Go duration string for HTTP requests.
// APIKey, when set, takes precedence over the NVIDIA_API_KEY env var.
type AINvidiaConfig struct {
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
	APIKey   string `mapstructure:"api_key"  yaml:"api_key"`
}

// AIGroqConfig controls the Groq AI provider. APIKey, when set, takes
// precedence over the GROQ_API_KEY env var.
type AIGroqConfig struct {
	Model    string `mapstructure:"model"    yaml:"model"`
	Endpoint string `mapstructure:"endpoint" yaml:"endpoint"`
	Timeout  string `mapstructure:"timeout"  yaml:"timeout"`
	APIKey   string `mapstructure:"api_key"  yaml:"api_key"`
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
	Mode      string `mapstructure:"mode"          yaml:"mode"`
	MaxGroups int    `mapstructure:"max_groups"    yaml:"max_groups"`
	MaxTokens int    `mapstructure:"max_tokens"    yaml:"max_tokens"`
	// Model overrides ai.<provider>.model for `gk commit` only. Commit
	// message generation is a mechanical task that a small, fast model
	// handles well, so this lets a cheaper model run commits while the
	// chat/advice commands (do/ask/explain/status) keep the larger default
	// model. Empty falls back to the provider's configured model. Honoured
	// only by HTTP providers (anthropic/openai/nvidia/groq and custom);
	// CLI providers (gemini/qwen/kiro) own their own model selection.
	Model string `mapstructure:"model"         yaml:"model"`
	// Lang overrides ai.lang for `gk commit` only — set it to write commit
	// messages in one language (e.g. "en") while keeping chat/advice commands
	// (do/ask/explain) in another. Empty falls back to ai.lang (which itself
	// follows output.lang). A one-shot `gk commit --lang <code>` still wins.
	Lang        string        `mapstructure:"lang"          yaml:"lang"`
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
	// Concurrency caps how many commit groups are composed in parallel.
	// Compose is the dominant latency in `gk commit` — each group is an
	// independent LLM round-trip — so groups fan out concurrently. 0
	// falls back to 4: enough to run a typical multi-group commit fully
	// in parallel while bounding burst pressure on provider rate limits
	// (e.g. Groq's free tier) and the number of CLI-provider subprocesses
	// (gemini/qwen/kiro) spawned at once. Lower it for strict quotas;
	// raise it on a paid tier.
	Concurrency int `mapstructure:"concurrency"   yaml:"concurrency"`
	// WarmCache, when true AND the provider caches its system prompt
	// (Anthropic), composes the first group synchronously to prime the
	// prompt cache before the rest fan out. Default FALSE: measurement
	// showed the warm-up serialises one extra round-trip (≈1.8× slower on
	// a cache-less provider), and the offsetting cache saving is unproven
	// for gk's small system prompt. Enable only after measuring a net win
	// on your Anthropic setup.
	WarmCache bool `mapstructure:"warm_cache"    yaml:"warm_cache"`
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
	// "rich" splits the branch + working-tree + next-action sections
	// into titled blocks, enables the divergence diagram and 7-day
	// activity sparkline, and removes the 24h gate on the "last commit"
	// tag so the SHA + age are always shown. The CLI flag `-v` /
	// `--verbose` (count ≥ 1) escalates to rich for a single invocation
	// regardless of the config value.
	Density string `mapstructure:"density" yaml:"density"`
	// Layout selects how rich-mode sections are framed. "bar" (default)
	// prefixes each title with a coloured ▎ bar; "rule" brackets the
	// title with horizontal rules. Both replace the legacy box layout
	// because the box's per-line padding misaligned with wide-character
	// content (한글/이모지/coloured glyphs). Ignored when density is
	// "normal".
	Layout string `mapstructure:"layout" yaml:"layout"`
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
	// ScanContext shows ±1 surrounding source line (masked) around each
	// secret-scan hit on push/ship, so a reviewer can judge a false positive
	// in place. The global --verbose / `gk push -v` flag turns it on for a
	// single invocation; this makes it the default.
	ScanContext bool `mapstructure:"scan_context" yaml:"scan_context"`
}

// ForgetConfig controls gk forget behaviour.
// Engine selects the history-rewrite implementation: "native" (default,
// gk's built-in fast-export→filter→fast-import pipeline, no external
// install) or "filter-repo" (delegate to git filter-repo). Equivalent to
// passing --engine on each invocation; the flag wins when both are set.
type ForgetConfig struct {
	Engine string `mapstructure:"engine" yaml:"engine"`
}

// PullConfig controls gk pull behaviour.
// Strategy accepts: "rebase" (default), "merge", "ff-only", "auto".
// "auto" reads git config pull.rebase; if unset, falls back to "rebase".
// WithBase makes every pull additionally fast-forward the local base
// branch (e.g. main) from its remote — no checkout involved, strictly
// FF-only, ambiguous states are skipped with a note. Equivalent to
// passing --with-base on each invocation.
// Autostash (default true) lets a dirty working tree pull without an
// interactive gate: tracked changes are stashed before integration and
// popped after, so the common no-conflict case flows through with only a
// status line. The pop is the one place a real conflict with local edits
// surfaces — and the one place pull still stops. Set false (or pass
// --no-autostash) to restore the old behaviour: prompt on a TTY, refuse on
// a non-TTY. GK_PULL_AUTOSTASH overrides per environment.
type PullConfig struct {
	Strategy  string `mapstructure:"strategy" yaml:"strategy"`
	WithBase  bool   `mapstructure:"with_base" yaml:"with_base"`
	Autostash bool   `mapstructure:"autostash" yaml:"autostash"`
}

// ShipConfig extends gk ship beyond the shared preflight steps with
// post-tag hooks and an explicit version-file list, so the whole
// release pipeline (checks → version → changelog → tag → push → CI
// watch → post-release verification) runs from one command on any
// project.
//
//   - Watch: commands run after the release tag is pushed — typically a
//     blocking CI watcher (e.g. `gh run watch ...`). A failure aborts
//     ship with a rerun hint; the tag is already published at that point.
//   - Verify: post-release checks run after every Watch step succeeds
//     (artifact reachable on the CDN, tap/registry bumped, ...).
//   - VersionFiles: explicit version-file list (paths relative to the repo
//     root). When set it replaces the VERSION/package.json/
//     marketplace.json auto-detection. Each entry is either a bare path
//     string (the format is inferred from the filename) or a {path,
//     pattern, key} object — see VersionFile.
//   - AutoConfirm: skip the final confirmation prompt by default, as if
//     every run passed -y. An explicit --yes=false still forces the
//     prompt for one invocation.
//   - Wait: run the post-tag Watch/Verify pipeline (default true).
//     false returns right after the push — the release is published but
//     untracked, and ship prints the skipped commands as a note.
//     --wait / --wait=false overrides per invocation.
//
// Watch and Verify reuse PreflightStep so `name:`/`command:`/
// `continue_on_failure:` read the same in all three lists.
type ShipConfig struct {
	Watch        []PreflightStep `mapstructure:"watch"         yaml:"watch"`
	Verify       []PreflightStep `mapstructure:"verify"        yaml:"verify"`
	VersionFiles []VersionFile   `mapstructure:"version_files" yaml:"version_files"`
	AutoConfirm  bool            `mapstructure:"auto_confirm"  yaml:"auto_confirm"`
	Wait         bool            `mapstructure:"wait"          yaml:"wait"`
}

// VersionFile is one entry of ship.version_files. It decodes from either a
// bare string (`- pyproject.toml`, handled by the stringToVersionFile decode
// hook in load.go) or a mapping (`- {path: ..., pattern: ...}`):
//
//   - Path: the file to rewrite, relative to the repo root. Required.
//   - Pattern: a literal template containing exactly one `{version}`
//     placeholder (e.g. `__version__ = "{version}"`). ship replaces the
//     text that sits where `{version}` does. Works on any text file — the
//     escape hatch for formats with no native handler.
//   - Key: a dotted key path into a YAML file (e.g. `version` or
//     `tool.poetry.version`). ship rewrites that scalar while preserving
//     comments and surrounding structure.
//
// Pattern and Key are mutually exclusive; when both are empty ship infers
// the format from the filename (VERSION, package.json, pyproject.toml,
// Cargo.toml, *.py __version__, pubspec.yaml / Chart.yaml).
type VersionFile struct {
	Path    string `mapstructure:"path"    yaml:"path"`
	Pattern string `mapstructure:"pattern" yaml:"pattern,omitempty"`
	Key     string `mapstructure:"key"     yaml:"key,omitempty"`
}

// LandConfig controls gk land. Promote turns the promote step on by
// default, as if every invocation passed the flag:
//
//   - "" (default): promote only when --promote is passed.
//   - "parent": bare --promote semantics — one hop to the branch's
//     gk-parent, falling back to the configured base. YAML booleans are
//     tolerated ("true"/"1" read as "parent", "false"/"0"/"none"/"off"
//     as off) so `promote: true` does the intuitive thing.
//   - any branch name: same as --promote=<branch> — walk the parent
//     chain hop by hop until that branch.
//
// An explicit --promote flag always wins over config; --no-promote skips
// the step for one invocation.
//
// Autostash (default false) makes the --to / promote merge step stash a
// dirty receiver worktree (the parent checkout someone left mid-edit)
// around the merge and pop it after, as if every invocation passed
// --autostash. An explicit --autostash / --autostash=false flag wins.
type LandConfig struct {
	Promote   string `mapstructure:"promote"   yaml:"promote"`
	Autostash bool   `mapstructure:"autostash" yaml:"autostash"`
}

// PromoteConfig controls gk promote. Autostash (default false) makes every
// hop's merge stash a dirty receiver worktree around the merge and pop it
// after, as if --autostash were passed — for worktree flows where the
// parent checkout is routinely dirty. An explicit --autostash /
// --autostash=false flag overrides it for one run.
type PromoteConfig struct {
	Autostash bool `mapstructure:"autostash" yaml:"autostash"`
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

// RefreshConfig controls `gk refresh`. Tracked lists the long-lived
// branches that `gk refresh` fast-forwards to their remote counterparts
// (origin/<branch>). It never rebases or merges across branches, so it is
// safe on shared branches: a diverged branch is skipped, not rewritten.
//
// Empty (the default) means gk resolves the list dynamically per repo:
// the canonical main branch (origin/HEAD → main → master) plus develop/dev
// when they exist locally. Set an explicit list to override — e.g. to add
// a long-lived release branch, or to refresh master only.
type RefreshConfig struct {
	Tracked []string `mapstructure:"tracked" yaml:"tracked"`
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
//   - Hosts: alias table for multi-host / multi-account users. `gk clone
//     gl:group/repo` looks up `gl` here, using the per-alias host +
//     protocol (falling back to DefaultProtocol when the alias omits it).
//     An alias with `owner` set doubles as an account profile: `gk clone
//     gl:repo` completes the owner, and `gk init` offers the alias when
//     wiring the origin remote. Layers merge field-by-field (viper deep
//     merge), so a repo-local `.gk.yaml` that sets only `protocol` on an
//     alias inherits the global entry's host/owner.
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
//
// Owner turns the alias into an account profile: `alias:repo` (no slash)
// expands to `owner/repo`, and `gk init` lists the alias as a remote
// candidate. SSHHost swaps a `~/.ssh/config` Host alias (e.g.
// `github.com-work`, carrying the right key for a second account on the
// same host) into ssh URLs; https URLs and structured metadata keep the
// canonical Host. Both fields are optional — aliases without them behave
// exactly as before.
type HostAlias struct {
	Host     string `mapstructure:"host"     yaml:"host"`
	Protocol string `mapstructure:"protocol" yaml:"protocol"`
	Owner    string `mapstructure:"owner"    yaml:"owner,omitempty"`
	SSHHost  string `mapstructure:"ssh_host" yaml:"ssh_host,omitempty"`
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
	Base    string        `mapstructure:"base"    yaml:"base"`
	Project string        `mapstructure:"project" yaml:"project"`
	Init    *WorktreeInit `mapstructure:"init"    yaml:"init,omitempty"`
}

// WorktreeInit declares how a freshly created worktree is bootstrapped so
// gitignored, per-checkout state (secrets, dependency trees, virtualenvs)
// is reconstituted rather than left empty. The three keys map to three
// fundamentally different resource types — conflating them is the usual
// mistake:
//
//   - Link: symlink a file/dir from the main worktree. Right for secrets
//     and shared config (.env) you want managed in ONE place and kept in
//     sync across every worktree. NOT for virtualenvs (absolute paths
//     baked into pyvenv.cfg/shebangs break) or node_modules (branches may
//     pin different lockfiles → cross-contamination).
//   - Copy: copy a file/dir from the main worktree. Use when each worktree
//     needs an independently editable copy (e.g. a .env whose port differs
//     per checkout). Same anti-patterns as Link for venv/node_modules.
//   - Run: shell commands executed IN the new worktree, in order. The
//     correct home for `npm ci`, `uv sync`, `python -m venv` — anything
//     that must be regenerated against this checkout's lockfile for true
//     isolation.
//
// All three are idempotent on re-run via `gk worktree init`: existing
// correct symlinks are left alone, present copy targets are skipped, and
// install commands (npm ci / uv sync) are safe to repeat.
type WorktreeInit struct {
	Link []string `mapstructure:"link" yaml:"link,omitempty"`
	Copy []string `mapstructure:"copy" yaml:"copy,omitempty"`
	Run  []string `mapstructure:"run"  yaml:"run,omitempty"`
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
			// `local` (working-tree change badge) and `since-push` (unpushed
			// age+count, with --remotes fallback for no-upstream branches) are
			// on by default so the BRANCH line surfaces all local-change
			// layers at a glance — uncommitted (local), staged, and unpushed —
			// without the user knowing any flag. Both render nothing when there
			// is nothing to show, so a clean synced repo stays quiet.
			Vis:       []string{"gauge", "bar", "progress", "base", "tree", "staleness", "local", "since-push"},
			AutoFetch: false,
			XYStyle:   "labels",
			Density:   "normal",
			Layout:    "bar",
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
			ScanContext:    false,
		},
		Pull: PullConfig{
			// Empty by default. resolveIntegrationStrategy treats an unset
			// value as "no explicit user choice" and falls through to the
			// rebase default with source="default" — which gk pull uses to
			// decide whether to refuse on diverged history. Pre-filling
			// "rebase" here would mask that signal and silently auto-rebase.
			Strategy: "",
			// Auto-stash a dirty tree by default — the interactive gate
			// stopped pull before knowing whether stash→integrate→pop would
			// even conflict, which it almost never does.
			Autostash: true,
		},
		Forget: ForgetConfig{
			Engine: "native",
		},
		Sync: SyncConfig{
			Strategy: "rebase",
		},
		Refresh: RefreshConfig{
			// nil → resolve dynamically (main/master + develop/dev).
			Tracked: nil,
		},
		Preflight: PreflightConfig{
			Steps: []PreflightStep{
				{Name: "commit-lint", Command: "commit-lint"},
				{Name: "branch-check", Command: "branch-check"},
				{Name: "no-conflict", Command: "no-conflict"},
			},
		},
		Ship: ShipConfig{
			// Wait defaults to true: shipping means seeing the release
			// through CI watch + verify. Load pre-seeds the struct with
			// these defaults, so a `ship:` section that omits the key
			// keeps it on.
			Wait: true,
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
			// Lang empty means "follow output.lang" (resolved in Load). An
			// explicit ai.lang in config/env still wins. fallbackLang() turns a
			// still-empty value into "en" at the call site.
			Lang: "",
			Assist: AIAssistConfig{
				Mode:        "off",
				Status:      true,
				IncludeDiff: false,
				DiffBudget:  8000,
				MaxTokens:   1200,
				TimeoutSecs: 8,
				Cache:       true,
			},
			Chat: AIChatConfig{
				Timeout:   "30s",
				MaxTokens: 4096,
			},
			Commit: AICommitConfig{
				Mode:        "interactive",
				MaxGroups:   10,
				MaxTokens:   24000,
				Timeout:     "120s",
				DenyPaths:   DefaultDenyPaths(),
				AllowRemote: true,
				Trailer:     false,
				Audit:       false,
				WIPMaxChain: 10,
				WIPEnabled:  true,
				Concurrency: 4,
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
