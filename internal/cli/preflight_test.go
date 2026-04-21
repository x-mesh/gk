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

// ---------------------------------------------------------------------------
// Unit tests — runShellStep
// ---------------------------------------------------------------------------

func TestRunShellStep_Pass(t *testing.T) {
	err := runShellStep(context.Background(), "true")
	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

func TestRunShellStep_Fail(t *testing.T) {
	err := runShellStep(context.Background(), "exit 7")
	if err == nil {
		t.Fatal("expected error for exit 7, got nil")
	}
}

func TestRunShellStep_OutputInError(t *testing.T) {
	err := runShellStep(context.Background(), "echo bad; exit 1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("expected 'bad' in error message, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unit tests — resolveDescription
// ---------------------------------------------------------------------------

func TestResolveDescription(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"commit-lint", "[builtin] lint HEAD commit message"},
		{"branch-check", "[builtin] validate branch name against patterns"},
		{"no-conflict", "[builtin] pre-merge conflict scan vs base"},
		{"make test", "[shell] make test"},
		{"npm run lint", "[shell] npm run lint"},
	}
	for _, tc := range cases {
		got := resolveDescription(tc.cmd)
		if got != tc.want {
			t.Errorf("resolveDescription(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration tests — built-in commit-lint
// ---------------------------------------------------------------------------

func TestBuiltinCommitLint_Pass(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hello")
	repo.Commit("feat: add hello file")

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	err := runBuiltinCommitLint(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("expected nil error for valid commit, got: %v", err)
	}
}

func TestBuiltinCommitLint_Fail(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hello")
	repo.Commit("random: invalid type commit")

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	err := runBuiltinCommitLint(context.Background(), runner, &cfg)
	if err == nil {
		t.Fatal("expected error for invalid commit type, got nil")
	}
	if !strings.Contains(err.Error(), "[type-enum]") {
		t.Fatalf("expected [type-enum] in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration tests — built-in branch-check
// ---------------------------------------------------------------------------

func TestBuiltinBranchCheck_Pass(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("feat/foo")

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	err := runBuiltinBranchCheck(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("expected nil error for valid branch name, got: %v", err)
	}
}

func TestBuiltinBranchCheck_Fail(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("WEIRD_NAME")

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	err := runBuiltinBranchCheck(context.Background(), runner, &cfg)
	if err == nil {
		t.Fatal("expected error for non-matching branch name, got nil")
	}
}

func TestBuiltinBranchCheck_Protected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	// "main" is the default branch from testutil.NewRepo.
	repo := testutil.NewRepo(t)

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	// "main" is in cfg.Branch.Protected — should pass even without matching pattern.
	err := runBuiltinBranchCheck(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("expected nil error for protected branch 'main', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration tests — built-in no-conflict
// ---------------------------------------------------------------------------

func TestBuiltinNoConflict_NoUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	// Fresh repo with no remote — should pass (inconclusive).
	repo := testutil.NewRepo(t)

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	err := runBuiltinNoConflict(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("expected nil (inconclusive pass) for repo without upstream, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helper — build a preflight cobra command rooted at a test root
// ---------------------------------------------------------------------------

func buildPreflightCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	preflightCmd := &cobra.Command{
		Use:          "preflight",
		RunE:         runPreflight,
		SilenceUsage: true,
	}
	preflightCmd.Flags().Bool("dry-run", false, "print steps without executing")
	preflightCmd.Flags().Bool("continue-on-failure", false, "keep running after failure")
	preflightCmd.Flags().StringSlice("skip", nil, "step names to skip")
	testRoot.AddCommand(preflightCmd)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	allArgs := append([]string{"--repo", repoDir, "preflight"}, extraArgs...)
	testRoot.SetArgs(allArgs)

	return testRoot, buf
}

// ---------------------------------------------------------------------------
// Cobra command integration tests
// ---------------------------------------------------------------------------

func TestPreflightCmd_DryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi")
	repo.Commit("feat: bootstrap")

	root, buf := buildPreflightCmd(repo.Dir, "--dry-run")
	if err := root.Execute(); err != nil {
		t.Fatalf("expected no error in dry-run, got: %v\noutput: %s", err, buf.String())
	}

	out := buf.String()
	// Expect all three default step names to appear.
	for _, step := range []string{"commit-lint", "branch-check", "no-conflict"} {
		if !strings.Contains(out, step) {
			t.Errorf("expected step %q in dry-run output, got:\n%s", step, out)
		}
	}
	// Expect [builtin] marker for each alias.
	if !strings.Contains(out, "[builtin]") {
		t.Errorf("expected [builtin] marker in dry-run output, got:\n%s", out)
	}
}

func TestPreflightCmd_SkipsNamed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi")
	repo.Commit("feat: bootstrap")

	root, buf := buildPreflightCmd(repo.Dir, "--dry-run", "--skip", "commit-lint")
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, buf.String())
	}

	out := buf.String()
	if !strings.Contains(out, "skipped") {
		t.Errorf("expected 'skipped' in output for commit-lint, got:\n%s", out)
	}
	if !strings.Contains(out, "commit-lint") {
		t.Errorf("expected 'commit-lint' name in output, got:\n%s", out)
	}
}

func TestPreflightCmd_StopsOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi")
	// Commit with invalid type — commit-lint step will fail.
	repo.Commit("badtype: should fail linting")

	root, buf := buildPreflightCmd(repo.Dir)
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected error when commit-lint fails, got nil\noutput: %s", buf.String())
	}

	out := buf.String()
	// commit-lint should be marked failed.
	if !strings.Contains(out, "commit-lint") {
		t.Errorf("expected 'commit-lint' in output, got:\n%s", out)
	}
	// branch-check should NOT appear (early exit).
	if strings.Contains(out, "ok") && strings.Contains(out, "branch-check") {
		t.Errorf("branch-check should not have run after commit-lint failure, got:\n%s", out)
	}
}

func TestPreflightCmd_ContinueOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi")
	// Invalid commit type — commit-lint will fail but we continue.
	repo.Commit("badtype: should fail linting")

	root, buf := buildPreflightCmd(repo.Dir, "--continue-on-failure")
	err := root.Execute()
	// Should still return an error summarising failures.
	if err == nil {
		t.Fatalf("expected final error with --continue-on-failure, got nil\noutput: %s", buf.String())
	}
	if !strings.Contains(err.Error(), "step(s) failed") {
		t.Errorf("expected 'step(s) failed' in error, got: %v", err)
	}

	out := buf.String()
	// branch-check should also have been attempted.
	if !strings.Contains(out, "branch-check") {
		t.Errorf("expected branch-check to run with --continue-on-failure, got:\n%s", out)
	}
}
