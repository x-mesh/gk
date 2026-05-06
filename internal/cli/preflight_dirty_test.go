package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestGuardWorkingTreeReady_Clean — passes through when there are no
// unmerged paths, which is the common case for every pull/sync/merge.
func TestGuardWorkingTreeReady_Clean(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}

	if err := guardWorkingTreeReady(context.Background(), runner, "pull"); err != nil {
		t.Fatalf("expected nil on clean tree, got %v", err)
	}
}

// TestGuardWorkingTreeReady_Unmerged — the regression case. A merge
// conflict left the tree with unmerged paths; guard must refuse with
// a hint that names the operation and at least one path.
func TestGuardWorkingTreeReady_Unmerged(t *testing.T) {
	repo := mkConflictedRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}

	err := guardWorkingTreeReady(context.Background(), runner, "pull")
	if err == nil {
		t.Fatal("expected error when unmerged paths exist")
	}
	msg := err.Error()
	if !strings.Contains(msg, "pull") {
		t.Errorf("error should name the op (pull): %q", msg)
	}
	if !strings.Contains(msg, "unmerged") {
		t.Errorf("error should mention unmerged: %q", msg)
	}
	hint := HintFrom(err)
	if !strings.Contains(hint, "merge --continue") {
		t.Errorf("hint should suggest the matching --continue verb when MERGE_HEAD is set: %q", hint)
	}
}

// TestPreviewPaths_Truncates — the preview helper protects multi-file
// errors from spamming the terminal. 5 paths over a max of 4 should
// produce 4 + a "(+1 more)" tail.
func TestPreviewPaths_Truncates(t *testing.T) {
	got := previewPaths([]string{"a", "b", "c", "d", "e"}, 4)
	if len(got) != 5 || got[4] != "... (+1 more)" {
		t.Errorf("expected 4 paths + tail, got %v", got)
	}
}

func TestPreviewPaths_NoTruncate(t *testing.T) {
	got := previewPaths([]string{"a", "b"}, 4)
	if len(got) != 2 {
		t.Errorf("expected 2 paths unchanged, got %v", got)
	}
}

// TestDiagnoseStashFailure_StaleLock — the index.lock probe should win
// when present, since it's the cheapest-to-fix and most common silent
// failure.
func TestDiagnoseStashFailure_StaleLock(t *testing.T) {
	repo := testutil.NewRepo(t)
	lock := filepath.Join(repo.Dir, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("stale"), 0o644); err != nil {
		t.Fatalf("create lock: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })

	runner := &git.ExecRunner{Dir: repo.Dir}
	hint := diagnoseStashFailure(context.Background(), runner)
	if !strings.Contains(hint, "index.lock") {
		t.Errorf("hint should call out the lock file: %q", hint)
	}
}

// TestDiagnoseStashFailure_Unmerged — when conflicts are pending, the
// hint must surface them so the user knows to run `gk resolve` rather
// than chasing a phantom git issue.
func TestDiagnoseStashFailure_Unmerged(t *testing.T) {
	repo := mkConflictedRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}

	hint := diagnoseStashFailure(context.Background(), runner)
	if !strings.Contains(hint, "unmerged") {
		t.Errorf("hint should mention unmerged: %q", hint)
	}
	if !strings.Contains(hint, "gk resolve") {
		t.Errorf("hint should suggest gk resolve: %q", hint)
	}
}

// TestGuardWorkingTreeReady_UnmergedNoOp — `git stash apply`,
// `git apply --3way`, and a few partial-reset paths produce unmerged
// stages without writing any of the in-progress op markers. The hint
// must steer those users at `git add` / `git checkout --` instead of
// the misleading `git rebase --continue` advice they'd hit a dead
// end on.
func TestGuardWorkingTreeReady_UnmergedNoOp(t *testing.T) {
	repo := mkConflictedRepo(t)
	// Strip the merge marker without resolving the index — leaves
	// the working tree in the exact "stash apply mid-conflict" shape.
	for _, name := range []string{"MERGE_HEAD", "MERGE_MSG", "MERGE_MODE"} {
		_ = os.Remove(filepath.Join(repo.Dir, ".git", name))
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	err := guardWorkingTreeReady(context.Background(), runner, "pull")
	if err == nil {
		t.Fatal("expected error when unmerged paths exist")
	}
	hint := HintFrom(err)
	if hint == "" {
		t.Fatalf("expected hint to be attached: %v", err)
	}
	if strings.Contains(hint, "rebase --continue") || strings.Contains(hint, "rebase --abort") {
		t.Errorf("hint must NOT suggest rebase --continue/--abort when no op is in progress: %q", hint)
	}
	if !strings.Contains(hint, "git add") {
		t.Errorf("hint should suggest `git add <files>` for the unmerged-only branch: %q", hint)
	}
}

// TestDiagnoseStashFailure_Generic — clean repo with no unmerged / no
// op / no lock returns the fall-through hint pointing at the raw
// command. Guards against the helper silently returning "" and the
// caller printing nothing useful.
func TestDiagnoseStashFailure_Generic(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}

	hint := diagnoseStashFailure(context.Background(), runner)
	if hint == "" {
		t.Fatal("hint should never be empty")
	}
	if !strings.Contains(hint, "git stash push") {
		t.Errorf("fall-through hint should suggest running stash directly: %q", hint)
	}
}

// mkConflictedRepo builds a repo whose working tree has unmerged
// paths via a deliberate two-branch merge collision. The resulting
// state is exactly what `git status` reports as "both modified".
func mkConflictedRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "base\n")
	repo.RunGit("add", "a.txt")
	repo.RunGit("commit", "-m", "base")
	repo.RunGit("checkout", "-b", "feature")
	repo.WriteFile("a.txt", "feature\n")
	repo.RunGit("commit", "-am", "feature change")
	repo.RunGit("checkout", "main")
	repo.WriteFile("a.txt", "main\n")
	repo.RunGit("commit", "-am", "main change")
	// Provoke the conflict; the merge fails with exit 1, which is
	// expected — we want the unmerged state, not a successful merge.
	_, _ = repo.TryGit("merge", "feature")
	return repo
}
