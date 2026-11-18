package branchclean

import (
	"context"
	"reflect"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// twoSquashesFixture squash-merges feat/a and feat/b into main, then a
// release commit changes the lines of both, so both merges conflict and both
// branches go to the patch-id check.
func twoSquashesFixture(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a\n")
	repo.WriteFile("b.txt", "b\n")
	repo.WriteFile("r.txt", "r\n")
	repo.Commit("base")
	for _, n := range []string{"a", "b"} {
		repo.Checkout("main")
		repo.CreateBranch("feat/" + n)
		repo.WriteFile(n+".txt", n+"\n"+n+" feat\n")
		repo.Commit(n + " one")
		repo.WriteFile(n+"2.txt", n+"\n")
		repo.Commit(n + " two")
		repo.Checkout("main")
		repo.RunGit("merge", "--squash", "feat/"+n)
		repo.RunGit("commit", "-m", "squash "+n)
	}
	repo.WriteFile("a.txt", "a\na released\n")
	repo.WriteFile("b.txt", "b\nb released\n")
	repo.Commit("release")
	return repo
}

func TestPatchIDMergedSet(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := twoSquashesFixture(t)

	// Work past the squash: the whole change differs from the squash.
	repo.Checkout("feat/a")
	repo.CreateBranch("feat/apast")
	repo.WriteFile("c.txt", "c\n")
	repo.Commit("past the squash")

	// A change that landed on main, was reverted, and is re-applied by a
	// branch cut after the revert: its patch-id equals the old commit, which
	// is in the branch's own history, not a squash of it.
	repo.Checkout("main")
	repo.WriteFile("r.txt", "r\nR\n")
	repo.Commit("add R")
	repo.RunGit("revert", "--no-edit", "HEAD")
	repo.CreateBranch("feat/reapply")
	repo.WriteFile("r.txt", "r\nR\n")
	repo.Commit("re-add R")
	repo.Checkout("main")
	repo.WriteFile("r.txt", "r\nlater\n")
	repo.Commit("later")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	if ok, _ := patchIDMerged(ctx, runner, "main", "feat/reapply"); ok {
		t.Fatal("premise broken: the single check already matches the re-applied change")
	}

	branches := []string{"feat/a", "feat/apast", "feat/reapply", "feat/b"}
	before := looseObjects(t, repo)
	merged, errs := patchIDMergedSet(ctx, runner, "main", branches)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if want := map[string]bool{"feat/a": true, "feat/b": true}; !reflect.DeepEqual(merged, want) {
		t.Errorf("merged = %v, want %v", merged, want)
	}
	if after := looseObjects(t, repo); after != before {
		t.Errorf("the patch-id check wrote objects: %s -> %s", before, after)
	}
	for _, b := range branches {
		if ok, _ := patchIDMerged(ctx, runner, "main", b); ok != merged[b] {
			t.Errorf("%s: the shared check says %v, the single check %v", b, merged[b], ok)
		}
	}

	d := &SquashDetector{Runner: runner}
	squashed, _, warnings := d.DetectSquashMerged(ctx, branches, "main", nil)
	if len(warnings) != 0 || !reflect.DeepEqual(squashed, []string{"feat/a", "feat/b"}) {
		t.Errorf("DetectSquashMerged = %v, warnings %v", squashed, warnings)
	}
	if set := EffectivelyMergedSet(ctx, runner, "main", branches); !reflect.DeepEqual(set, map[string]bool{"feat/a": true, "feat/b": true}) {
		t.Errorf("EffectivelyMergedSet = %v", set)
	}
}

// When the shared log stops at the limit, a branch it did not match is
// checked with its own paths, which reach as far back as before.
func TestPatchIDMergedSet_LimitFallsBackToOneBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := twoSquashesFixture(t)
	for _, s := range []string{"x", "y", "z"} {
		repo.WriteFile("b2.txt", "b\n"+s+"\n")
		repo.Commit("b2 " + s)
	}
	old := patchIDBaseLimit
	patchIDBaseLimit = 2
	t.Cleanup(func() { patchIDBaseLimit = old })

	merged, errs := patchIDMergedSet(context.Background(), &git.ExecRunner{Dir: repo.Dir}, "main", []string{"feat/a", "feat/b"})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	// feat/a: the newest 2 commits on its paths are the release and its
	// squash. feat/b: the newest 2 on its paths are b2 commits.
	if want := map[string]bool{"feat/a": true}; !reflect.DeepEqual(merged, want) {
		t.Errorf("merged = %v, want %v", merged, want)
	}
}

