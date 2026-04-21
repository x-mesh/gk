package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// buildLintCommitCmd wires a minimal cobra tree with the lint-commit command
// for testing, targeting the given repoDir.
func buildLintCommitCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	lintCmd := &cobra.Command{
		Use:  "lint-commit [<rev-range>]",
		RunE: runLintCommit,
	}
	lintCmd.Flags().String("file", "", "validate a single message from file")
	lintCmd.Flags().Bool("staged", false, "validate .git/COMMIT_EDITMSG")
	testRoot.AddCommand(lintCmd)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	allArgs := append([]string{"--repo", repoDir, "lint-commit"}, extraArgs...)
	testRoot.SetArgs(allArgs)

	return testRoot, buf
}

func TestLintCommit_HEAD_Valid(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hello")
	repo.Commit("feat: init project")

	// Use HEAD~1..HEAD to lint only the last commit, not the initial "initial" commit
	// created by testutil.NewRepo which does not follow Conventional Commits.
	root, buf := buildLintCommitCmd(repo.Dir, "HEAD~1..HEAD")
	err := root.Execute()
	if err != nil {
		t.Fatalf("expected no error, got: %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "✓") {
		t.Errorf("expected pass indicator, got: %s", buf.String())
	}
}

func TestLintCommit_HEAD_InvalidType(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hello")
	repo.Commit("random: foo bar")

	// Lint only the last commit so the initial "initial" commit from testutil.NewRepo
	// does not interfere.
	root, buf := buildLintCommitCmd(repo.Dir, "HEAD~1..HEAD")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "[type-enum]") {
		t.Errorf("expected [type-enum] in output, got: %s", out)
	}
}

func TestLintCommit_RevRange_3(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	repo.WriteFile("a.txt", "a")
	repo.Commit("feat: add a")
	repo.WriteFile("b.txt", "b")
	repo.Commit("badtype: invalid commit")
	repo.WriteFile("c.txt", "c")
	repo.Commit("fix: correct issue")

	root, buf := buildLintCommitCmd(repo.Dir, "HEAD~3..HEAD")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for invalid commit, got nil")
	}
	out := buf.String()
	// The invalid commit should show type-enum error.
	if !strings.Contains(out, "[type-enum]") {
		t.Errorf("expected [type-enum] in output, got: %s", out)
	}
	// The valid commits should show pass indicator.
	passCount := strings.Count(out, "✓")
	if passCount < 2 {
		t.Errorf("expected at least 2 pass indicators for valid commits, got %d\noutput: %s", passCount, out)
	}
}

func TestLintCommit_File(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	msgFile := filepath.Join(t.TempDir(), "commit-msg")
	if err := os.WriteFile(msgFile, []byte("feat: add great feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, buf := buildLintCommitCmd(repo.Dir, "--file", msgFile)
	err := root.Execute()
	if err != nil {
		t.Fatalf("expected no error, got: %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "✓") {
		t.Errorf("expected pass indicator, got: %s", buf.String())
	}
}

func TestLintCommit_File_WithComments(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	content := "# This is a git comment\nfeat: add feature with comments\n# Another comment\n"
	msgFile := filepath.Join(t.TempDir(), "commit-msg")
	if err := os.WriteFile(msgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	root, buf := buildLintCommitCmd(repo.Dir, "--file", msgFile)
	err := root.Execute()
	if err != nil {
		t.Fatalf("expected no error (comments should be stripped), got: %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "✓") {
		t.Errorf("expected pass indicator, got: %s", buf.String())
	}
}

func TestLintCommit_Staged(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	// Write a valid message into .git/COMMIT_EDITMSG
	editmsg := filepath.Join(repo.GitDir, "COMMIT_EDITMSG")
	if err := os.WriteFile(editmsg, []byte("chore: update dependencies\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, buf := buildLintCommitCmd(repo.Dir, "--staged")
	err := root.Execute()
	if err != nil {
		t.Fatalf("expected no error, got: %v\noutput: %s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "✓") {
		t.Errorf("expected pass indicator, got: %s", buf.String())
	}
}
