package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// cacheRunner stubs the git-path lookup so the cache lands under a temp dir,
// plus an optional origin URL for the status-render path.
func cacheRunner(t *testing.T, originURL string) *git.FakeRunner {
	t.Helper()
	dir := t.TempDir()
	resp := map[string]git.FakeResponse{
		"rev-parse --git-path gk-github-cache": {Stdout: dir + "/gk-github-cache\n"},
	}
	if originURL != "" {
		resp["remote get-url origin"] = git.FakeResponse{Stdout: originURL + "\n"}
	}
	return &git.FakeRunner{Responses: resp}
}

func TestGitHubCountsRoundTrip(t *testing.T) {
	runner := cacheRunner(t, "")
	ctx := context.Background()

	if _, ok := readGitHubCounts(ctx, runner, "x-mesh/gk"); ok {
		t.Fatal("expected no cache entry before any write")
	}

	want := githubCounts{Repo: "x-mesh/gk", OpenPRs: 3, OpenIssues: 2, ReviewRequested: 1, FetchedAt: time.Now()}
	writeGitHubCounts(ctx, runner, want)

	got, ok := readGitHubCounts(ctx, runner, "x-mesh/gk")
	if !ok {
		t.Fatal("expected a cache entry after write")
	}
	if got.OpenPRs != 3 || got.OpenIssues != 2 || got.ReviewRequested != 1 || got.Repo != "x-mesh/gk" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestRenderStatusGitHubCacheHit(t *testing.T) {
	runner := cacheRunner(t, "git@github.com:x-mesh/gk.git")
	ctx := context.Background()
	cfg := &config.Config{}

	// No cache yet → silent.
	if line := renderStatusGitHub(ctx, runner, cfg); line != "" {
		t.Fatalf("expected empty line before cache, got %q", line)
	}

	writeGitHubCounts(ctx, runner, githubCounts{Repo: "x-mesh/gk", OpenPRs: 4, OpenIssues: 1, FetchedAt: time.Now()})

	line := renderStatusGitHub(ctx, runner, cfg)
	if !strings.Contains(line, "x-mesh/gk") || !strings.Contains(line, "4 PR") || !strings.Contains(line, "1 issue") {
		t.Fatalf("status line missing expected counts: %q", line)
	}
}

func TestRenderStatusGitHubNonGitHubOriginSilent(t *testing.T) {
	runner := cacheRunner(t, "git@gitlab.com:x-mesh/gk.git")
	if line := renderStatusGitHub(context.Background(), runner, &config.Config{}); line != "" {
		t.Fatalf("non-github origin must be silent, got %q", line)
	}
}
