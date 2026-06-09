package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// buildSnapshotCmd assembles the `gk snapshot` command tree (save + list +
// restore) under a throwaway root so tests can drive it via SetArgs.
func buildSnapshotCmd(repoDir string, args ...string) (*cobra.Command, *bytes.Buffer) {
	root := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	root.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	snap := &cobra.Command{Use: "snapshot", Args: cobra.NoArgs, RunE: runSnapshotSave}
	snap.Flags().StringP("message", "m", "", "")
	snap.Flags().BoolP("quiet", "q", false, "")

	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: runSnapshotList}
	snap.AddCommand(list)

	restore := &cobra.Command{Use: "restore", Args: cobra.MaximumNArgs(1), RunE: runSnapshotRestore}
	restore.Flags().StringP("message", "m", "", "")
	snap.AddCommand(restore)

	root.AddCommand(snap)

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(append([]string{"--repo", repoDir, "--no-color"}, args...))
	return root, buf
}

func runSnap(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	root, buf := buildSnapshotCmd(repoDir, args...)
	if err := root.Execute(); err != nil {
		t.Fatalf("gk snapshot %s: %v\nout: %s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String()
}

// TestSnapshot_CleanTreeIsNoop verifies a clean tree creates no ref.
func TestSnapshot_CleanTreeIsNoop(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	out := runSnap(t, repo.Dir, "snapshot")
	if !strings.Contains(out, "nothing to snapshot") {
		t.Fatalf("expected clean-tree message, got: %s", out)
	}
	if _, err := repo.TryGit("rev-parse", "--verify", "--quiet", "refs/wip/main"); err == nil {
		t.Fatal("refs/wip/main should not exist for a clean tree")
	}
}

// TestSnapshot_CapturesUntrackedAndPreservesTree is the core safety-net
// guarantee: untracked files are captured and the working tree/index are not
// disturbed.
func TestSnapshot_CapturesUntrackedAndPreservesTree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("tracked.txt", "v1\n")
	repo.RunGit("add", "tracked.txt")
	repo.RunGit("commit", "-m", "add tracked")

	// Modify tracked, add an untracked file, and stage nothing extra.
	repo.WriteFile("tracked.txt", "v2-working\n")
	repo.WriteFile("new.txt", "brand new\n")

	statusBefore := repo.RunGit("status", "--porcelain")

	out := runSnap(t, repo.Dir, "snapshot", "-m", "wip note")
	if !strings.Contains(out, "snapshot saved") {
		t.Fatalf("expected save confirmation, got: %s", out)
	}

	// Ref exists and the snapshot tree contains both files at working-tree state.
	tree := repo.RunGit("ls-tree", "-r", "--name-only", "refs/wip/main")
	if !strings.Contains(tree, "new.txt") || !strings.Contains(tree, "tracked.txt") {
		t.Fatalf("snapshot tree missing files: %q", tree)
	}
	got := repo.RunGit("show", "refs/wip/main:tracked.txt")
	if got != "v2-working" {
		t.Fatalf("snapshot should hold working-tree content, got %q", got)
	}

	// Working tree and index untouched.
	if statusAfter := repo.RunGit("status", "--porcelain"); statusAfter != statusBefore {
		t.Fatalf("working tree changed: before=%q after=%q", statusBefore, statusAfter)
	}
	if head := repo.RunGit("log", "-1", "--format=%s"); head != "add tracked" {
		t.Fatalf("HEAD moved: %q", head)
	}
}

// TestSnapshot_RestoreRoundTrip saves a snapshot, mutates/deletes files, then
// restores and confirms the snapshot content comes back.
func TestSnapshot_RestoreRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "original\n")
	out := runSnap(t, repo.Dir, "snapshot")
	if !strings.Contains(out, "snapshot saved") {
		t.Fatalf("save failed: %s", out)
	}

	// Drop the file entirely, then restore the latest snapshot.
	repo.RunGit("clean", "-fd")
	if _, err := repo.TryGit("cat-file", "-e", "HEAD:a.txt"); err == nil {
		t.Fatal("precondition: a.txt should not be committed")
	}

	out = runSnap(t, repo.Dir, "snapshot", "restore")
	if !strings.Contains(out, "restored") {
		t.Fatalf("restore failed: %s", out)
	}
	got := repo.RunGit("show", ":a.txt") // index after checkout
	if got != "original" {
		t.Fatalf("restored content wrong: %q", got)
	}
}

// TestSnapshot_RestoreBacksUpDirtyTree verifies a dirty tree is snapshotted
// before restore so no work is lost.
func TestSnapshot_RestoreBacksUpDirtyTree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "snapshot-state\n")
	runSnap(t, repo.Dir, "snapshot") // @{0}

	// Now dirty the tree differently, without snapshotting.
	repo.WriteFile("a.txt", "uncommitted-edit\n")

	// @{1} does not exist (only one snapshot), so restore 1 must error.
	root, buf := buildSnapshotCmd(repo.Dir, "snapshot", "restore", "1")
	if err := root.Execute(); err == nil {
		t.Fatalf("expected error restoring non-existent @{1}, out: %s", buf.String())
	}

	// Restoring @{0} should first back up the dirty edit as a new snapshot.
	out := runSnap(t, repo.Dir, "snapshot", "restore", "0")
	if !strings.Contains(out, "saved as the latest snapshot") {
		t.Fatalf("expected auto-backup notice, got: %s", out)
	}
	// The dirty edit is now preserved as @{0}; restored state is on disk.
	if got := repo.RunGit("show", ":a.txt"); got != "snapshot-state" {
		t.Fatalf("expected restored snapshot content, got %q", got)
	}
	backup := repo.RunGit("show", "refs/wip/main@{0}:a.txt")
	if backup != "uncommitted-edit" {
		t.Fatalf("dirty edit not backed up, got %q", backup)
	}
}

// TestSnapshot_ListShowsEntries checks the list output references @{n}.
func TestSnapshot_ListShowsEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)

	out := runSnap(t, repo.Dir, "snapshot", "list")
	if !strings.Contains(out, "no snapshots") {
		t.Fatalf("expected empty-list message, got: %s", out)
	}

	repo.WriteFile("a.txt", "one\n")
	runSnap(t, repo.Dir, "snapshot", "-m", "first")
	repo.WriteFile("a.txt", "two\n")
	runSnap(t, repo.Dir, "snapshot", "-m", "second")

	out = runSnap(t, repo.Dir, "snapshot", "list")
	if !strings.Contains(out, "@{0}") || !strings.Contains(out, "@{1}") {
		t.Fatalf("list should show @{0} and @{1}, got: %s", out)
	}
	if !strings.Contains(out, "second") || !strings.Contains(out, "first") {
		t.Fatalf("list should show notes, got: %s", out)
	}
}
