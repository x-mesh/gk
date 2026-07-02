package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// stringToVersionFileHookFunc lets ship.version_files mix bare path strings
// (`- pyproject.toml`) and {path, pattern, key} mappings in the same list:
// mapstructure cannot decode a scalar into a struct on its own, so this hook
// promotes a string into VersionFile{Path: s} before the struct decode runs.
func stringToVersionFileHookFunc() mapstructure.DecodeHookFunc {
	versionFileType := reflect.TypeOf(VersionFile{})
	return func(from reflect.Type, to reflect.Type, data any) (any, error) {
		if from.Kind() != reflect.String || to != versionFileType {
			return data, nil
		}
		return VersionFile{Path: data.(string)}, nil
	}
}

// Broken-config warnings are deduped per file per process: Load runs several
// times per invocation (command body, EasyEngine, …) and the user needs the
// diagnosis once, not once per layer read.
var (
	configWarnMu     sync.Mutex
	configWarnSeen             = map[string]bool{}
	configWarnWriter io.Writer = os.Stderr
)

// warnBrokenConfig surfaces a config-layer failure to the user and lets Load
// continue with the remaining layers. The old behavior — aborting Load with a
// nil *Config — both hid the mistake (most call sites read `cfg, _ :=`) and
// crashed the ones that dereferenced the nil. yaml's own error text already
// names duplicate keys and line numbers, so it is passed through verbatim.
func warnBrokenConfig(source string, err error) {
	configWarnMu.Lock()
	defer configWarnMu.Unlock()
	if configWarnSeen[source] {
		return
	}
	configWarnSeen[source] = true
	fmt.Fprintf(configWarnWriter,
		"gk: config error in %s: %v\n    this layer is ignored — fix it, then verify with `gk config doctor`\n",
		source, err)
}

// SetConfigWarnWriter redirects broken-config warnings (default os.Stderr)
// and clears the dedupe set; returns a restore func. Test hook.
func SetConfigWarnWriter(w io.Writer) func() {
	configWarnMu.Lock()
	defer configWarnMu.Unlock()
	prev := configWarnWriter
	configWarnWriter = w
	configWarnSeen = map[string]bool{}
	return func() {
		configWarnMu.Lock()
		defer configWarnMu.Unlock()
		configWarnWriter = prev
		configWarnSeen = map[string]bool{}
	}
}

// reservedConfigSections names the top-level Config keys that decode into
// a struct/map. A CLI flag can never represent a whole section, so flags
// whose name matches one of these are excluded from pflag→viper binding
// (see step 6 in Load). The scalar top-level keys (base_branch, remote)
// are intentionally absent — those remain bindable for genuine overrides.
var reservedConfigSections = map[string]bool{
	"log":       true,
	"status":    true,
	"ui":        true,
	"branch":    true,
	"commit":    true,
	"push":      true,
	"pull":      true,
	"ship":      true,
	"land":      true,
	"promote":   true,
	"sync":      true,
	"refresh":   true,
	"preflight": true,
	"clone":     true,
	"worktree":  true,
	"ai":        true,
	"output":    true,
	"fleet":     true,
	"lang":      true,
}

