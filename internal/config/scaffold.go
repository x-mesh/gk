package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

//go:embed default_config.yaml
var defaultConfigTemplate string

// DefaultConfigTemplate returns the commented YAML template written by
// `gk init config` and the first-run auto-init path. Exposed so tests
// (and `gk config template`) can inspect what will land on disk.
func DefaultConfigTemplate() string { return defaultConfigTemplate }

// GlobalConfigPath resolves to $XDG_CONFIG_HOME/gk/config.yaml
// (defaulting to ~/.config/gk/config.yaml). Returns empty when the
// user's home directory is unknown — callers should treat that as a
// signal to skip writing rather than crash.
func GlobalConfigPath() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "gk", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "gk", "config.yaml")
}

// GlobalInitAIGitignore reports whether the GLOBAL config file turns on
// init.ai_gitignore. `gk init` consults this instead of the layered value:
// the key enables a remote AI call, so a repo-local .gk.yaml inside an
// untrusted checkout must not be able to switch it on silently — only the
// user's own global config (or an explicit --ai-gitignore flag) may.
func GlobalInitAIGitignore() bool {
	path := GlobalConfigPath()
	if path == "" {
		return false
	}
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return false
	}
	return v.GetBool("init.ai_gitignore")
}

// GlobalResolveSettings reads resolve.verify and resolve.union_files from
// the GLOBAL config file only. Both keys shape what `gk resolve` executes or
// auto-merges, so a repo-local .gk.yaml inside an untrusted checkout must
// not be able to widen them: verify runs arbitrary shell commands (the same
// trust boundary as init.ai_gitignore), and union_files silently expands
// the auto-merge surface. unionSet distinguishes "unset → defaults" from an
// explicit empty list (= union merging disabled).
func GlobalResolveSettings() (verify []string, unionFiles []string, unionSet bool) {
	path := GlobalConfigPath()
	if path == "" {
		return nil, nil, false
	}
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return nil, nil, false
	}
	verify = v.GetStringSlice("resolve.verify")
	if v.IsSet("resolve.union_files") {
		return verify, v.GetStringSlice("resolve.union_files"), true
	}
	return verify, nil, false
}

// GlobalConfigHealthy reports a non-nil error when the global config file
// EXISTS but cannot be parsed — the global-only resolve safety settings
// (verify, union_files, min_confidence) silently fall back to their
// defaults in that state, which callers should surface, not swallow.
func GlobalConfigHealthy() error {
	path := GlobalConfigPath()
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil // absent is a legitimate state, not a failure
	}
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("global config %s unreadable: %w", path, err)
	}
	return nil
}

// GlobalChatSettings reads gk chat's tool-loop knobs from the GLOBAL
// config only (ai.chat.max_tool_rounds / tool_result_cap / deny_paths).
// Same trust boundary as resolve.verify: a cloned repo's .gk.yaml must
// not be able to raise the tool budget or alter the deny surface of a
// chat session running inside it. Zero returns mean "unset — use the
// caller's default"; denyExtra only ever ADDS globs (the caller unions
// with DefaultDenyPaths, so no layer can shrink below the defaults).
func GlobalChatSettings() (maxToolRounds, toolResultCap int, denyExtra []string) {
	path := GlobalConfigPath()
	if path == "" {
		return 0, 0, nil
	}
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return 0, 0, nil
	}
	maxToolRounds = v.GetInt("ai.chat.max_tool_rounds")
	if maxToolRounds < 0 {
		maxToolRounds = 0
	}
	toolResultCap = v.GetInt("ai.chat.tool_result_cap")
	if toolResultCap < 0 {
		toolResultCap = 0
	}
	return maxToolRounds, toolResultCap, v.GetStringSlice("ai.chat.deny_paths")
}

// GlobalResolveMinConfidence reads resolve.min_confidence from the GLOBAL
// config file only — a repo-local .gk.yaml lowering the gate would widen
// what gets auto-applied inside an untrusted checkout.
func GlobalResolveMinConfidence() float64 {
	path := GlobalConfigPath()
	if path == "" {
		return 0
	}
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return 0
	}
	f := v.GetFloat64("resolve.min_confidence")
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// WriteDefaultConfig creates the commented YAML template at path.
//
// Behaviour:
//   - Creates missing parent directories (0o755).
//   - Refuses to overwrite an existing file unless force=true.
//     Returns ErrConfigExists so callers can distinguish that from a
//     genuine IO failure.
//   - Writes the exact bytes of the embedded template (deterministic
//     across releases — no template expansion).
func WriteDefaultConfig(path string, force bool) error {
	if path == "" {
		return fmt.Errorf("gk: config scaffold: empty path")
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%w: %s", ErrConfigExists, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("gk: config scaffold: stat: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gk: config scaffold: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(defaultConfigTemplate), 0o644); err != nil {
		return fmt.Errorf("gk: config scaffold: write: %w", err)
	}
	return nil
}

// EnsureGlobalConfig is the hook wired into cmd/gk/main.go. On first
// run — when the global config file is missing — it writes the
// commented template and prints a single stderr notice so users see
// "where" their config lives. Failures are swallowed: a read-only
// home dir, sandbox, or bad $XDG_CONFIG_HOME must not break gk.
//
// Opt-outs:
//   - GK_NO_AUTO_CONFIG=1  skips the attempt entirely (useful in CI
//     and in our own test suite).
//   - Any IO error during write is silently ignored — tell the user
//     next time, not now.
//
// Returns (created, path). `created` is true only when the file was
// freshly written on this call.
func EnsureGlobalConfig() (bool, string) {
	if os.Getenv("GK_NO_AUTO_CONFIG") == "1" {
		return false, ""
	}
	path := GlobalConfigPath()
	if path == "" {
		return false, ""
	}
	if _, err := os.Stat(path); err == nil {
		return false, path
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, path
	}
	if err := WriteDefaultConfig(path, false); err != nil {
		return false, path
	}
	return true, path
}

// ErrConfigExists is returned by WriteDefaultConfig when the target
// path already exists and force=false.
var ErrConfigExists = errors.New("config file already exists")
