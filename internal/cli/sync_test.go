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

// buildSyncCmd wires a cobra root + `sync` subcommand targeting repoDir.
// Mirrors buildPrecheckCmd / buildPreflightCmd.
//
// NOTE: tests in this file exercise the v0.6 sync semantics that now live
// behind --upstream-only. They are kept skipped until t9 rewrites the suite
// for the new "catch up to base" sync — see docs/rfc-sync-redesign.md.
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
	sync.Flags().Bool("all", false, "sync every local branch")
	sync.Flags().Bool("fetch-only", false, "fetch only")
	sync.Flags().Bool("no-fetch", false, "skip fetch")
	sync.Flags().Bool("autostash", false, "autostash")
	testRoot.AddCommand(sync)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	allArgs := append([]string{"--repo", repoDir, "sync"}, extraArgs...)
	testRoot.SetArgs(allArgs)
	return testRoot, buf
}

// setupTrackingDownstream returns (upstream, downstream) repos where
// downstream/main tracks upstream/main via a local-path remote.
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
// Unit — upstreamOf, resolveShortSHA, equalRefs (against real git)
// ---------------------------------------------------------------------------

func TestUpstreamOf_NoUpstream(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	got, _ := upstreamOf(context.Background(), execRunnerFor(repo), "main")
	if got != "" {
		t.Errorf("expected empty for no upstream, got %q", got)
	}
}

func TestUpstreamOf_WithUpstream(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
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
// Integration — full sync command
// ---------------------------------------------------------------------------

func TestSyncCmd_NoUpstream(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, buf := buildSyncCmd(repo.Dir, "--no-fetch")
	if err := root.Execute(); err != nil {
		t.Fatalf("expected no error for no-upstream, got: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "no upstream") {
		t.Errorf("expected 'no upstream' in report, got:\n%s", out)
	}
}

func TestSyncCmd_AlreadyUpToDate(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	_, downstream := setupTrackingDownstream(t)

	root, buf := buildSyncCmd(downstream.Dir, "--no-fetch")
	if err := root.Execute(); err != nil {
		t.Fatalf("err: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "up to date") {
		t.Errorf("expected 'up to date', got:\n%s", buf.String())
	}
}

func TestSyncCmd_FastForwards(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	upstream, downstream := setupTrackingDownstream(t)

	// upstream advances
	upstream.WriteFile("b.txt", "second\n")
	upstreamHead := upstream.Commit("feat: add b")

	// Real fetch (using remote = upstream.Dir).
	root, buf := buildSyncCmd(downstream.Dir)
	if err := root.Execute(); err != nil {
		t.Fatalf("sync failed: %v\n%s", err, buf.String())
	}

	got := strings.TrimSpace(downstream.RunGit("rev-parse", "HEAD"))
	if got != upstreamHead {
		t.Errorf("downstream HEAD = %s, want %s", got, upstreamHead)
	}
	if !strings.Contains(buf.String(), "fast-forwarded") && !strings.Contains(buf.String(), "→") {
		t.Errorf("expected FF report, got:\n%s", buf.String())
	}
}

func TestSyncCmd_Diverged(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	upstream, downstream := setupTrackingDownstream(t)

	// upstream advances
	upstream.WriteFile("b.txt", "up\n")
	upstream.Commit("feat: up")

	// downstream also advances on main → diverged
	downstream.RunGit("fetch", "origin")
	downstream.WriteFile("c.txt", "down\n")
	downstream.Commit("feat: down")

	root, buf := buildSyncCmd(downstream.Dir, "--no-fetch")
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected DivergedError, got nil\n%s", buf.String())
	}
	var de *DivergedError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DivergedError, got %T: %v", err, err)
	}
	if de.Code != 4 {
		t.Errorf("expected exit 4, got %d", de.Code)
	}
	if !strings.Contains(buf.String(), "diverged") {
		t.Errorf("expected 'diverged' in report, got:\n%s", buf.String())
	}
}

func TestSyncCmd_FetchOnly(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	upstream, downstream := setupTrackingDownstream(t)
	upstream.WriteFile("b.txt", "later\n")
	upstreamHead := upstream.Commit("feat: later")

	root, buf := buildSyncCmd(downstream.Dir, "--fetch-only")
	if err := root.Execute(); err != nil {
		t.Fatalf("fetch-only failed: %v\n%s", err, buf.String())
	}
	// origin/main should point at upstreamHead, but local main should NOT have moved.
	originMain := strings.TrimSpace(downstream.RunGit("rev-parse", "origin/main"))
	if originMain != upstreamHead {
		t.Errorf("origin/main = %s, want %s", originMain, upstreamHead)
	}
	localMain := strings.TrimSpace(downstream.RunGit("rev-parse", "main"))
	if localMain == upstreamHead {
		t.Error("--fetch-only moved local main; should have skipped FF")
	}
}

func TestSyncCmd_MutexFlags(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	// --fetch-only + --no-fetch is still rejected in the new sync, so this
	// test stays valid. Force --base to skip auto-detection (current branch
	// is also the base in a fresh repo with no remote).
	root, _ := buildSyncCmd(repo.Dir, "--fetch-only", "--no-fetch")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected mutex error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive', got: %v", err)
	}
}

