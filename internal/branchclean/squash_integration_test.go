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

// squashThenBaseMovesFixture is the shape merge-tree cannot see: the branch
// is squash-merged, and the base then changes the same line again (a later
// PR or a release commit editing CHANGELOG.md). Merging the branch now
// conflicts, but its whole change is already in the base as one commit.
func squashThenBaseMovesFixture(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("CHANGELOG.md", "# Changes\n\n- one\n")
	repo.Commit("base")
	repo.CreateBranch("feat/sq")
	repo.WriteFile("CHANGELOG.md", "# Changes\n\n- one\n- two\n")
	repo.Commit("c1")
	repo.WriteFile("f.txt", "f\n")
	repo.Commit("c2")
	repo.Checkout("main")
	repo.RunGit("merge", "--squash", "feat/sq")
	repo.RunGit("commit", "-m", "feat: squashed")
	repo.WriteFile("CHANGELOG.md", "# Changes\n\n## 1.0\n\n- one\n- two, released\n")
	repo.Commit("release")
	return repo
}

func TestDetectSquashMerged_PatchIDAfterBaseMoved(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := squashThenBaseMovesFixture(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	baseTree := strings.TrimSpace(repo.RunGit("rev-parse", "main^{tree}"))
	if ok, _ := effectivelyMergedTree(ctx, runner, baseTree, "main", "feat/sq"); ok {
		// The premise: the content check alone misses this shape.
		t.Fatal("premise broken: merge-tree already sees the squash after the base moved")
	}
	before := looseObjects(t, repo)
	if ok, err := patchIDMerged(ctx, runner, "main", "feat/sq"); !ok || err != nil {
		t.Fatalf("patchIDMerged = %v, %v", ok, err)
	}
	if after := looseObjects(t, repo); after != before {
		t.Errorf("the patch-id check wrote objects: %s -> %s", before, after)
	}

	d := &SquashDetector{Runner: runner}
	squashed, _, warnings := d.DetectSquashMerged(ctx, []string{"feat/sq"}, "main", nil)
	if len(warnings) != 0 || len(squashed) != 1 {
		t.Fatalf("squashed = %v, warnings %v — the patch-id check must catch it", squashed, warnings)
	}
	if set := EffectivelyMergedSet(ctx, runner, "main", []string{"feat/sq"}); !set["feat/sq"] {
		t.Errorf("EffectivelyMergedSet disagrees with the cleaner: %v", set)
	}
	if ok, err := EffectivelyMerged(ctx, runner, "main", "feat/sq"); !ok || err != nil {
		t.Errorf("EffectivelyMerged = %v, %v", ok, err)
	}
}

func TestDetectSquashMerged_PatchIDNegatives(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	ctx := context.Background()
	t.Run("new work after the squash", func(t *testing.T) {
		repo := squashThenBaseMovesFixture(t)
		repo.Checkout("feat/sq")
		repo.WriteFile("g.txt", "g\n")
		repo.Commit("c3")
		repo.Checkout("main")
		if got := EffectivelyMergedSet(ctx, &git.ExecRunner{Dir: repo.Dir}, "main", []string{"feat/sq"}); got["feat/sq"] {
			t.Error("a branch with work past the squash is reported merged")
		}
	})
	t.Run("the squash changed the content", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		repo.WriteFile("a.txt", "a\n")
		repo.Commit("base")
		repo.CreateBranch("feat/sq")
		repo.WriteFile("a.txt", "a\nb\n")
		repo.Commit("c1")
		repo.Checkout("main")
		repo.WriteFile("a.txt", "a\nb, edited while merging\n")
		repo.Commit("feat: squashed with an edit")
		if got := EffectivelyMergedSet(ctx, &git.ExecRunner{Dir: repo.Dir}, "main", []string{"feat/sq"}); got["feat/sq"] {
			t.Error("a branch whose change differs from the base commit is reported merged")
		}
	})
	t.Run("changes that cancel out", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		repo.WriteFile("a.txt", "a\n")
		repo.Commit("base")
		repo.CreateBranch("feat/noop")
		repo.WriteFile("a.txt", "x\n")
		repo.Commit("change")
		repo.WriteFile("a.txt", "a\n")
		repo.Commit("revert")
		repo.Checkout("main")
		repo.WriteFile("a.txt", "a\nmain moves\n")
		repo.Commit("main moves")
		// A net-zero branch is the content check's business: the patch-id
		// check must not match an empty diff to anything.
		if ok, _ := patchIDMerged(ctx, &git.ExecRunner{Dir: repo.Dir}, "main", "feat/noop"); ok {
			t.Error("an empty cumulative diff matched a base commit")
		}
	})
}

func looseObjects(t *testing.T, repo *testutil.Repo) string {
	t.Helper()
	for _, l := range strings.Split(repo.RunGit("count-objects", "-v"), "\n") {
		if strings.HasPrefix(l, "count:") {
			return l
		}
	}
	return ""
}
