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

func buildWipeCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	c := &cobra.Command{Use: "wipe", RunE: runWipe}
	c.Flags().BoolP("yes", "y", false, "")
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("include-ignored", false, "")
	testRoot.AddCommand(c)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs(append([]string{"--repo", repoDir, "wipe"}, extraArgs...))
	return testRoot, buf
}

// TestWipe_DryRunShowsPlan lists actions without touching the tree.
func TestWipe_DryRunShowsPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("extra.txt", "untracked\n")

	root, buf := buildWipeCmd(repo.Dir, "--dry-run")
	if err := root.Execute(); err != nil {
		t.Fatalf("wipe --dry-run: %v\nout: %s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "[dry-run]") {
		t.Errorf("expected dry-run marker, got:\n%s", out)
	}
	// untracked file should still exist
	status := repo.RunGit("status", "--porcelain")
	if !strings.Contains(status, "extra.txt") {
		t.Errorf("extra.txt should still be untracked after dry-run")
	}
}

// TestWipe_YesRemovesUntracked runs a real wipe and leaves a backup ref.
func TestWipe_YesRemovesUntracked(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("extra.txt", "untracked\n")
	preSHA := repo.RunGit("rev-parse", "HEAD")

	root, buf := buildWipeCmd(repo.Dir, "--yes")
	if err := root.Execute(); err != nil {
		t.Fatalf("wipe --yes: %v\nout: %s", err, buf.String())
	}

	status := repo.RunGit("status", "--porcelain")
	if strings.Contains(status, "extra.txt") {
		t.Errorf("extra.txt should be gone after wipe, status=%q", status)
	}

	// Look for any backup ref under refs/gk/wipe-backup/main/
	refs := repo.RunGit("for-each-ref", "--format=%(refname)", "refs/gk/wipe-backup/")
	if !strings.Contains(refs, "refs/gk/wipe-backup/main/") {
		t.Errorf("expected wipe-backup ref, got: %q", refs)
	}

	// Backup ref should point at pre-wipe HEAD.
	lines := strings.Split(strings.TrimSpace(refs), "\n")
	if len(lines) > 0 {
		backupSHA := repo.RunGit("rev-parse", lines[0])
		if backupSHA != preSHA {
			t.Errorf("backup sha %s != pre sha %s", backupSHA, preSHA)
		}
	}
}

// TestWipe_NonTTYRequiresYes refuses without --yes in non-TTY mode.
func TestWipe_NonTTYRequiresYes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	root, _ := buildWipeCmd(repo.Dir)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error without --yes in non-TTY")
	}
	if !strings.Contains(err.Error(), "confirmation") {
		t.Errorf("expected confirmation error, got %v", err)
	}
}

// TestWipe_RefusesInProgressRebase — SHARED-01 preflight addition.
// Before the gitsafe extraction, `gk wipe` ran through even during a rebase,
// leaving the repo half-broken. This test proves the preflight blocks.
func TestWipe_RefusesInProgressRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.Commit("init")

	// Simulate in-progress rebase via the marker directory gitstate checks for.
	if err := os.MkdirAll(filepath.Join(repo.Dir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatalf("mkdir rebase-merge: %v", err)
	}

	root, _ := buildWipeCmd(repo.Dir, "--yes")
	err := root.Execute()
	if err == nil {
		t.Fatal("expected wipe to refuse during rebase, got nil")
	}
	if !strings.Contains(err.Error(), "in-progress") {
		t.Errorf("expected 'in-progress' in error, got: %v", err)
	}
}
