package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// breakSubmodule points the submodule's gitfile at a missing directory, so
// every git status in the worktree fails.
func breakSubmodule(t *testing.T, wtPath string) {
	t.Helper()
	gitfile := filepath.Join(wtPath, "vendor", "sub", ".git")
	if err := os.WriteFile(gitfile, []byte("gitdir: /nonexistent/modules/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorktreeRemove_DeinitSubmodulesStatusFailure separates a failed git
// status from uncommitted changes: "commit or stash" cannot clear a broken
// submodule, so an agent must get a different code.
func TestWorktreeRemove_DeinitSubmodulesStatusFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo, wtPath := newMergedSubmoduleWorktree(t)
	breakSubmodule(t, wtPath)
	rm, buf := buildWorktreeCmd(repo.Dir, "remove", "--deinit-submodules", wtPath)
	err := rm.Execute()
	var he *hintError
	if !errors.As(err, &he) || he.code != "worktree-status-failed" {
		t.Fatalf("want blocked code worktree-status-failed, got err=%v\n%s", err, buf.String())
	}
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Fatalf("worktree removed despite the failed check: %v", statErr)
	}
}

// TestCleanupSelectLoss_HiddenAndUnreadable covers the loss confirmation of
// cleanup -i: changes that submodule.<name>.ignore hides must be listed, and
// a worktree whose status cannot be read must not pass as clean.
func TestCleanupSelectLoss_HiddenAndUnreadable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	ctx := context.Background()

	repo, wtPath := newMergedSubmoduleWorktree(t)
	repo.RunGit("config", "submodule.vendor/sub.ignore", "all")
	if err := os.WriteFile(filepath.Join(wtPath, "vendor", "sub", "unsaved.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &git.ExecRunner{Dir: repo.Dir}
	for _, r := range []cleanupSelectRow{
		{entry: worktreeCleanupEntry{Path: wtPath, Branch: "feat/sub-wt", Action: cleanupActionRemove}, candidate: true},
		{entry: worktreeCleanupEntry{Path: wtPath, Branch: "feat/sub-wt", Reasons: []string{"protected"}}},
	} {
		if loss := cleanupSelectLoss(ctx, runner, &r); !strings.Contains(loss, "uncommitted") {
			t.Errorf("candidate=%v: hidden submodule change not listed, loss=%q", r.candidate, loss)
		}
	}

	broken, brokenPath := newMergedSubmoduleWorktree(t)
	breakSubmodule(t, brokenPath)
	r := cleanupSelectRow{entry: worktreeCleanupEntry{Path: brokenPath, Branch: "feat/sub-wt", Reasons: []string{"protected"}}}
	if loss := cleanupSelectLoss(ctx, &git.ExecRunner{Dir: broken.Dir}, &r); !strings.Contains(loss, "could not check") {
		t.Errorf("an unreadable worktree must be reported, loss=%q", loss)
	}
}

// TestApplyWorktreeCleanup_BranchFollowsAction pins branch deletion to the
// row's action, so a row picked past the verdict never loses its branch
// whatever options reach the apply step.
func TestApplyWorktreeCleanup_BranchFollowsAction(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	path := addCleanupWorktree(t, repo.Dir, "keep-me")
	runner := &git.ExecRunner{Dir: repo.Dir}
	entry := worktreeCleanupEntry{Path: path, Branch: "keep-me", Target: "main", Action: cleanupActionRemove}
	removed, failed := applyWorktreeCleanup(context.Background(), &strings.Builder{}, runner, []worktreeCleanupEntry{entry}, worktreeCleanupOpts{deleteBranches: true})
	if len(removed) != 1 || len(failed) != 0 {
		t.Fatalf("removed=%+v failed=%+v", removed, failed)
	}
	if !branchExists(context.Background(), runner, "keep-me") {
		t.Fatal("a remove-worktree action deleted its branch")
	}
}

// TestCollectWorktreeCleanup_DetachedCarriesLock records the lock of a
// worktree that the verdict skips before its lock check.
func TestCollectWorktreeCleanup_DetachedCarriesLock(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	det := filepath.Join(t.TempDir(), "det")
	repo.RunGit("worktree", "add", "--detach", det)
	repo.RunGit("worktree", "lock", "--reason", "claude agent (pid 999999)", det)
	rep := runCleanupJSON(t, repo.Dir)
	for _, s := range rep.Skipped {
		if sameDir(s.Path, det) {
			if !s.Locked {
				t.Fatalf("detached locked worktree reported unlocked: %+v", s)
			}
			return
		}
	}
	t.Fatalf("detached worktree missing from the report: %+v", rep.Skipped)
}

// TestCheckSelectedLosses_KeepsPickOrder runs the checks in parallel but must
// list the losses in the order the user picked the rows.
func TestCheckSelectedLosses_KeepsPickOrder(t *testing.T) {
	missing := t.TempDir()
	var picked []cleanupSelectRow
	for _, name := range []string{"c", "a", "b", "d"} {
		picked = append(picked, cleanupSelectRow{entry: worktreeCleanupEntry{Path: filepath.Join(missing, name), Branch: name, Reasons: []string{"protected"}}})
	}
	losses, lossy := checkSelectedLosses(context.Background(), &git.ExecRunner{Dir: missing}, picked)
	if len(losses) != 4 || len(lossy) != 4 {
		t.Fatalf("an unreadable worktree must count as a loss: %v", lossy)
	}
	for i, name := range []string{"c", "a", "b", "d"} {
		if !strings.HasPrefix(lossy[i], filepath.Join(missing, name)+":") {
			t.Fatalf("loss %d = %q, want the row picked as %s", i, lossy[i], name)
		}
	}
}