// Load resolves the full configuration using the layered priority:
//
//  1. Defaults (lowest)
//  2. $XDG_CONFIG_HOME/gk/config.yaml  (default: ~/.config/gk/config.yaml)
//  3. .gk.yaml in the git working-tree root
//  4. git config --get-regexp '^gk\.' entries (merged as map)
//  5. GK_* environment variables
//  6. CLI flags via pflag.FlagSet (highest)
//
// flags may be nil; in that case flag binding is skipped.
func Load(flags *pflag.FlagSet) (*Config, error) {
	v := viper.New()

	// --- 1. Defaults ---
	defaults := Defaults()
	v.SetDefault("base_branch", defaults.BaseBranch)
	v.SetDefault("remote", defaults.Remote)
	v.SetDefault("log.format", defaults.Log.Format)
	v.SetDefault("log.graph", defaults.Log.Graph)
	v.SetDefault("log.limit", defaults.Log.Limit)
	v.SetDefault("log.vis", defaults.Log.Vis)
	v.SetDefault("status.vis", defaults.Status.Vis)
	v.SetDefault("status.auto_fetch", defaults.Status.AutoFetch)
	v.SetDefault("status.xy_style", defaults.Status.XYStyle)
	v.SetDefault("status.density", defaults.Status.Density)
	v.SetDefault("ui.color", defaults.UI.Color)
	v.SetDefault("ui.prefer", defaults.UI.Prefer)
	v.SetDefault("branch.stale_days", defaults.Branch.StaleDays)
	v.SetDefault("branch.protected", defaults.Branch.Protected)
	v.SetDefault("clone.default_protocol", defaults.Clone.DefaultProtocol)
	v.SetDefault("clone.default_host", defaults.Clone.DefaultHost)
	v.SetDefault("worktree.base", defaults.Worktree.Base)
	v.SetDefault("ai.enabled", defaults.AI.Enabled)
	v.SetDefault("ai.provider", defaults.AI.Provider)
	// ai.lang is intentionally NOT given a viper default: that would make
	// viper.IsSet("ai.lang") always true and defeat the "follow output.lang
	// when unset" fallback below. The struct default (Defaults().AI.Lang)
	// still applies before that fallback runs.
	v.SetDefault("ai.assist.mode", defaults.AI.Assist.Mode)
	v.SetDefault("ai.assist.status", defaults.AI.Assist.Status)
	v.SetDefault("ai.assist.include_diff", defaults.AI.Assist.IncludeDiff)
	v.SetDefault("ai.assist.diff_budget", defaults.AI.Assist.DiffBudget)
	v.SetDefault("ai.assist.max_tokens", defaults.AI.Assist.MaxTokens)
	v.SetDefault("ai.assist.timeout_secs", defaults.AI.Assist.TimeoutSecs)
	v.SetDefault("ai.assist.cache", defaults.AI.Assist.Cache)
	v.SetDefault("ai.commit.mode", defaults.AI.Commit.Mode)
	v.SetDefault("ai.commit.max_groups", defaults.AI.Commit.MaxGroups)
	v.SetDefault("ai.commit.max_tokens", defaults.AI.Commit.MaxTokens)
	v.SetDefault("ai.commit.timeout", defaults.AI.Commit.Timeout)
	v.SetDefault("ai.commit.deny_paths", defaults.AI.Commit.DenyPaths)
	v.SetDefault("ai.commit.allow_remote", defaults.AI.Commit.AllowRemote)
	v.SetDefault("ai.commit.trailer", defaults.AI.Commit.Trailer)
	v.SetDefault("ai.commit.audit", defaults.AI.Commit.Audit)
	v.SetDefault("ai.commit.wip_max_chain", defaults.AI.Commit.WIPMaxChain)
	v.SetDefault("ai.commit.wip_enabled", defaults.AI.Commit.WIPEnabled)
	v.SetDefault("ai.commit.concurrency", defaults.AI.Commit.Concurrency)
	v.SetDefault("ai.commit.warm_cache", defaults.AI.Commit.WarmCache)
	v.SetDefault("ai.nvidia.model", defaults.AI.Nvidia.Model)
	v.SetDefault("ai.nvidia.endpoint", defaults.AI.Nvidia.Endpoint)
	v.SetDefault("ai.nvidia.timeout", defaults.AI.Nvidia.Timeout)
	v.SetDefault("output.easy", defaults.Output.Easy)
	v.SetDefault("output.lang", defaults.Output.Lang)
	v.SetDefault("output.emoji", defaults.Output.Emoji)
	v.SetDefault("output.hints", defaults.Output.Hints)
	v.SetDefault("ship.auto_confirm", defaults.Ship.AutoConfirm)
	v.SetDefault("ship.wait", defaults.Ship.Wait)
	v.SetDefault("land.promote", defaults.Land.Promote)
	// Registering the key (rather than relying on the struct default alone)
	// lets AutomaticEnv + the "."→"_" replacer resolve GK_PULL_AUTOSTASH,
	// the same mechanism that carries GK_LAND_PROMOTE / GK_SHIP_WAIT.
	v.SetDefault("pull.autostash", defaults.Pull.Autostash)
	// Same registration for promote.autostash / land.autostash → enables
	// GK_PROMOTE_AUTOSTASH / GK_LAND_AUTOSTASH.
	v.SetDefault("promote.autostash", defaults.Promote.Autostash)
	v.SetDefault("land.autostash", defaults.Land.Autostash)

	// --- 2. Global config: $XDG_CONFIG_HOME/gk/config.yaml ---
	globalDir := xdgConfigDir()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(globalDir)
	// ReadInConfig returns an error when the file does not exist; ignore that.
	// A real parse error (duplicate keys, bad indentation, …) must not abort
	// the whole Load: warn once and continue with the remaining layers, so
	// every command keeps working on defaults instead of panicking on the
	// nil Config the old early-return produced.
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			if !isNoSuchFile(err) {
				warnBrokenConfig(filepath.Join(globalDir, "config.yaml"), err)
			}
		}
	}

	// --- 3. Repo-local .gk.yaml ---
	// An explicit --repo flag points config discovery at that repo's
	// working-tree root; without it we fall back to the cwd-derived root.
	// Otherwise `gk --repo /other <cmd>` would read the cwd's .gk.yaml
	// (or none) instead of /other's, so its repo-local overrides silently
	// wouldn't apply.
	repoRoot := ""
	if flags != nil {
		if rf, ferr := flags.GetString("repo"); ferr == nil && rf != "" {
			repoRoot, _ = gitRepoRootIn(rf)
		}
	}
	if repoRoot == "" {
		repoRoot, _ = cachedGitRepoRoot()
	}
	if repoRoot != "" {
		localCfg := filepath.Join(repoRoot, ".gk.yaml")
		if _, statErr := os.Stat(localCfg); statErr == nil {
			v2 := viper.New()
			v2.SetConfigFile(localCfg)
			if err2 := v2.ReadInConfig(); err2 == nil {
				if mergeErr := v.MergeConfigMap(v2.AllSettings()); mergeErr != nil {
					warnBrokenConfig(localCfg, mergeErr)
				}
			} else {
				// Previously a broken .gk.yaml was skipped in silence —
				// the user had no way to learn their repo config wasn't
				// applying. Same degrade-and-warn policy as the global file.
				warnBrokenConfig(localCfg, err2)
			}
		}
	}

	// --- 4. git config gk.* ---
	gkMap, err := cachedReadGK()
	if err == nil && len(gkMap) > 0 {
		if mergeErr := v.MergeConfigMap(gkMap); mergeErr != nil {
			warnBrokenConfig("git config gk.*", mergeErr)
		}
	}

	// --- 5. Environment variables: GK_* ---
	v.SetEnvPrefix("GK")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Explicit bindings for output.* fields so that the short env var
	// names GK_EASY, GK_LANG, GK_EMOJI, GK_HINTS work in addition to
	// the automatic GK_OUTPUT_EASY, GK_OUTPUT_LANG, etc.
	_ = v.BindEnv("output.easy", "GK_EASY")
	_ = v.BindEnv("output.lang", "GK_LANG")
	_ = v.BindEnv("output.emoji", "GK_EMOJI")
	_ = v.BindEnv("output.hints", "GK_HINTS")

	// land.promote needs no explicit BindEnv: the SetDefault above registers the
	// key, so AutomaticEnv + the "."→"_" replacer resolve GK_LAND_PROMOTE on
	// their own — the same mechanism that carries GK_SHIP_WAIT / GK_SHIP_AUTO_CONFIRM.
	// (The output.* binds below exist only for their short aliases like GK_EASY,
	// which AutomaticEnv cannot derive from the key name.)

	// --- 6. CLI flags ---
	// BindPFlags maps each flag to a viper key by its bare name. A command
	// flag whose name collides with a top-level config *section* (e.g.
	// `gk resolve --ai` vs the `ai:` struct) would otherwise overwrite the
	// whole section with the flag's scalar value, and Unmarshal would fail
	// with "'ai' expected a map or struct, got bool". Such flags are local
	// switches read via cmd.Flags(), never config overrides, so skip them.
	if flags != nil {
		var bindErr error
		flags.VisitAll(func(f *pflag.Flag) {
			if bindErr != nil || reservedConfigSections[f.Name] {
				return
			}
			bindErr = v.BindPFlag(f.Name, f)
		})
		if bindErr != nil {
			fallback := Defaults()
			return &fallback, bindErr
		}
	}

	// Unmarshal into Config struct. On failure (e.g. a scalar where a
	// section is expected) degrade to defaults rather than returning nil —
	// `cfg, _ := config.Load(...)` call sites dereference the result.
	cfg := Defaults()
	// Override viper's default decode hook so ship.version_files accepts both
	// string and mapping entries. The two viper defaults
	// (StringToTimeDurationHookFunc, StringToSliceHookFunc) must be re-composed
	// here — passing DecodeHook replaces them wholesale — or duration strings
	// and comma-separated env slices would stop decoding.
	decodeHook := viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		stringToVersionFileHookFunc(),
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	))
	if err := v.Unmarshal(&cfg, decodeHook); err != nil {
		warnBrokenConfig("config", err)
		fallback := Defaults()
		if fallback.AI.Lang == "" {
			fallback.AI.Lang = fallback.Output.Lang
		}
		return &fallback, err
	}

	// When ai.lang is not explicitly configured, follow output.lang so that
	// AI responses match the rest of the CLI's language (e.g. Easy Mode with
	// output.lang=ko gives Korean `gk ask`/`do`/`explain`/`status --ai`
	// answers). An explicit ai.lang still wins; output.lang itself defaults
	// to the catalogue language.
	if cfg.AI.Lang == "" {
		cfg.AI.Lang = cfg.Output.Lang
	}

	// Reconstruct the clone.hosts declaration order the map decode above
	// discarded — global file first, repo-local additions after — so
	// pickers can present profiles the way the user wrote them.
	orderPaths := []string{filepath.Join(globalDir, "config.yaml")}
	if repoRoot != "" {
		orderPaths = append(orderPaths, filepath.Join(repoRoot, ".gk.yaml"))
	}
	cfg.Clone.HostsOrder = cloneHostsOrder(orderPaths...)

	return &cfg, nil
}

