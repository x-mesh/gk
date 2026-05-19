package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestSplitRemoteRef covers the --remote vs embedded-slash precedence.
func TestSplitRemoteRef(t *testing.T) {
	cases := []struct {
		name, target, remoteFlag, cfgRemote string
		wantRemote, wantRef                 string
	}{
		{"slash target", "origin/main", "", "", "origin", "main"},
		{"flag wins over slash", "origin/main", "upstream", "", "upstream", "origin/main"},
		{"no slash, cfg fallback", "main", "", "upstream", "upstream", "main"},
		{"no slash, origin default", "main", "", "", "origin", "main"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ref := splitRemoteRef(tc.target, tc.remoteFlag, tc.cfgRemote)
			if r != tc.wantRemote || ref != tc.wantRef {
				t.Errorf("splitRemoteRef(%q,%q,%q) = (%q,%q), want (%q,%q)",
					tc.target, tc.remoteFlag, tc.cfgRemote, r, ref, tc.wantRemote, tc.wantRef)
			}
		})
	}
}

// TestResolveResetTarget_ExplicitWins bypasses upstream lookup when --to set.
func TestResolveResetTarget_ExplicitWins(t *testing.T) {
	fake := &git.FakeRunner{}
	got, err := resolveResetTarget(context.Background(), fake, "main", "origin/dev")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "origin/dev" {
		t.Errorf("got %q, want origin/dev", got)
	}
}

// TestResolveResetTarget_NoUpstream returns an error with a hint.
func TestResolveResetTarget_NoUpstream(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref --symbolic-full-name main@{upstream}": {
				Stderr:   "fatal: no upstream configured",
				ExitCode: 128,
			},
		},
	}
	_, err := resolveResetTarget(context.Background(), fake, "main", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no upstream") {
		t.Errorf("unexpected error: %v", err)
	}
}

// buildResetCmd wires a minimal cobra root with reset for tests.
func buildResetCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	rs := &cobra.Command{Use: "reset", Args: cobra.MaximumNArgs(1), RunE: runReset}
	rs.Flags().String("to", "", "")
	rs.Flags().Bool("to-remote", false, "")
	rs.Flags().String("remote", "", "")
	rs.Flags().BoolP("yes", "y", false, "")
	rs.Flags().Bool("clean", false, "")
	rs.Flags().Bool("dry-run", false, "")
	testRoot.AddCommand(rs)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs(append([]string{"--repo", repoDir, "reset"}, extraArgs...))
	return testRoot, buf
}

// TestReset_DryRunShowsPlan does not call fetch/reset and prints the plan.
func TestReset_DryRunShowsPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	// Build a remote repo so our repo has an upstream.
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "a")
	upstream.Commit("a")

	repo := testutil.NewRepo(t)
	repo.AddRemote("origin", upstream.Dir)
	repo.RunGit("fetch", "origin")
	// set upstream on main -> origin/main
	repo.RunGit("branch", "--set-upstream-to=origin/main", "main")

	root, buf := buildResetCmd(repo.Dir, "--dry-run")
	if err := root.Execute(); err != nil {
		t.Fatalf("reset --dry-run failed: %v\nout: %s", err, buf.String())
	}

	out := buf.String()
	if !strings.Contains(out, "target:") || !strings.Contains(out, "origin/main") {
		t.Errorf("expected target: origin/main in output, got:\n%s", out)
	}
	if !strings.Contains(out, "[dry-run]") {
		t.Errorf("expected dry-run marker, got:\n%s", out)
	}
}

// TestReset_NonTTYRequiresYes refuses without --yes when not a TTY.
func TestReset_NonTTYRequiresYes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "a")
	upstream.Commit("a")
	repo := testutil.NewRepo(t)
	repo.AddRemote("origin", upstream.Dir)
	repo.RunGit("fetch", "origin")
	repo.RunGit("branch", "--set-upstream-to=origin/main", "main")

	root, _ := buildResetCmd(repo.Dir)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error without --yes in non-TTY")
	}
	if !strings.Contains(err.Error(), "confirmation") && !strings.Contains(err.Error(), "TTY") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestReset_ToRemoteResolvesBranch verifies --to-remote derives origin/<current>
