package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

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
	"sync":      true,
	"refresh":   true,
	"preflight": true,
	"clone":     true,
	"worktree":  true,
	"ai":        true,
	"output":    true,
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
	v.SetDefault("ai.lang", defaults.AI.Lang)
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
	v.SetDefault("ai.nvidia.model", defaults.AI.Nvidia.Model)
	v.SetDefault("ai.nvidia.endpoint", defaults.AI.Nvidia.Endpoint)
	v.SetDefault("ai.nvidia.timeout", defaults.AI.Nvidia.Timeout)
	v.SetDefault("output.easy", defaults.Output.Easy)
	v.SetDefault("output.lang", defaults.Output.Lang)
	v.SetDefault("output.emoji", defaults.Output.Emoji)
	v.SetDefault("output.hints", defaults.Output.Hints)

	// --- 2. Global config: $XDG_CONFIG_HOME/gk/config.yaml ---
	globalDir := xdgConfigDir()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(globalDir)
	// ReadInConfig returns an error when the file does not exist; ignore that.
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			// A real parse error — surface it.
			if !isNoSuchFile(err) {
				return nil, err
			}
		}
	}

	// --- 3. Repo-local .gk.yaml ---
	if repoRoot, err := cachedGitRepoRoot(); err == nil && repoRoot != "" {
		localCfg := filepath.Join(repoRoot, ".gk.yaml")
		if _, statErr := os.Stat(localCfg); statErr == nil {
			v2 := viper.New()
			v2.SetConfigFile(localCfg)
			if err2 := v2.ReadInConfig(); err2 == nil {
				if mergeErr := v.MergeConfigMap(v2.AllSettings()); mergeErr != nil {
					return nil, mergeErr
				}
			}
		}
	}

	// --- 4. git config gk.* ---
	gkMap, err := cachedReadGK()
	if err == nil && len(gkMap) > 0 {
		if mergeErr := v.MergeConfigMap(gkMap); mergeErr != nil {
			return nil, mergeErr
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
			return nil, bindErr
		}
	}

	// Unmarshal into Config struct.
	cfg := Defaults()
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

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

// gitRepoRoot returns the working-tree root of the current git repository.
// Returns empty string (nil error) when not in a git repo.
func gitRepoRoot() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := cmdOutput(ctx, "git", "rev-parse", "--show-toplevel")
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
