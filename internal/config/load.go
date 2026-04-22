package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

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
	v.SetDefault("status.vis", defaults.Status.Vis)
	v.SetDefault("status.auto_fetch", defaults.Status.AutoFetch)
	v.SetDefault("ui.color", defaults.UI.Color)
	v.SetDefault("ui.prefer", defaults.UI.Prefer)
	v.SetDefault("branch.stale_days", defaults.Branch.StaleDays)
	v.SetDefault("branch.protected", defaults.Branch.Protected)

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
	if repoRoot, err := gitRepoRoot(); err == nil && repoRoot != "" {
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
	gkMap, err := ReadGK()
	if err == nil && len(gkMap) > 0 {
		if mergeErr := v.MergeConfigMap(gkMap); mergeErr != nil {
			return nil, mergeErr
		}
	}

	// --- 5. Environment variables: GK_* ---
	v.SetEnvPrefix("GK")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// --- 6. CLI flags ---
	if flags != nil {
		if err := v.BindPFlags(flags); err != nil {
			return nil, err
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

// isNoSuchFile reports whether err is a "no such file or directory" error.
func isNoSuchFile(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no such file") ||
		strings.Contains(err.Error(), "cannot find") ||
		os.IsNotExist(err)
}