// even when no upstream is configured.
func TestReset_ToRemoteResolvesBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "a")
	upstream.Commit("a")

	repo := testutil.NewRepo(t)
	repo.AddRemote("origin", upstream.Dir)
	repo.RunGit("fetch", "origin")
	// Deliberately do NOT --set-upstream-to: --to-remote must work without it.

	root, buf := buildResetCmd(repo.Dir, "--to-remote", "--dry-run")
	if err := root.Execute(); err != nil {
		t.Fatalf("reset --to-remote --dry-run failed: %v\nout: %s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "target:") || !strings.Contains(out, "origin/main") {
		t.Errorf("expected target: origin/main, got:\n%s", out)
	}
}

// TestReset_ToRemoteAndToMutex rejects --to + --to-remote together.
func TestReset_ToRemoteAndToMutex(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	root, _ := buildResetCmd(repo.Dir, "--to-remote", "--to", "origin/main", "--dry-run")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually-exclusive error, got %v", err)
	}
}

// TestReset_PositionalRefIsToAlias verifies `gk reset <ref>` targets <ref>
// instead of silently ignoring it (AC1).
func TestReset_PositionalRefIsToAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("a")

	root, buf := buildResetCmd(repo.Dir, "main", "--dry-run")
	if err := root.Execute(); err != nil {
		t.Fatalf("reset main --dry-run failed: %v\nout: %s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "target:  main") {
		t.Errorf("expected positional ref to become target: main, got:\n%s", out)
	}
}

// TestReset_PositionalAndToConflict rejects a positional ref together with --to (AC1).
func TestReset_PositionalAndToConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("a")

	root, _ := buildResetCmd(repo.Dir, "main", "--to", "dev", "--dry-run")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "cannot combine positional ref") {
		t.Errorf("expected positional+--to conflict error, got %v", err)
	}
}

// TestReset_DetachedDuringRebaseHint points at gk abort/continue, not gk switch (AC2).
func TestReset_DetachedDuringRebaseHint(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("a")
	// Detach HEAD and fake an in-progress rebase so gitstate.Detect reports it.
	repo.RunGit("checkout", "--detach")
	if err := os.MkdirAll(filepath.Join(repo.Dir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatalf("mkdir rebase-merge: %v", err)
	}

	root, _ := buildResetCmd(repo.Dir, "--to", "main", "--dry-run")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected reset to fail on detached HEAD")
	}
	hint := HintFrom(err)
	if !strings.Contains(hint, "gk abort") || !strings.Contains(hint, "gk continue") {
		t.Errorf("hint should suggest gk abort/continue, got %q", hint)
	}
	if strings.Contains(hint, "gk switch") {
		t.Errorf("hint should not suggest gk switch during rebase, got %q", hint)
	}
}

// TestReset_YesActuallyResets verifies -y performs fetch + hard reset.
func TestReset_YesActuallyResets(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("a.txt", "a")
	upstream.Commit("a")

	repo := testutil.NewRepo(t)
	repo.AddRemote("origin", upstream.Dir)
	repo.RunGit("fetch", "origin")
	repo.RunGit("branch", "--set-upstream-to=origin/main", "main")

	// Add a local-only commit that should be discarded.
	repo.WriteFile("local.txt", "local-only")
	localSHA := repo.Commit("local wip")

	root, buf := buildResetCmd(repo.Dir, "--yes")
	if err := root.Execute(); err != nil {
		t.Fatalf("reset --yes failed: %v\nout: %s", err, buf.String())
	}

	cur := repo.RunGit("rev-parse", "HEAD")
	if cur == localSHA {
		t.Errorf("HEAD (%s) still points to local commit; expected remote HEAD", cur)
	}
}
