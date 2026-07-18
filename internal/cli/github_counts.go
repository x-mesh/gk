package cli

import (
	"context"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// githubFetchTimeout bounds the network fetch for GitHub counts. Every path
// that fetches (context/status under ttl or force) runs under it, so no GitHub
// call can hang an orientation or a status invocation.
const githubFetchTimeout = 5 * time.Second

// githubCountPolicyValid reports whether p is a recognized per-surface policy.
func githubCountPolicyValid(p string) bool {
	switch p {
	case "off", "cache", "ttl", "force":
		return true
	}
	return false
}

// githubFetchFn is the fetch implementation githubCountsResolve calls. It is a
// package var so tests can substitute a canned fetch without a network round
// trip; production points it at fetchGitHubCounts.
var githubFetchFn = fetchGitHubCounts

// fetchGitHubCounts performs the network fetch (open PRs, open issues, and —
// with a token — review-requested) for slug under a hard timeout, writes the
// result to the shared cache, and returns it.
func fetchGitHubCounts(ctx context.Context, runner git.Runner, slug string) (githubCounts, error) {
	tctx, cancel := context.WithTimeout(ctx, githubFetchTimeout)
	defer cancel()

	token := ghapi.ResolveToken()
	client := &ghapi.Client{Token: token}

	openPRs, err := client.SearchCount(tctx, "repo:"+slug+" is:open is:pr")
	if err != nil {
		return githubCounts{}, err
	}
	openIssues, err := client.SearchCount(tctx, "repo:"+slug+" is:open is:issue")
	if err != nil {
		return githubCounts{}, err
	}
	reviewRequested := 0
	// review-requested:@me needs a token to resolve @me; skip silently when
	// unauthenticated rather than failing the whole fetch.
	if token != "" {
		if n, rerr := client.SearchCount(tctx, "repo:"+slug+" is:open is:pr review-requested:@me"); rerr == nil {
			reviewRequested = n
		}
	}
	c := githubCounts{
		Repo:            slug,
		OpenPRs:         openPRs,
		OpenIssues:      openIssues,
		ReviewRequested: reviewRequested,
		FetchedAt:       time.Now(),
	}
	writeGitHubCounts(ctx, runner, c)
	return c, nil
}

// githubCountsResolve returns the counts to display for one surface, honoring
// its policy (off | cache | ttl | force). It resolves the current repo's
// GitHub slug from origin.
//
// err is non-nil ONLY when a fetch was required (ttl-stale or force) but failed
// AND there is no cached value to fall back on — callers turn that into a note.
// (nil, nil) means "nothing to show" (off, no GitHub origin, or a cache-policy
// cold cache) and is NOT an error.
func githubCountsResolve(ctx context.Context, runner git.Runner, cfg *config.Config, policy string) (*githubCounts, error) {
	if cfg == nil || policy == "off" || policy == "" {
		return nil, nil
	}
	if !githubCountPolicyValid(policy) {
		policy = "cache" // unknown policy → safest (never fetch)
	}
	owner, repo, err := currentRepoSlug(ctx, *cfg, runner)
	if err != nil {
		return nil, nil // no GitHub origin — stay silent
	}
	slug := owner + "/" + repo

	cached, hasCache := readGitHubCounts(ctx, runner, slug)

	switch policy {
	case "cache":
		if hasCache {
			return &cached, nil
		}
		return nil, nil
	case "ttl":
		if hasCache && time.Since(cached.FetchedAt) < githubCountTTLFrom(cfg) {
			return &cached, nil
		}
		fresh, ferr := githubFetchFn(ctx, runner, slug)
		if ferr != nil {
			if hasCache {
				return &cached, nil // fall back to stale rather than showing nothing
			}
			return nil, ferr
		}
		return &fresh, nil
	case "force":
		fresh, ferr := githubFetchFn(ctx, runner, slug)
		if ferr != nil {
			if hasCache {
				return &cached, nil
			}
			return nil, ferr
		}
		return &fresh, nil
	}
	return nil, nil
}

// githubCountTTLFrom resolves the configured freshness window, falling back to
// the built-in default when unset or non-positive.
func githubCountTTLFrom(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.GitHub.Counts.TTLMinutes > 0 {
		return time.Duration(cfg.GitHub.Counts.TTLMinutes) * time.Minute
	}
	return githubCountTTL
}

// warmGitHubCountFromList refreshes one field of the count cache from a
// current-repo listing that already hit the network (free warm). It is a
// PARTIAL update: a `gk pr` run knows only the open-PR count, a `gk issue` run
// only the open-issue count; the other field keeps its previous value while
// FetchedAt bumps to now.
func warmGitHubCountFromList(ctx context.Context, runner git.Runner, slug string, isPR bool, n int) {
	c, _ := readGitHubCounts(ctx, runner, slug) // zero value when no prior cache
	c.Repo = slug
	if isPR {
		c.OpenPRs = n
	} else {
		c.OpenIssues = n
	}
	c.FetchedAt = time.Now()
	writeGitHubCounts(ctx, runner, c)
}
