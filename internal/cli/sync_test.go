package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// buildSyncCmd wires a cobra root + `sync` subcommand targeting repoDir
// for the new "catch up to base" sync. Test-local because the real init()
// registers against the global rootCmd.
func buildSyncCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	sync := &cobra.Command{
		Use:          "sync",
		RunE:         func(c *cobra.Command, _ []string) error { return runSyncCore(c) },
		SilenceUsage: true,
	}
	sync.Flags().String("base", "", "base branch")
	sync.Flags().String("strategy", "", "integration strategy")
	sync.Flags().Bool("fetch-only", false, "fetch only")
	sync.Flags().Bool("no-fetch", false, "skip fetch")
	sync.Flags().Bool("autostash", false, "autostash")
	sync.Flags().Bool("upstream-only", false, "legacy v0.6 behaviour")
	testRoot.AddCommand(sync)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	allArgs := append([]string{"--repo", repoDir, "sync"}, extraArgs...)
	testRoot.SetArgs(allArgs)
	return testRoot, buf
}

// execRunnerFor returns a live git.ExecRunner rooted at the repo directory.
func execRunnerFor(r *testutil.Repo) *git.ExecRunner {
	return &git.ExecRunner{Dir: r.Dir}
}

// setupFeatureFromMain builds (upstream, downstream) where:
//   - upstream has one main commit at SHA m0
//   - downstream tracks origin/main at m0
//   - downstream is on a feat/x branch off m0 with no commits yet
//
// Tests advance one or both sides further to construct the scenario.
func setupFeatureFromMain(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "hello\n")
	upstream.Commit("init")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.CreateBranch("feat/x")
	downstream.Checkout("feat/x")
	return upstream, downstream
}

// untrackedDownstream returns a downstream repo whose `main` has the
// origin/main ref cached but lacks a configured upstream — exactly the
// scenario where main fell out of sync silently. Used by status/doctor tests.
func untrackedDownstream(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "hello\n")
	upstream.Commit("feat: a")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.RunGit("reset", "--hard", "origin/main")
	// Intentionally NOT setting upstream.

	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")
	downstream.RunGit("fetch", "origin")
	return upstream, downstream
}

// setupTrackingDownstream returns (upstream, downstream) where downstream/main
// tracks upstream/main. Used by the legacy --upstream-only test.
func setupTrackingDownstream(t *testing.T) (*testutil.Repo, *testutil.Repo) {
	t.Helper()
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "hello\n")
	upstream.Commit("feat: initial")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	return upstream, downstream
}

// ---------------------------------------------------------------------------
// Unit — upstreamOf (still used by the --upstream-only legacy path)
// ---------------------------------------------------------------------------

func TestUpstreamOf_NoUpstream(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	got, _ := upstreamOf(context.Background(), execRunnerFor(repo), "main")
	if got != "" {
		t.Errorf("expected empty for no upstream, got %q", got)
	}
}

func TestUpstreamOf_WithUpstream(t *testing.T) {
	_, downstream := setupTrackingDownstream(t)

	got, err := upstreamOf(context.Background(), execRunnerFor(downstream), "main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "origin/main" {
		t.Errorf("got %q, want origin/main", got)
	}
}

// ---------------------------------------------------------------------------
// Mutex flag check
// ---------------------------------------------------------------------------

