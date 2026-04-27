package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/x-mesh/gk/internal/branchclean"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// ---------------------------------------------------------------------------
// Task 11.2: Integration test — 전체 `gk branch clean` end-to-end
// ---------------------------------------------------------------------------

// setupCleanRepo creates a Cleaner backed by a real git repo.
func setupCleanRepo(t *testing.T, repo *testutil.Repo) *branchclean.Cleaner {
	t.Helper()
	runner := &git.ExecRunner{Dir: repo.Dir}
	client := git.NewClient(runner)
	return &branchclean.Cleaner{
		Runner: runner,
		Client: client,
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}
}

// defaultProtected returns the standard protected branch list.
func defaultProtected() []string {
	return []string{"main", "master", "develop"}
}

// checkBranchExists checks whether a branch name exists in the repo.
func checkBranchExists(t *testing.T, repo *testutil.Repo, name string) bool {
	t.Helper()
	_, err := repo.TryGit("rev-parse", "--verify", "refs/heads/"+name)
	return err == nil
}

// ---------------------------------------------------------------------------
// Test: default (no flags) → merged branches deleted
// ---------------------------------------------------------------------------

func TestIntegration_BranchClean_DefaultMerged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create and merge a branch
	repo.CreateBranch("feat-merged")
	repo.WriteFile("merged.txt", "merged\n")
	repo.Commit("add merged feature")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "feat-merged", "-m", "merge feat-merged")

	// create an unmerged branch (should NOT be deleted)
	repo.CreateBranch("feat-active")
	repo.WriteFile("active.txt", "active\n")
	repo.Commit("add active feature")
	repo.Checkout("main")

	// set up remote HEAD so DefaultBranch works
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("fetch", "origin")
	repo.SetRemoteHEAD("origin", "main")

	cleaner := setupCleanRepo(t, repo)
	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Yes:       true,
		Protected: defaultProtected(),
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	// feat-merged should be deleted
	if !contains(result.Deleted, "feat-merged") {
		t.Errorf("expected feat-merged in Deleted, got %v", result.Deleted)
	}

	// feat-active should still exist
	if !checkBranchExists(t, repo, "feat-active") {
		t.Error("feat-active should still exist (not merged)")
	}

	// feat-merged should no longer exist
	if checkBranchExists(t, repo, "feat-merged") {
		t.Error("feat-merged should have been deleted")
	}
}

// ---------------------------------------------------------------------------
// Test: --gone → gone branches deleted
// ---------------------------------------------------------------------------

func TestIntegration_BranchClean_Gone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// set up remote
	repo.RunGit("remote", "add", "origin", repo.Dir)

	// create a branch, push it to origin, set upstream, then remove remote ref
	repo.CreateBranch("feat-gone")
	repo.WriteFile("gone.txt", "gone\n")
	repo.Commit("add gone feature")
	repo.Checkout("main")

	// fetch so origin/feat-gone exists
	repo.RunGit("fetch", "origin")
	repo.SetRemoteHEAD("origin", "main")

	// set upstream tracking
	repo.RunGit("branch", "--set-upstream-to=origin/feat-gone", "feat-gone")
	// delete the remote tracking ref to simulate "gone"
	repo.RunGit("update-ref", "-d", "refs/remotes/origin/feat-gone")

	cleaner := setupCleanRepo(t, repo)
	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Yes:       true,
		Gone:      true,
		Force:     true, // -D since feat-gone is not merged
		Protected: defaultProtected(),
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	// feat-gone should be deleted (collected as gone or merged)
	if !contains(result.Deleted, "feat-gone") {
		t.Errorf("expected feat-gone in Deleted, got %v", result.Deleted)
	}

	// feat-gone should no longer exist
	if checkBranchExists(t, repo, "feat-gone") {
		t.Error("feat-gone should have been deleted")
	}
}

// ---------------------------------------------------------------------------
// Test: --all --yes → all cleanup
// ---------------------------------------------------------------------------

