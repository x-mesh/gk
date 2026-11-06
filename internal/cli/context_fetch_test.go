package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// newFetchFixture returns a local clone of origin on main, tracking
// origin/main, and then lands one more commit on origin that the local
// remote-tracking ref has not seen yet.
func newFetchFixture(t *testing.T) (origin, local *testutil.Repo) {
	t.Helper()
	origin = testutil.NewRepo(t)
	origin.WriteFile("seed.txt", "seed\n")
	origin.Commit("seed: initial")

	local = testutil.NewRepo(t)
	local.AddRemote("origin", origin.Dir)
	local.RunGit("fetch", "origin")
	local.RunGit("reset", "--hard", "origin/main")
	local.RunGit("branch", "--set-upstream-to=origin/main", "main")

	origin.WriteFile("remote.txt", "remote\n")
	origin.Commit("feat: remote-only work")
	return origin, local
}

func TestIntegration_ContextFetchRefreshesCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, local := newFetchFixture(t)
	prev := flagRepo
	flagRepo = local.Dir
	t.Cleanup(func() { flagRepo = prev })

	ctx := context.Background()
	runner := &git.ExecRunner{Dir: local.Dir}
	cfg := config.Defaults()

	stale, err := collectContext(ctx, runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	if stale.Behind != 0 {
		t.Fatalf("before fetch: behind = %d, want 0 (tracking ref not refreshed yet)", stale.Behind)
	}

	results := runContextFetch(ctx, runner, contextFetchRemotes(ctx, runner, &cfg, "main"))
	if len(results) != 1 || results[0].Remote != "origin" || !results[0].OK {
		t.Fatalf("fetch results = %+v, want one ok origin entry", results)
	}
	fresh, err := collectContext(ctx, runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	if fresh.Behind != 1 {
		t.Errorf("after fetch: behind = %d, want 1", fresh.Behind)
	}
}

func TestIntegration_ContextFetchFailureIsReported(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, local := newFetchFixture(t)
	local.RunGit("remote", "set-url", "origin", local.Dir+"/missing-remote.git")

	ctx := context.Background()
	runner := &git.ExecRunner{Dir: local.Dir}
	cfg := config.Defaults()
	results := runContextFetch(ctx, runner, contextFetchRemotes(ctx, runner, &cfg, "main"))
	if len(results) != 1 || results[0].OK || results[0].Error == "" {
		t.Fatalf("fetch results = %+v, want origin ok:false with an error", results)
	}
}

func TestIntegration_ContextFetchRemotes(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	origin, local := newFetchFixture(t)
	local.AddRemote("fork", origin.Dir)
	local.RunGit("fetch", "fork")
	local.RunGit("branch", "--set-upstream-to=fork/main", "main")

	ctx := context.Background()
	runner := &git.ExecRunner{Dir: local.Dir}
	cfg := config.Defaults()
	got := contextFetchRemotes(ctx, runner, &cfg, "main")
	if strings.Join(got, ",") != "origin,fork" {
		t.Errorf("remotes = %v, want base remote then the upstream remote", got)
	}

	cfg.Remote = "nowhere"
	got = contextFetchRemotes(ctx, runner, &cfg, "main")
	if strings.Join(got, ",") != "fork" {
		t.Errorf("remotes = %v, want an unconfigured base remote dropped", got)
	}
}

func TestIntegration_ContextBaseAheadRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, local := newFetchFixture(t)
	local.WriteFile("local.txt", "local\n")
	local.Commit("feat: unpushed base work")
	local.WriteFile("local2.txt", "local\n")
	local.Commit("feat: more unpushed base work")
	local.RunGit("fetch", "origin")
	local.RunGit("switch", "-c", "feat/x")
	prev := flagRepo
	flagRepo = local.Dir
	t.Cleanup(func() { flagRepo = prev })

	cfg := config.Defaults()
	cfg.BaseBranch = "main"
	out, err := collectContext(context.Background(), &git.ExecRunner{Dir: local.Dir}, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	if out.Base == nil || out.Base.AheadRemote != 2 || out.Base.BehindRemote != 1 {
		t.Errorf("base = %+v, want ahead_remote=2 and behind_remote=1", out.Base)
	}
}

// TestRunContext_FetchFlag drives the command so the flag wiring and the
// fetch-before-collect order are covered, not just the helpers.
func TestRunContext_FetchFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	_, local := newFetchFixture(t)
	prevRepo, prevJSON := flagRepo, flagJSON
	flagRepo, flagJSON = local.Dir, false
	t.Cleanup(func() { flagRepo, flagJSON = prevRepo, prevJSON })

	cmd := &cobra.Command{Use: "context", RunE: runContext}
	cmd.Flags().StringSlice("include", nil, "")
	cmd.Flags().Bool("delta", false, "")
	cmd.Flags().String("worktrees", "all", "")
	cmd.Flags().Bool("fetch", false, "")
	cmd.SetArgs([]string{"--fetch"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("context --fetch: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "↓1") || !strings.Contains(buf.String(), "fetched origin") {
		t.Errorf("output should show the refreshed behind count and the fetch line:\n%s", buf.String())
	}
}
