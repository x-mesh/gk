package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// countsCfg builds a config with the given per-surface policy set for every
// surface and a 3-minute TTL.
func countsCfg(policy string) *config.Config {
	return &config.Config{GitHub: config.GitHubConfig{Counts: config.GitHubCountsConfig{
		TTLMinutes: 3, Context: policy, Include: policy, Status: policy,
	}}}
}

// withStubFetch swaps githubFetchFn for the duration of a test, recording calls.
func withStubFetch(t *testing.T, result githubCounts, err error) *int {
	t.Helper()
	calls := 0
	orig := githubFetchFn
	githubFetchFn = func(_ context.Context, _ git.Runner, slug string) (githubCounts, error) {
		calls++
		result.Repo = slug
		return result, err
	}
	t.Cleanup(func() { githubFetchFn = orig })
	return &calls
}

func TestGitHubCountsResolveOff(t *testing.T) {
	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	calls := withStubFetch(t, githubCounts{}, nil)
	got, err := githubCountsResolve(context.Background(), runner, countsCfg("off"), "off")
	if err != nil || got != nil {
		t.Fatalf("off must show nothing, got %v, %v", got, err)
	}
	if *calls != 0 {
		t.Errorf("off must not fetch, calls=%d", *calls)
	}
}

func TestGitHubCountsResolveCache(t *testing.T) {
	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	ctx := context.Background()
	calls := withStubFetch(t, githubCounts{}, nil)

	// cold cache → nothing, no fetch
	if got, _ := githubCountsResolve(ctx, runner, countsCfg("cache"), "cache"); got != nil {
		t.Fatalf("cold cache must be nil, got %v", got)
	}
	// warm cache → served, still no fetch
	writeGitHubCounts(ctx, runner, githubCounts{Repo: "x-mesh/gk", OpenPRs: 7, FetchedAt: time.Now()})
	got, _ := githubCountsResolve(ctx, runner, countsCfg("cache"), "cache")
	if got == nil || got.OpenPRs != 7 {
		t.Fatalf("cache hit expected, got %v", got)
	}
	if *calls != 0 {
		t.Errorf("cache policy must never fetch, calls=%d", *calls)
	}
}

func TestGitHubCountsResolveTTLFreshNoFetch(t *testing.T) {
	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	ctx := context.Background()
	calls := withStubFetch(t, githubCounts{OpenPRs: 99}, nil)

	writeGitHubCounts(ctx, runner, githubCounts{Repo: "x-mesh/gk", OpenPRs: 2, FetchedAt: time.Now()})
	got, _ := githubCountsResolve(ctx, runner, countsCfg("ttl"), "ttl")
	if got == nil || got.OpenPRs != 2 {
		t.Fatalf("fresh ttl should serve cache (2), got %v", got)
	}
	if *calls != 0 {
		t.Errorf("fresh ttl must not fetch, calls=%d", *calls)
	}
}

func TestGitHubCountsResolveTTLStaleFetches(t *testing.T) {
	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	ctx := context.Background()
	calls := withStubFetch(t, githubCounts{OpenPRs: 42, FetchedAt: time.Now()}, nil)

	// stale cache (older than 3-min TTL)
	writeGitHubCounts(ctx, runner, githubCounts{Repo: "x-mesh/gk", OpenPRs: 2, FetchedAt: time.Now().Add(-10 * time.Minute)})
	got, _ := githubCountsResolve(ctx, runner, countsCfg("ttl"), "ttl")
	if got == nil || got.OpenPRs != 42 {
		t.Fatalf("stale ttl should fetch (42), got %v", got)
	}
	if *calls != 1 {
		t.Errorf("stale ttl must fetch once, calls=%d", *calls)
	}
}

func TestGitHubCountsResolveForceAlwaysFetches(t *testing.T) {
	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	ctx := context.Background()
	calls := withStubFetch(t, githubCounts{OpenPRs: 5, FetchedAt: time.Now()}, nil)

	// even a fresh cache is bypassed by force
	writeGitHubCounts(ctx, runner, githubCounts{Repo: "x-mesh/gk", OpenPRs: 2, FetchedAt: time.Now()})
	got, _ := githubCountsResolve(ctx, runner, countsCfg("force"), "force")
	if got == nil || got.OpenPRs != 5 {
		t.Fatalf("force should fetch (5), got %v", got)
	}
	if *calls != 1 {
		t.Errorf("force must fetch, calls=%d", *calls)
	}
}

func TestGitHubCountsResolveFetchFailFallsBackToStale(t *testing.T) {
	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	ctx := context.Background()
	withStubFetch(t, githubCounts{}, errors.New("network down"))

	writeGitHubCounts(ctx, runner, githubCounts{Repo: "x-mesh/gk", OpenPRs: 3, FetchedAt: time.Now().Add(-time.Hour)})
	got, err := githubCountsResolve(ctx, runner, countsCfg("force"), "force")
	if err != nil {
		t.Fatalf("fetch failure with a cached fallback must not error, got %v", err)
	}
	if got == nil || got.OpenPRs != 3 {
		t.Fatalf("expected stale fallback (3), got %v", got)
	}
}

func TestGitHubCountsResolveFetchFailNoCacheErrors(t *testing.T) {
	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	withStubFetch(t, githubCounts{}, errors.New("network down"))

	_, err := githubCountsResolve(context.Background(), runner, countsCfg("force"), "force")
	if err == nil {
		t.Fatal("fetch failure with no cache must surface an error (for the note)")
	}
}

func TestGitHubCountsResolveNonGitHubOrigin(t *testing.T) {
	runner := cacheRunner(t, "git@gitlab.com:x-mesh/gk.git")
	got, err := githubCountsResolve(context.Background(), runner, countsCfg("force"), "force")
	if got != nil || err != nil {
		t.Fatalf("non-github origin must be silent (nil,nil), got %v, %v", got, err)
	}
}

func TestWarmGitHubCountPartial(t *testing.T) {
	runner := cacheRunner(t, "")
	ctx := context.Background()

	// pr warm sets OpenPRs; a later issue warm sets OpenIssues, keeping PRs.
	warmGitHubCountFromList(ctx, runner, "x-mesh/gk", true, 4)
	warmGitHubCountFromList(ctx, runner, "x-mesh/gk", false, 9)

	c, ok := readGitHubCounts(ctx, runner, "x-mesh/gk")
	if !ok || c.OpenPRs != 4 || c.OpenIssues != 9 {
		t.Fatalf("partial warm mismatch: %+v", c)
	}
}
