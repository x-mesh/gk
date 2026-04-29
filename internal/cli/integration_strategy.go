package cli

import (
	"context"
	"strings"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// resolveIntegrationStrategy is the shared resolver for `gk pull` and
// `gk sync` strategy selection. The priority chain is:
//
//  1. explicit --strategy flag (when not "" or "auto")
//  2. .gk.yaml strategy key (when not "" or "auto")
//  3. git config pull.rebase  (true→rebase, false→merge)
//  4. default: rebase
//
// cfgSource is the human-readable label for level 2 (e.g.,
// ".gk.yaml pull.strategy" or ".gk.yaml sync.strategy") so the caller's
// verbose output can attribute the decision precisely.
func resolveIntegrationStrategy(
	ctx context.Context,
	flag, cfgStrategy, cfgSource string,
	runner git.Runner,
) (string, string) {
	if flag != "" && flag != pullStrategyAuto {
		return flag, "--strategy"
	}
	if cfgStrategy != "" && cfgStrategy != pullStrategyAuto {
		return cfgStrategy, cfgSource
	}
	if out, _, err := runner.Run(ctx, "config", "--get", "pull.rebase"); err == nil {
		switch strings.TrimSpace(string(out)) {
		case "true", "1", "yes":
			return pullStrategyRebase, "git config pull.rebase"
		case "false", "0", "no":
			return pullStrategyMerge, "git config pull.rebase"
		}
	}
	return pullStrategyRebase, "default"
}

// resolveSyncStrategyWithSource picks the strategy for `gk sync`, mirroring
// the resolution chain used by pull but reading from cfg.Sync.Strategy.
func resolveSyncStrategyWithSource(
	ctx context.Context,
	flag string,
	cfg *config.Config,
	runner git.Runner,
) (string, string) {
	return resolveIntegrationStrategy(ctx, flag, cfg.Sync.Strategy, ".gk.yaml sync.strategy", runner)
}
