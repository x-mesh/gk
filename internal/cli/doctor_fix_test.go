package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestCheckRepoLockFile_StaleVsFresh(t *testing.T) {
	gitDir := t.TempDir()
	lock := filepath.Join(gitDir, "index.lock")

	// No lock → PASS.
	if c := checkRepoLockFile(gitDir); c.Status != statusPass {
		t.Errorf("no lock: status = %v", c.Status)
	}

	// Fresh lock → WARN, and the fix text must not suggest deletion.
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := checkRepoLockFile(gitDir)
	if c.Status != statusWarn || !strings.Contains(c.Detail, "recent") {
		t.Errorf("fresh lock: %+v", c)
	}
	if strings.Contains(c.Fix, "rm .git/index.lock") {
		t.Errorf("fresh lock fix must not suggest deletion: %q", c.Fix)
	}

	// Aged lock → FAIL with the removal fix.
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	c = checkRepoLockFile(gitDir)
	if c.Status != statusFail || !strings.Contains(c.Detail, "stale") {
		t.Errorf("stale lock: %+v", c)
	}
}

func TestMergeHeadOrphan(t *testing.T) {
	gitDir := t.TempDir()
	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"diff --name-only --diff-filter=U": {Stdout: ""},
	}}

	// No MERGE_HEAD → false.
	if mergeHeadOrphan(context.Background(), fake, gitDir) {
		t.Error("no MERGE_HEAD must be false")
	}
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// MERGE_HEAD + zero unmerged → orphan.
	if !mergeHeadOrphan(context.Background(), fake, gitDir) {
		t.Error("MERGE_HEAD with no conflicts must be orphan")
	}
	// Unmerged paths present → not orphan.
	fake.Responses["diff --name-only --diff-filter=U"] = git.FakeResponse{Stdout: "a.go\n"}
	if mergeHeadOrphan(context.Background(), fake, gitDir) {
		t.Error("conflicted merge must not be orphan")
	}
}

func TestIntegration_CheckPrunableWorktrees(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}

	// Clean repo → PASS.
	if c := checkPrunableWorktrees(context.Background(), runner); c.Status != statusPass {
		t.Errorf("clean: %+v", c)
	}

	// Add a worktree, delete its directory behind git's back → prunable.
	wt := filepath.Join(t.TempDir(), "wt")
	repo.RunGit("worktree", "add", wt, "-b", "scratch")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	c := checkPrunableWorktrees(context.Background(), runner)
	if c.Status != statusWarn || !strings.Contains(c.Detail, "1 stale") {
		t.Errorf("prunable: %+v", c)
	}
}
