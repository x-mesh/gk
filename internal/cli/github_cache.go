package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// githubCountTTL bounds how long a cached count is treated as fresh.
// `gk context --include=github` refetches once a cache entry is older than
// this; `gk status --vis github` only ever READS the cache (it never fetches
// on its hot path), so it may show an older value tagged with its age.
const githubCountTTL = 10 * time.Minute

// githubCounts is the cached GitHub orientation for one repo. It is written by
// the network-touching path (`gk context --include=github`) and read by the
// offline hot path (`gk status --vis github`), so status never blocks on the
// network or the search rate limit.
type githubCounts struct {
	Repo            string    `json:"repo"` // owner/name
	OpenPRs         int       `json:"open_prs"`
	OpenIssues      int       `json:"open_issues"`
	ReviewRequested int       `json:"review_requested"`
	FetchedAt       time.Time `json:"fetched_at"`
}

// githubCacheFile resolves .git/gk-github-cache/<hash>.json, mirroring
// aiCacheDir's git-path approach. ok=false outside a repo (or when runner is
// nil), so callers degrade to "no cache" rather than erroring.
func githubCacheFile(ctx context.Context, runner git.Runner, slug string) (string, bool) {
	if runner == nil {
		return "", false
	}
	out, _, err := runner.Run(ctx, "rev-parse", "--git-path", "gk-github-cache")
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		base := runnerDir(runner)
		if base == "" {
			base = RepoFlag()
		}
		p = filepath.Join(base, p)
	}
	return filepath.Join(p, aiCacheKey(slug)+".json"), true
}

// readGitHubCounts loads the cached counts for slug. ok=false when there is no
// cache entry (or it is unreadable/corrupt) — the caller decides whether to
// fetch (context) or stay silent (status).
func readGitHubCounts(ctx context.Context, runner git.Runner, slug string) (githubCounts, bool) {
	f, ok := githubCacheFile(ctx, runner, slug)
	if !ok {
		return githubCounts{}, false
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return githubCounts{}, false
	}
	var c githubCounts
	if json.Unmarshal(b, &c) != nil {
		return githubCounts{}, false
	}
	return c, true
}

// writeGitHubCounts persists c atomically. Best-effort: any error (not a repo,
// unwritable dir) is swallowed — a missing cache just means status shows
// nothing until the next successful fetch.
func writeGitHubCounts(ctx context.Context, runner git.Runner, c githubCounts) {
	f, ok := githubCacheFile(ctx, runner, c.Repo)
	if !ok {
		return
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := f + ".tmp"
	if os.WriteFile(tmp, b, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, f)
}