// execRunnerFor returns a live git.ExecRunner rooted at the repo directory.
func execRunnerFor(r *testutil.Repo) *git.ExecRunner {
	return &git.ExecRunner{Dir: r.Dir}
}

// untrackedDownstream returns a downstream repo whose `main` has the
// origin/main ref cached but lacks a configured upstream — exactly the
// scenario where mem-mesh's main fell out of sync silently. The upstream
// is advanced past the downstream by one commit so origin/main is "ahead".
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

	// upstream advances → origin/main now ahead by 1 after the next fetch.
	upstream.WriteFile("b.txt", "world\n")
	upstream.Commit("feat: b")
	downstream.RunGit("fetch", "origin")
	return upstream, downstream
}

func TestSyncCmd_NoUpstream_ImplicitDivergence(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	_, downstream := untrackedDownstream(t)

	root, buf := buildSyncCmd(downstream.Dir, "--no-fetch")
	if err := root.Execute(); err != nil {
		t.Fatalf("expected no error (skip), got: %v\n%s", err, buf.String())
	}
	out := buf.String()

	if !strings.Contains(out, "no upstream") {
		t.Errorf("expected 'no upstream' marker, got:\n%s", out)
	}
	if !strings.Contains(out, "origin/main differs") {
		t.Errorf("expected divergence detail, got:\n%s", out)
	}
	if !strings.Contains(out, "↑0 ↓1") {
		t.Errorf("expected '↑0 ↓1' (origin ahead by 1), got:\n%s", out)
	}
	if !strings.Contains(out, "fix: git branch --set-upstream-to=origin/main main") {
		t.Errorf("expected fix command, got:\n%s", out)
	}
	if strings.Contains(out, "skipped") {
		t.Errorf("legacy 'skipped' phrasing should not appear when implicit divergence detected, got:\n%s", out)
	}
}

func TestSyncCmd_NoUpstream_EqualToImplicit_KeepsLegacyPhrasing(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "hi\n")
	upstream.Commit("feat: a")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.RunGit("reset", "--hard", "origin/main")
	// Same SHA, no upstream set → should NOT emit the new hint.

	root, buf := buildSyncCmd(downstream.Dir, "--no-fetch")
	if err := root.Execute(); err != nil {
		t.Fatalf("err: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "no upstream configured — skipped") {
		t.Errorf("expected legacy 'skipped' phrasing for equal SHA, got:\n%s", out)
	}
	if strings.Contains(out, "differs") {
		t.Errorf("hint should not fire when SHAs match, got:\n%s", out)
	}
}

func TestSyncCmd_NoUpstream_ForkBranch_NoSameNamedRemote(t *testing.T) {
	t.Skip("legacy v0.6 sync test — rewritten in t9 against new catch-up-to-base semantics")
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")
	// No origin → no remote refs cached at all.

	root, buf := buildSyncCmd(repo.Dir, "--no-fetch")
	if err := root.Execute(); err != nil {
		t.Fatalf("err: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "no upstream configured — skipped") {
		t.Errorf("expected legacy 'skipped' phrasing for fork-style branch, got:\n%s", out)
	}
	if strings.Contains(out, "differs") {
		t.Errorf("hint should be silent when same-named remote ref is absent, got:\n%s", out)
	}
}
