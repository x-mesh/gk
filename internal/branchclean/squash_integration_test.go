package branchclean

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// squashMergeFixture builds the exact GitHub "Squash & Merge" shape: a
// two-commit branch whose combined diff lands on main as ONE commit. The
// squashed commit's patch-id matches neither original, so git cherry sees
// only `+` lines.
func squashMergeFixture(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("base.txt", "base")
	repo.Commit("base")
	repo.CreateBranch("feat/sq")
	repo.WriteFile("f1.txt", "one")
	repo.Commit("c1")
	repo.WriteFile("f2.txt", "two")
	repo.Commit("c2")
	repo.Checkout("main")
	repo.RunGit("merge", "--squash", "feat/sq")
	repo.RunGit("commit", "-m", "feat: squashed")
	return repo
}

// The multi-commit squash is the case the content check exists for. The test
// first proves the premise — cherry alone does NOT see it — so a future git
// that starts seeing it would surface here as "the fallback is redundant",
// not as silent dead code.
func TestDetectSquashMerged_RealMultiCommitSquash(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := squashMergeFixture(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	cherryOut, _, err := runner.Run(ctx, "cherry", "main", "feat/sq")
	if err != nil {
		t.Fatal(err)
	}
	if allApplied, _, _ := ParseCherryOutput(string(cherryOut)); allApplied {
		t.Fatalf("premise broken: cherry already detects the squash (%q) — is the content check still needed?", cherryOut)
	}

	d := &SquashDetector{Runner: runner}
	squashed, ambig, warnings := d.DetectSquashMerged(ctx, []string{"feat/sq"}, "main", nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(squashed) != 1 || squashed[0] != "feat/sq" {
		t.Fatalf("squashed = %v (ambiguous %v) — the content check must catch the multi-commit squash cherry misses", squashed, ambig)
	}

	// The other direction: new work on the branch AFTER the squash must keep
	// it out of the candidates — its tree now differs from main's.
	repo.Checkout("feat/sq")
	repo.WriteFile("f3.txt", "three")
	repo.Commit("c3")
	repo.Checkout("main")
	squashed2, ambig2, _ := d.DetectSquashMerged(ctx, []string{"feat/sq"}, "main", nil)
	if len(squashed2) != 0 || len(ambig2) != 0 {
		t.Fatalf("post-squash new work must stay undetected: squashed=%v ambiguous=%v", squashed2, ambig2)
	}
}

// End to end through the cleaner: a squash-merged candidate deletes with -D
// and WITHOUT --force — `git branch -d` refuses non-ancestors, and requiring
// --force would also surface protected branches, the wrong price to pay.
func TestCleanerRun_DeletesSquashMergedWithoutForce(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := squashMergeFixture(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	c := &Cleaner{Runner: runner, Client: git.NewClient(runner)}

	res, err := c.Run(context.Background(), CleanOptions{
		SquashMerged: true,
		Yes:          true,
		NoAI:         true,
		BaseBranch:   "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat/sq" {
		t.Fatalf("deleted = %v, failed = %v — squash-merged must delete without --force", res.Deleted, res.Failed)
	}
	if out := strings.TrimSpace(repo.RunGit("branch", "--list", "feat/sq")); out != "" {
		t.Fatalf("feat/sq still exists after clean: %q", out)
	}
}
