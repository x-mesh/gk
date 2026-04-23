package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