// xdgConfigDir returns the XDG config directory for gk.
func xdgConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gk")
}

// gitRepoRoot returns the working-tree root of the current git repository
// (cwd). Returns empty string (nil error) when not in a git repo.
func gitRepoRoot() (string, error) { return gitRepoRootIn("") }

// gitRepoRootIn returns the working-tree root of the git repository at dir
// (via `git -C dir`), or of the cwd when dir is empty. Returns empty string
// (nil error) when dir is not in a git repo — config discovery then skips
// the repo-local layer rather than failing.
func gitRepoRootIn(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := make([]string, 0, 4)
	if dir != "" {
		args = append(args, "-C", dir)
	}
	args = append(args, "rev-parse", "--show-toplevel")
	out, err := cmdOutput(ctx, "git", args...)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// Load caches its two git-fork helpers (repo root + gk.* config) keyed
// on the process's current working directory. A gk command process
// never chdir's mid-run, so subsequent Load() calls from the same
// command share the work. Tests that chdir between scenarios still
// see fresh reads because the cwd key invalidates automatically — no
// explicit reset hook needed.
var (
	loadCacheMu      sync.Mutex
	loadCacheWd      string
	cachedRepoRoot   string
	cachedRepoErr    error
	cachedRepoRootOK bool
	cachedGKMap      map[string]any
	cachedGKErr      error
	cachedGKOK       bool
)

// loadCacheKey returns the cwd used as the cache key. We re-read it on
// every Load call; getwd is a single syscall (~µs) compared to the
// git fork it replaces (~ms).
func loadCacheKey() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// invalidateLoadCacheIfMoved drops cached state when cwd changes.
// Called with the mutex held.
func invalidateLoadCacheIfMoved(wd string) {
	if wd != loadCacheWd {
		loadCacheWd = wd
		cachedRepoRootOK = false
		cachedGKOK = false
	}
}

func cachedGitRepoRoot() (string, error) {
	wd := loadCacheKey()
	loadCacheMu.Lock()
	defer loadCacheMu.Unlock()
	invalidateLoadCacheIfMoved(wd)
	if cachedRepoRootOK {
		return cachedRepoRoot, cachedRepoErr
	}
	cachedRepoRoot, cachedRepoErr = gitRepoRoot()
	cachedRepoRootOK = true
	return cachedRepoRoot, cachedRepoErr
}

func cachedReadGK() (map[string]any, error) {
	wd := loadCacheKey()
	loadCacheMu.Lock()
	defer loadCacheMu.Unlock()
	invalidateLoadCacheIfMoved(wd)
	if cachedGKOK {
		return cloneNestedMap(cachedGKMap), cachedGKErr
	}
	cachedGKMap, cachedGKErr = ReadGK()
	cachedGKOK = true
	return cloneNestedMap(cachedGKMap), cachedGKErr
}

// cloneNestedMap returns a deep copy of m. ReadGK's result is fed to
// viper.MergeConfigMap which lowercases keys in place (insensitiviseMap),
// so handing out the cached map directly would corrupt subsequent reads.
// The map is small (one entry per gk.* key) so the clone cost is
// negligible vs the git fork it saves.
func cloneNestedMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = cloneNestedMap(sub)
		} else {
			out[k] = v
		}
	}
	return out
}

// isNoSuchFile reports whether err is a "no such file or directory" error.
func isNoSuchFile(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no such file") ||
		strings.Contains(err.Error(), "cannot find") ||
		os.IsNotExist(err)
}