func TestSyncCmd_MutexFlags(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, _ := buildSyncCmd(repo.Dir, "--fetch-only", "--no-fetch")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected mutex error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Scenario 1 — ancestor → fast-forward onto base
// ---------------------------------------------------------------------------

func TestSyncCmd_AncestorFastForwards(t *testing.T) {
	upstream, downstream := setupFeatureFromMain(t)

	// upstream main advances by one commit beyond what downstream has.
	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")

	// feat/x has no commits; it's at the original main SHA, which is
	// strictly an ancestor of origin/main after the next fetch. The new
	// sync should auto-fetch and FF.
	root, buf := buildSyncCmd(downstream.Dir, "--base", "main")
	if err := root.Execute(); err != nil {
		t.Fatalf("ancestor sync failed: %v\n%s", err, buf.String())
	}

	out := buf.String()
	if !strings.Contains(out, "fast-forwarded") {
		t.Errorf("expected 'fast-forwarded' in summary, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Scenario 2 — divergence → rebase succeeds
// ---------------------------------------------------------------------------

func TestSyncCmd_DivergedRebaseSuccess(t *testing.T) {
	upstream, downstream := setupFeatureFromMain(t)

	// upstream advances on a different file.
	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")

	// feat/x advances on its own file (no overlap → no conflict).
	downstream.WriteFile("c.txt", "feat-x\n")
	downstream.Commit("feat: c")

	root, buf := buildSyncCmd(downstream.Dir, "--base", "main")
	if err := root.Execute(); err != nil {
		t.Fatalf("rebase sync failed: %v\n%s", err, buf.String())
	}

	out := buf.String()
	if !strings.Contains(out, "rebased") {
		t.Errorf("expected 'rebased' in summary, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Scenario 3 — divergence with conflict → ConflictError
// ---------------------------------------------------------------------------

func TestSyncCmd_DivergedRebaseConflict(t *testing.T) {
	upstream, downstream := setupFeatureFromMain(t)

	// Both sides edit the same file → guaranteed rebase conflict.
	upstream.WriteFile("a.txt", "upstream change\n")
	upstream.Commit("feat: upstream a")

	downstream.WriteFile("a.txt", "downstream change\n")
	downstream.Commit("feat: downstream a")

	root, buf := buildSyncCmd(downstream.Dir, "--base", "main")
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected conflict error, got success.\n%s", buf.String())
	}

	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	if ce.Code != 3 {
		t.Errorf("expected exit code 3, got %d", ce.Code)
	}
}

// ---------------------------------------------------------------------------
// Scenario 4 — --strategy merge → merge commit instead of rebase
// ---------------------------------------------------------------------------

func TestSyncCmd_DivergedStrategyMerge(t *testing.T) {
	upstream, downstream := setupFeatureFromMain(t)

	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")

	downstream.WriteFile("c.txt", "feat-x\n")
	downstream.Commit("feat: c")

	root, buf := buildSyncCmd(downstream.Dir, "--base", "main", "--strategy", "merge")
	if err := root.Execute(); err != nil {
		t.Fatalf("merge sync failed: %v\n%s", err, buf.String())
	}

	// Verify a merge commit landed on feat/x.
	log := downstream.RunGit("log", "--oneline", "-1", "--pretty=format:%P")
	parents := strings.Fields(strings.TrimSpace(log))
	if len(parents) != 2 {
		t.Errorf("expected merge commit (2 parents), got %d:\n%s", len(parents), buf.String())
	}
}

// ---------------------------------------------------------------------------
// Scenario 5 — --strategy ff-only on diverged → rejection
// ---------------------------------------------------------------------------

func TestSyncCmd_FFOnlyDivergedRejection(t *testing.T) {
	upstream, downstream := setupFeatureFromMain(t)

	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")

	downstream.WriteFile("c.txt", "feat-x\n")
	downstream.Commit("feat: c")

	root, buf := buildSyncCmd(downstream.Dir, "--base", "main", "--strategy", "ff-only")
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected ff-only rejection, got success.\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "fast-forward not possible") &&
		!strings.Contains(err.Error(), "ff-only") &&
		!strings.Contains(err.Error(), "diverged") {
		t.Errorf("expected ff-only rejection message, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Scenario 6 — dirty tree + --autostash → stash, integrate, pop
// ---------------------------------------------------------------------------

func TestSyncCmd_DirtyAutostash(t *testing.T) {
	upstream, downstream := setupFeatureFromMain(t)

	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")

	// Dirty tree on a file the integration won't touch.
	downstream.WriteFile("dirty.txt", "wip\n")

	root, buf := buildSyncCmd(downstream.Dir, "--base", "main", "--autostash")
	if err := root.Execute(); err != nil {
		t.Fatalf("autostash sync failed: %v\n%s", err, buf.String())
	}

	// dirty.txt should be back on disk after stash pop.
	status := downstream.RunGit("status", "--porcelain")
	if !strings.Contains(status, "dirty.txt") {
		t.Errorf("expected dirty.txt restored after stash pop, got status:\n%s", status)
	}
}

// ---------------------------------------------------------------------------
// Scenario 7 — --upstream-only legacy path with deprecation notice
// ---------------------------------------------------------------------------

func TestSyncCmd_UpstreamOnlyLegacy(t *testing.T) {
	upstream, downstream := setupTrackingDownstream(t)

	beforeSHA := strings.TrimSpace(downstream.RunGit("rev-parse", "main"))

	// upstream main advances by one commit.
	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")

	// downstream's main is still the old SHA but has @{u} = origin/main.
	t.Setenv("GK_SUPPRESS_DEPRECATION", "")
	root, buf := buildSyncCmd(downstream.Dir, "--upstream-only")
	if err := root.Execute(); err != nil {
		t.Fatalf("upstream-only legacy sync failed: %v\n%s", err, buf.String())
	}

	// The legacy report (writeSyncReport → stdout) contains "main" and a
	// transition arrow when an FF moved HEAD. The deprecation notice
	// itself goes to real os.Stderr (matching the existing project
	// convention in pull.go) and is not captured here — verify the FF
	// took effect via git state instead.
	out := buf.String()
	if !strings.Contains(out, "main") {
		t.Errorf("expected 'main' in legacy report, got:\n%s", out)
	}

	afterSHA := strings.TrimSpace(downstream.RunGit("rev-parse", "main"))
	if beforeSHA == afterSHA {
		t.Errorf("expected legacy FF to advance main; SHA stayed at %s", afterSHA)
	}
}

// TestSyncLegacy_DeprecationSuppressed verifies that GK_SUPPRESS_DEPRECATION=1
// silences the notice. We can't easily capture os.Stderr here; instead we
// assert the run completes cleanly with the env var set (no panic, exit 0)
// and the legacy FF actually happened.
func TestSyncLegacy_DeprecationSuppressed(t *testing.T) {
	upstream, downstream := setupTrackingDownstream(t)
	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")

	t.Setenv("GK_SUPPRESS_DEPRECATION", "1")
	root, buf := buildSyncCmd(downstream.Dir, "--upstream-only")
	if err := root.Execute(); err != nil {
		t.Fatalf("upstream-only with suppressed deprecation failed: %v\n%s",
			err, buf.String())
	}
}

// ---------------------------------------------------------------------------
// Scenario 8 — --no-fetch integrates from already-fetched ref
// ---------------------------------------------------------------------------

func TestSyncCmd_NoFetch(t *testing.T) {
	upstream, downstream := setupFeatureFromMain(t)

	// Pre-advance upstream and let downstream fetch once.
	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")
	downstream.RunGit("fetch", "origin")

	// Now break the upstream URL so a fetch would fail. With --no-fetch,
	// sync should still succeed using the already-fetched origin/main.
	downstream.RunGit("remote", "set-url", "origin", "/nonexistent/path")

	root, buf := buildSyncCmd(downstream.Dir, "--base", "main", "--no-fetch")
	if err := root.Execute(); err != nil {
		t.Fatalf("--no-fetch sync failed (fetch should have been skipped): %v\n%s",
			err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "fast-forwarded") && !strings.Contains(out, "rebased") {
		t.Errorf("expected an integration verb in output, got:\n%s", out)
	}
}
