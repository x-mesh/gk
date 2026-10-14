package cli

import (
	"context"
	"encoding/json"
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

	// cleanup -i passes the changed candidates without deleteBranches; their
	// action alone carries --delete-branches.
	path = addCleanupWorktree(t, repo.Dir, "drop-me")
	entry = worktreeCleanupEntry{Path: path, Branch: "drop-me", Target: "main", Action: cleanupActionRemoveDeleteBranch}
	removed, failed = applyWorktreeCleanup(context.Background(), &strings.Builder{}, runner, []worktreeCleanupEntry{entry}, worktreeCleanupOpts{})
	if len(removed) != 1 || len(failed) != 0 || !removed[0].BranchDeleted {
		t.Fatalf("removed=%+v failed=%+v", removed, failed)
	}
	if branchExists(context.Background(), runner, "drop-me") {
		t.Fatal("a remove-worktree-delete-branch action kept its branch")
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

// TestCleanupSelectLoss_RecheckReplacesListState forces only what the user saw
// and confirmed: a worktree that was dirty when the list opened and is clean
// at the recheck must lose its old counts, or the removal forces it without a
// prompt; and a failed recheck must still show the counts the list knew.
func TestCleanupSelectLoss_RecheckReplacesListState(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	path := addCleanupWorktree(t, repo.Dir, "was-dirty")
	runner := &git.ExecRunner{Dir: repo.Dir}
	r := cleanupSelectRow{entry: worktreeCleanupEntry{Path: path, Branch: "was-dirty", Reasons: []string{"dirty"}, Dirty: &contextDirtyJSON{Unstaged: 1}}}
	if loss := cleanupSelectLoss(ctx, runner, &r); loss != "" {
		t.Fatalf("a clean worktree listed a loss: %q", loss)
	}
	if r.entry.Dirty != nil {
		t.Fatalf("the counts from the list survived a clean recheck: %+v", r.entry.Dirty)
	}

	broken, brokenPath := newMergedSubmoduleWorktree(t)
	breakSubmodule(t, brokenPath)
	r = cleanupSelectRow{entry: worktreeCleanupEntry{Path: brokenPath, Branch: "feat/sub-wt", Reasons: []string{"dirty"}, Dirty: &contextDirtyJSON{Unstaged: 2}}}
	loss := cleanupSelectLoss(ctx, &git.ExecRunner{Dir: broken.Dir}, &r)
	if !strings.Contains(loss, "could not check") || !strings.Contains(loss, "2 modified") {
		t.Fatalf("a failed recheck must keep the known counts, loss=%q", loss)
	}
}

// TestWorktreeCleanup_StatusUnreadableIsKept keeps a worktree whose git
// status fails: nothing is known about its work, so neither cleanup nor the
// [removable] mark of gk worktree list may treat it as clean.
func TestWorktreeCleanup_StatusUnreadableIsKept(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo, wtPath := newMergedSubmoduleWorktree(t)
	isCandidate := func(rep worktreeCleanupJSON) bool {
		for _, c := range rep.Candidates {
			if sameDir(c.Path, wtPath) {
				return true
			}
		}
		return false
	}
	if !isCandidate(runCleanupJSON(t, repo.Dir)) {
		t.Fatal("setup: the merged worktree must be a candidate while its status reads")
	}

	breakSubmodule(t, wtPath)
	rep := runCleanupJSON(t, repo.Dir)
	if isCandidate(rep) {
		t.Fatal("a worktree whose status fails is a cleanup candidate")
	}
	var kept *worktreeCleanupEntry
	for i := range rep.Skipped {
		if sameDir(rep.Skipped[i].Path, wtPath) {
			kept = &rep.Skipped[i]
		}
	}
	if kept == nil || cleanupPrimaryReason(*kept) != reasonStatusUnreadable || kept.Error == "" {
		t.Fatalf("want the worktree kept as status-unreadable with the git error, got %+v", kept)
	}
	// --discard-dirty drops known changes; changes nobody could read are not
	// known, so it must not reach this worktree.
	if isCandidate(runCleanupJSON(t, repo.Dir, "--discard-dirty")) {
		t.Fatal("--discard-dirty made an unreadable worktree a candidate")
	}

	listJSON, jbuf := buildWorktreeCmd(repo.Dir, "list", "--json")
	if err := listJSON.Execute(); err != nil {
		t.Fatalf("worktree list --json: %v\n%s", err, jbuf.String())
	}
	var rows []worktreeListEntryJSON
	if err := json.Unmarshal(jbuf.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal list: %v\n%s", err, jbuf.String())
	}
	for _, r := range rows {
		if sameDir(r.Path, wtPath) && r.StatusError == "" {
			t.Fatalf("list --json reports an unreadable worktree without status_error: %+v", r)
		}
	}

	list, buf := buildWorktreeCmd(repo.Dir, "list")
	if err := list.Execute(); err != nil {
		t.Fatalf("worktree list: %v\n%s", err, buf.String())
	}
	if line := worktreeListLine(buf.String(), "feat/sub-wt"); !strings.Contains(line, "[status unreadable]") || strings.Contains(line, "[removable]") {
		t.Fatalf("list must mark the worktree unreadable, not removable: %q\n%s", line, buf.String())
	}
}

// TestCleanupSelectDetail_StatusUnreadable keeps the detail box of cleanup -i
// from calling a worktree clean when its status could not be read.
func TestCleanupSelectDetail_StatusUnreadable(t *testing.T) {
	r := cleanupSelectRow{entry: worktreeCleanupEntry{Path: "/wt/broken", Branch: "broken", Reasons: []string{"status-unreadable"}, Error: "git status: fatal: broken"}}
	detail := cleanupSelectDetail(r)
	if strings.Contains(detail, "no uncommitted changes") || !strings.Contains(detail, "uncommitted changes unknown") {
		t.Fatalf("detail must say the changes are unknown:\n%s", detail)
	}
}