func TestIntegration_BranchClean_AllYes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// set up remote
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("fetch", "origin")
	repo.SetRemoteHEAD("origin", "main")

	// create a merged branch
	repo.CreateBranch("feat-merged")
	repo.WriteFile("m.txt", "m\n")
	repo.Commit("add merged")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "feat-merged", "-m", "merge feat-merged")

	// create a branch with gone upstream
	repo.CreateBranch("feat-gone")
	repo.WriteFile("g.txt", "g\n")
	repo.Commit("add gone")
	repo.Checkout("main")
	repo.RunGit("fetch", "origin")
	repo.RunGit("branch", "--set-upstream-to=origin/feat-gone", "feat-gone")
	repo.RunGit("update-ref", "-d", "refs/remotes/origin/feat-gone")

	cleaner := setupCleanRepo(t, repo)
	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		All:       true,
		Yes:       true,
		Force:     true, // -D to handle unmerged branches
		Protected: defaultProtected(),
		StaleDays: 30,
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	// both should be deleted
	if !contains(result.Deleted, "feat-merged") {
		t.Errorf("expected feat-merged in Deleted, got %v", result.Deleted)
	}
	if !contains(result.Deleted, "feat-gone") {
		t.Errorf("expected feat-gone in Deleted, got %v", result.Deleted)
	}

	if checkBranchExists(t, repo, "feat-merged") {
		t.Error("feat-merged should have been deleted")
	}
	if checkBranchExists(t, repo, "feat-gone") {
		t.Error("feat-gone should have been deleted")
	}
}

// ---------------------------------------------------------------------------
// Test: --dry-run → no deletion
// ---------------------------------------------------------------------------

func TestIntegration_BranchClean_DryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create and merge a branch
	repo.CreateBranch("feat-dryrun")
	repo.WriteFile("dr.txt", "dr\n")
	repo.Commit("add dryrun")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "feat-dryrun", "-m", "merge feat-dryrun")

	// set up remote HEAD
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("fetch", "origin")
	repo.SetRemoteHEAD("origin", "main")

	cleaner := setupCleanRepo(t, repo)
	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		DryRun:    true,
		Protected: defaultProtected(),
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	// DryRun candidates should include feat-dryrun
	found := false
	for _, c := range result.DryRun {
		if c.Name == "feat-dryrun" {
			found = true
		}
	}
	if !found {
		t.Error("expected feat-dryrun in dry-run candidates")
	}

	// No branches should be deleted
	if len(result.Deleted) > 0 {
		t.Errorf("dry-run should not delete branches, got %v", result.Deleted)
	}

	// Branch should still exist
	if !checkBranchExists(t, repo, "feat-dryrun") {
		t.Error("feat-dryrun should still exist after dry-run")
	}
}

// ---------------------------------------------------------------------------
// Test: protected branches never deleted
// ---------------------------------------------------------------------------

func TestIntegration_BranchClean_ProtectedNeverDeleted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// create a "develop" branch (protected) and merge it
	repo.CreateBranch("develop")
	repo.WriteFile("dev.txt", "dev\n")
	repo.Commit("add dev")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "develop", "-m", "merge develop")

	// create a normal merged branch
	repo.CreateBranch("feat-normal")
	repo.WriteFile("normal.txt", "normal\n")
	repo.Commit("add normal")
	repo.Checkout("main")
	repo.RunGit("merge", "--no-ff", "feat-normal", "-m", "merge feat-normal")

	// set up remote HEAD
	repo.RunGit("remote", "add", "origin", repo.Dir)
	repo.RunGit("fetch", "origin")
	repo.SetRemoteHEAD("origin", "main")

	cleaner := setupCleanRepo(t, repo)
	result, err := cleaner.Run(context.Background(), branchclean.CleanOptions{
		Yes:       true,
		Protected: defaultProtected(),
	})
	if err != nil {
		t.Fatalf("Cleaner.Run: %v", err)
	}

	// develop (protected) should NOT be deleted
	if contains(result.Deleted, "develop") {
		t.Error("protected branch 'develop' should not be deleted")
	}
	if !checkBranchExists(t, repo, "develop") {
		t.Error("develop should still exist (protected)")
	}

	// main (current + protected) should NOT be deleted
	if contains(result.Deleted, "main") {
		t.Error("current/protected branch 'main' should not be deleted")
	}

	// feat-normal should be deleted
	if !contains(result.Deleted, "feat-normal") {
		t.Errorf("expected feat-normal in Deleted, got %v", result.Deleted)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
