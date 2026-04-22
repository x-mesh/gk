package cli

import (
	"bytes"
	"context"
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

	rs := &cobra.Command{Use: "reset", RunE: runReset}
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