// A squash commit that also changes another candidate branch's paths hashes
// differently in the shared log, which covers both branches' paths. The
// branch must still match, as it does alone, by hashing that commit again
// with the branch's paths only.
func TestPatchIDMergedSet_SquashWithOtherBranchPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a\n")
	repo.WriteFile("b.txt", "b1\nb2\nb3\nb4\nb5\nb6\nb7\nb8\n")
	repo.Commit("base")
	repo.CreateBranch("feat/a")
	repo.WriteFile("a.txt", "a\na feat\n")
	repo.Commit("a")
	repo.Checkout("main")
	repo.CreateBranch("feat/b")
	repo.WriteFile("b.txt", "b1\nb2\nb3\nb4\nb5\nb6\nb7\nb8 feat\n")
	repo.Commit("b")
	repo.Checkout("main")
	repo.RunGit("merge", "--squash", "feat/a")
	repo.WriteFile("b.txt", "b1 edited with the squash\nb2\nb3\nb4\nb5\nb6\nb7\nb8\n")
	repo.RunGit("add", "b.txt")
	repo.RunGit("commit", "-m", "squash a, with a b.txt edit")
	repo.WriteFile("a.txt", "a\na released\n")
	repo.WriteFile("b.txt", "b1 edited with the squash\nb2\nb3\nb4\nb5\nb6\nb7\nb8 released\n")
	repo.Commit("release")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	if ok, _ := patchIDMerged(ctx, runner, "main", "feat/a"); !ok {
		t.Fatal("premise broken: the single check does not match the squash")
	}
	merged, errs := patchIDMergedSet(ctx, runner, "main", []string{"feat/a", "feat/b"})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if want := map[string]bool{"feat/a": true}; !reflect.DeepEqual(merged, want) {
		t.Errorf("merged = %v, want %v", merged, want)
	}
}

// A path named like the old "commit <oid>" record header must stay a path of
// the squash in the shared log, or the squash no longer covers the branch's
// paths and the branch is missed.
func TestPatchIDMergedSet_PathNamedLikeCommitHeader(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a\n")
	repo.WriteFile("b.txt", "b1\nb2\nb3\nb4\nb5\nb6\nb7\nb8\n")
	base := repo.Commit("base")
	header := "commit " + base
	repo.CreateBranch("feat/a")
	repo.WriteFile("a.txt", "a\na feat\n")
	repo.WriteFile(header, "x\n")
	repo.Commit("a")
	repo.Checkout("main")
	repo.CreateBranch("feat/b")
	repo.WriteFile("b.txt", "b1\nb2\nb3\nb4\nb5\nb6\nb7\nb8 feat\n")
	repo.Commit("b")
	repo.Checkout("main")
	repo.RunGit("merge", "--squash", "feat/a")
	repo.WriteFile("b.txt", "b1 edited with the squash\nb2\nb3\nb4\nb5\nb6\nb7\nb8\n")
	repo.RunGit("add", "b.txt")
	repo.RunGit("commit", "-m", "squash a, with a b.txt edit")
	repo.WriteFile("a.txt", "a\na released\n")
	repo.WriteFile("b.txt", "b1 edited with the squash\nb2\nb3\nb4\nb5\nb6\nb7\nb8 released\n")
	repo.Commit("release")

	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()
	if ok, _ := patchIDMerged(ctx, runner, "main", "feat/a"); !ok {
		t.Fatal("premise broken: the single check does not match the squash")
	}
	merged, errs := patchIDMergedSet(ctx, runner, "main", []string{"feat/a", "feat/b"})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if want := map[string]bool{"feat/a": true}; !reflect.DeepEqual(merged, want) {
		t.Errorf("merged = %v, want %v", merged, want)
	}
}

// The commits hashed again with one branch's paths still need to be past its
// merge base: here the old commit changed r.txt and q.txt, was reverted, and
// a later branch re-applies the r.txt half of it.
func TestPatchIDMergedSet_SupersetReapplyNotMerged(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("r.txt", "r\n")
	repo.WriteFile("q.txt", "q\n")
	repo.Commit("base")
	repo.CreateBranch("feat/q")
	repo.WriteFile("q.txt", "q\nQ2\n")
	repo.Commit("q")
	repo.Checkout("main")
	repo.WriteFile("r.txt", "r\nR\n")
	repo.WriteFile("q.txt", "q\nQ\n")
	repo.Commit("add R and Q")
	repo.RunGit("revert", "--no-edit", "HEAD")
	repo.CreateBranch("feat/reapply")
	repo.WriteFile("r.txt", "r\nR\n")
	repo.Commit("re-add R")
	repo.Checkout("main")
	repo.WriteFile("r.txt", "r\nlater\n")
	repo.WriteFile("q.txt", "q\nlater\n")
	repo.Commit("later")

	merged, errs := patchIDMergedSet(context.Background(), &git.ExecRunner{Dir: repo.Dir}, "main", []string{"feat/q", "feat/reapply"})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(merged) != 0 {
		t.Errorf("merged = %v, want none", merged)
	}
}
