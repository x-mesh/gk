// Package branchparent persists and resolves the parent branch of a local
// git branch — the branch from which it forked and against which divergence
// should be reported. It is the single source of truth for the gk-parent
// metadata; status, switch, and worktree consume it via Resolver.
//
// Storage is per-branch git config: `branch.<name>.gk-parent`. The choice of
// git config (over an external file) means parent metadata travels with the
// repo's local clone, no synchronization with the working tree is needed,
// and other gk versions that don't recognize the key simply ignore it.
package branchparent

import (
	"context"
	"fmt"

	"github.com/x-mesh/gk/internal/git"
)

const parentConfigKey = "gk-parent"

// Config is the write-path API for the gk-parent metadata. Reads also live
// here so callers that only need the explicit value (no inference, no
// fallback) avoid pulling in Resolver. SetParent does NOT validate; that's
// validateParent's job — Config is the dumb wrapper around git config.
type Config struct {
	c *git.Client
}

// NewConfig wraps a *git.Client. The client supplies the runner used for
// every git config invocation; nothing is cached.
func NewConfig(c *git.Client) *Config { return &Config{c: c} }

// GetParent reads `branch.<branch>.gk-parent`. Returns ("", nil) when unset,
// matching git's documented exit-1-for-missing-key contract. A non-nil error
// only surfaces real failures (broken config, missing repo) — callers can
// safely treat ("", nil) as "no parent configured".
func (cfg *Config) GetParent(ctx context.Context, branch string) (string, error) {
	return cfg.c.GetBranchConfig(ctx, branch, parentConfigKey)
}

// SetParent writes the parent. The value is stored verbatim; no normalization
// (e.g., stripping `refs/heads/`). Validate before calling — see ValidateSet
// in this package, which the CLI write path runs first.
//
// Empty parent is rejected: storing "" would create a key whose value reads
// back as "" on GetParent, indistinguishable from "unset". Callers wanting
// to remove the metadata must use UnsetParent.
func (cfg *Config) SetParent(ctx context.Context, branch, parent string) error {
	if parent == "" {
		return fmt.Errorf("parent must not be empty; use UnsetParent to clear")
	}
	return cfg.c.SetBranchConfig(ctx, branch, parentConfigKey, parent)
}

// UnsetParent removes the key. Idempotent: returns nil when the key was
// already absent, so callers don't need to check first.
func (cfg *Config) UnsetParent(ctx context.Context, branch string) error {
	return cfg.c.UnsetBranchConfig(ctx, branch, parentConfigKey)
}
