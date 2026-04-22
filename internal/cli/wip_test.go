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

func buildWipCmd(repoDir string, use string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	runE := runWip
	if use == "unwip" {
		runE = runUnwip
	}
	c := &cobra.Command{Use: use, RunE: runE}
	testRoot.AddCommand(c)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs([]string{"--repo", repoDir, use})
	return testRoot, buf
}

// TestWip_CommitsThenUnwipRestores round-trips a WIP commit.
func TestWip_CommitsThenUnwipRestores(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("wip.txt", "work in progress\n")

	// gk wip
	root, buf := buildWipCmd(repo.Dir, "wip")
	if err := root.Execute(); err != nil {
		t.Fatalf("wip: %v\nout: %s", err, buf.String())
	}
	subj := repo.RunGit("log", "-1", "--format=%s")
	if !strings.HasPrefix(subj, "--wip--") {
		t.Fatalf("expected --wip-- subject, got %q", subj)
	}

	// gk unwip
	root2, buf2 := buildWipCmd(repo.Dir, "unwip")
	if err := root2.Execute(); err != nil {
		t.Fatalf("unwip: %v\nout: %s", err, buf2.String())
	}
	subjAfter := repo.RunGit("log", "-1", "--format=%s")
	if strings.HasPrefix(subjAfter, "--wip--") {
		t.Errorf("unwip left a wip commit: %q", subjAfter)
	}
	// file should still be untracked/modified
	status := repo.RunGit("status", "--porcelain")
	if !strings.Contains(status, "wip.txt") {
		t.Errorf("expected wip.txt in status after unwip, got: %q", status)
	}
}

// TestWip_CleanTreeIsNoop reports cleanly when there is nothing to commit.
func TestWip_CleanTreeIsNoop(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	root, buf := buildWipCmd(repo.Dir, "wip")
	if err := root.Execute(); err != nil {
		t.Fatalf("wip on clean tree: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing to wip") {
		t.Errorf("expected 'nothing to wip' message, got: %q", buf.String())
	}
}

// TestUnwip_RefusesOnNonWip errors when HEAD is not a wip commit.
func TestUnwip_RefusesOnNonWip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	root, _ := buildWipCmd(repo.Dir, "unwip")
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "not a wip") {
		t.Errorf("expected 'not a wip' error, got: %v", err)
	}
}

func TestStagingIsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}

	empty, err := stagingIsEmpty(context.Background(), runner)
	if err != nil {
		t.Fatalf("stagingIsEmpty: %v", err)
	}
	if !empty {
		t.Error("expected empty staging on fresh repo")
	}

	repo.WriteFile("a.txt", "a")
	repo.RunGit("add", "a.txt")

	empty2, err := stagingIsEmpty(context.Background(), runner)
	if err != nil {
		t.Fatalf("stagingIsEmpty: %v", err)
	}
	if empty2 {
		t.Error("expected non-empty staging after git add")
	}
}
