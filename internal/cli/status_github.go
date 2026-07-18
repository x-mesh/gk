package cli

import (
	"context"
	"fmt"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// renderStatusGitHub builds the optional `--vis github` line for gk status.
//
// The `github.counts.status` policy decides freshness: the default "cache"
// reads the on-disk cache only and NEVER touches the network, keeping the
// status hot path instant, offline-safe, and off the search rate limit.
// Setting it to ttl/force lets status refresh (the user's explicit opt-in).
// The --vis token is what enables the line at all, so an "off" policy is
// coerced to "cache" here (the user already asked to see it).
//
// Returns "" when there is no GitHub origin or no counts to show yet — run
// `gk context --include=github` (or a `gk pr`/`gk issue`) once to populate it.
func renderStatusGitHub(ctx context.Context, runner git.Runner, cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	policy := cfg.GitHub.Counts.Status
	if policy == "off" || policy == "" {
		policy = "cache"
	}
	c, err := githubCountsResolve(ctx, runner, cfg, policy)
	if err != nil || c == nil {
		return "" // fetch failed with no fallback, or nothing to show — stay silent
	}

	body := fmt.Sprintf("%d PR · %d issue", c.OpenPRs, c.OpenIssues)
	if c.ReviewRequested > 0 {
		body += fmt.Sprintf(" · %d review", c.ReviewRequested)
	}
	return cellFaint(fmt.Sprintf("github %s: %s (%s)", c.Repo, body, shortAge(c.FetchedAt)))
}
