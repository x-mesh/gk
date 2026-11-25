package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestLocalChangeBadge(t *testing.T) {
	prevNoColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prevNoColor })

	cases := []struct {
		name    string
		entries []git.StatusEntry
		want    string
	}{
		{name: "clean tree → empty", entries: nil, want: ""},
		{
			name: "modified only",
			entries: []git.StatusEntry{
				{Kind: git.KindOrdinary, XY: ".M", Path: "a"},
			},
			want: "· 1 unstaged",
		},
		{
			name: "all layers",
			entries: []git.StatusEntry{
				{Kind: git.KindOrdinary, XY: ".M", Path: "a"},  // unstaged
				{Kind: git.KindUntracked, XY: "??", Path: "b"}, // unstaged
				{Kind: git.KindOrdinary, XY: "M.", Path: "c"},  // staged
				{Kind: git.KindUnmerged, XY: "UU", Path: "d"},  // conflict
			},
			want: "· 2 unstaged · 1 staged · 1 conflicts",
		},
		{
			name: "submodules excluded",
			entries: []git.StatusEntry{
				{Kind: git.KindSubmodule, XY: ".M", Path: "sub"},
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localChangeBadge(&git.Status{Entries: tc.entries})
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUnpushedCommits_NoUpstreamButRemotes verifies the --remotes fallback for
// gk local: a branch with no upstream but a remote present lists its
// local-only commits.
func TestUnpushedCommits_NoUpstreamButRemotes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	r.RunGit("remote", "add", "origin", bareDir)
	r.RunGit("push", "-q", "origin", "main")

	r.CreateBranch("feature")
	r.WriteFile("x.txt", "x\n")
	r.Commit("local only 1")
	r.WriteFile("y.txt", "y\n")
	r.Commit("local only 2")

	runner := &git.ExecRunner{Dir: r.Dir}
	commits, ok := unpushedCommits(context.Background(), runner, 10)
	if !ok {
		t.Fatal("remote present → expected ok=true")
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 unpushed commits, got %d (%+v)", len(commits), commits)
	}
	if !strings.Contains(commits[0].Subject, "local only 2") {
		t.Errorf("newest-first expected; got first=%q", commits[0].Subject)
	}
}

// TestUnpushedCommits_NoRemote: a repo with no remotes at all cannot determine
// push state — ok must be false so gk local prints "no remote to compare".
func TestUnpushedCommits_NoRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	if _, ok := unpushedCommits(context.Background(), runner, 10); ok {
		t.Error("no remote → expected ok=false")
	}
}

// --- gk local --all (repo scope) ---

// newAllScopeRepo builds a repo with a remote and three branches:
//   - main      pushed, clean
//   - shared    pushed, then one local commit on top
//   - orphaned  never pushed at all
//
// The last one is the case that motivated --all: branch scope cannot see
// it, and neither can any other machine.
func newAllScopeRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	r := testutil.NewRepo(t)
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	r.RunGit("remote", "add", "origin", bareDir)
	r.RunGit("push", "-q", "-u", "origin", "main")

	r.CreateBranch("shared")
	r.WriteFile("s.txt", "s\n")
	r.Commit("shared base")
	r.RunGit("push", "-q", "-u", "origin", "shared")
	r.WriteFile("s2.txt", "s2\n")
	r.Commit("shared local-only")

	r.Checkout("main")
	r.CreateBranch("orphaned")
	r.WriteFile("o.txt", "o\n")
	r.Commit("never pushed")

	r.Checkout("main")
	return r
}

func branchByName(reps []localBranchReport, name string) (localBranchReport, bool) {
	for _, b := range reps {
		if b.Branch == name {
			return b, true
		}
	}
	return localBranchReport{}, false
}

// The whole reason --all exists: standing on a clean, fully pushed branch
// tells you nothing about work stranded on another one.
func TestCollectAllLocal_FindsBranchesTheCurrentScopeCannotSee(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	ctx := context.Background()

	// Branch scope, standing on main: reports nothing stranded.
	commits, ok := unpushedCommits(ctx, runner, 10)
	if !ok || len(commits) != 0 {
		t.Fatalf("precondition: main should look clean, got ok=%v commits=%d", ok, len(commits))
	}

	branches, known := collectAllLocal(ctx, runner, nil)
	if !known {
		t.Fatal("a remote exists → push state must be knowable")
	}
	if len(branches) != 3 {
		t.Fatalf("want 3 branches, got %d (%+v)", len(branches), branches)
	}

	orphan, found := branchByName(branches, "orphaned")
	if !found {
		t.Fatal("the never-pushed branch must appear — it is the case branch scope misses")
	}
	if !orphan.NoUpstream {
		t.Error("a branch that was never pushed must be flagged no_upstream")
	}
	if orphan.Unpushed != 1 || orphan.Clean {
		t.Errorf("orphaned: want 1 unpushed and clean=false, got %+v", orphan)
	}

	shared, _ := branchByName(branches, "shared")
	if shared.NoUpstream {
		t.Error("shared has an upstream; it must not be flagged no_upstream")
	}
	if shared.Unpushed != 1 || shared.Clean {
		t.Errorf("shared: want 1 unpushed and clean=false, got %+v", shared)
	}

	mainRep, _ := branchByName(branches, "main")
	if !mainRep.Clean || mainRep.Unpushed != 0 {
		t.Errorf("main is pushed and clean, got %+v", mainRep)
	}
}

// unpushedByBranch replaced one rev-list per branch; its counts must match
// that per-branch answer on shapes the graph walk could get wrong: a merge
// joining two unpushed lines, two branches on one tip, a branch on a pushed
// commit, and a branch whose short name git qualifies because a tag shares it.
func TestUnpushedByBranch_MatchesPerBranchRevList(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	r.Checkout("shared")
	r.CreateBranch("merged")
	r.RunGit("merge", "-q", "--no-ff", "--no-edit", "orphaned")
	r.WriteFile("m.txt", "m\n")
	r.Commit("after merge")
	r.RunGit("branch", "twin", "merged")
	r.RunGit("branch", "at-main", "main")
	r.Checkout("main")
	r.CreateBranch("amb")
	r.WriteFile("a.txt", "a\n")
	r.Commit("amb only")
	r.RunGit("tag", "amb", "main")
	r.Checkout("main")

	runner := &git.ExecRunner{Dir: r.Dir}
	ctx := context.Background()
	check := func(setting string) {
		t.Helper()
		counts, err := unpushedByBranch(ctx, runner)
		if err != nil {
			t.Fatal(err)
		}
		branches, err := listLocalBranches(ctx, runner)
		if err != nil {
			t.Fatal(err)
		}
		sawQualified := false
		for _, b := range branches {
			sawQualified = sawQualified || strings.HasPrefix(b.Name, "heads/")
			full := "refs/heads/" + strings.TrimPrefix(b.Name, "heads/")
			want, _ := strconv.Atoi(strings.TrimSpace(r.RunGit("rev-list", "--count", full, "--not", "--remotes")))
			if got := unpushedLookup(counts, b.Name); got != want {
				t.Errorf("%s: %s: got %d unpushed, rev-list says %d", setting, b.Name, got, want)
			}
		}
		if !sawQualified {
			t.Fatalf("%s: precondition: the tag should make git print the branch as heads/amb", setting)
		}
		if counts["refs/heads/merged"] != 4 {
			t.Errorf("%s: merged: want 4 (shared local-only, never pushed, merge, after merge), got %d", setting, counts["refs/heads/merged"])
		}
	}
	check("default config")
	// The counts must not depend on how the user's config decorates log
	// output: a branch that lost its decoration once counted 0.
	r.RunGit("config", "log.excludeDecoration", "refs/heads/")
	r.RunGit("config", "log.decorate", "no")
	check("log.excludeDecoration=refs/heads/")
}

// Untracked files count as stranded work. loadWorktreeDirtyStates drops
// them as picker noise, which is why --all does not reuse it: a file you
// wrote and never added is exactly what gets lost on a machine switch.
func TestCollectAllLocal_CountsUntrackedInWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	r.WriteFile("scratch.txt", "never added\n") // untracked, not committed

	branches, _ := collectAllLocal(context.Background(), &git.ExecRunner{Dir: r.Dir}, nil)
	mainRep, _ := branchByName(branches, "main")
	if mainRep.Unstaged == 0 {
		t.Errorf("untracked file must count as local-only work, got %+v", mainRep)
	}
	if mainRep.Clean {
		t.Error("a branch holding an untracked file is not clean")
	}
}

// collectLocal hands its own status read to collectAllLocal instead of a
// second status call; the row of the invoking worktree must still carry it.
func TestCollectLocal_AllReusesCurrentWorktreeStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	r.WriteFile("scratch.txt", "never added\n")

	rep, err := collectLocal(context.Background(), r.Dir, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	mainRep, _ := branchByName(rep.Branches, "main")
	if mainRep.Unstaged != 1 || mainRep.Clean {
		t.Errorf("main holds one untracked file, got %+v", mainRep)
	}
}

// With no remote at all, "not pushed" cannot be told apart from "nothing
// to push" — nothing may be reported clean on that basis.
func TestCollectAllLocal_NoRemoteIsNeverClean(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	branches, known := collectAllLocal(context.Background(), &git.ExecRunner{Dir: r.Dir}, nil)
	if known {
		t.Fatal("no remote-tracking refs → push state must be unknown")
	}
	for _, b := range branches {
		if b.Clean {
			t.Errorf("branch %q claimed clean with no remote to compare against", b.Branch)
		}
	}
}

// A worktree recorded by git but missing from disk must read as unknown,
// never as clean — a reassuring answer we did not verify is worse than
// admitting we could not look.
func TestWorktreeLocalCounts_MissingPathIsUnknownNotClean(t *testing.T) {
	_, _, _, unknown := worktreeLocalCounts(context.Background(),
		filepath.Join(t.TempDir(), "does-not-exist"))
	if !unknown {
		t.Error("an unreadable worktree must be reported unknown")
	}
}

// A detached worktree holds no branch, so a scan that walks branches alone
// never looks inside it. Its uncommitted edits and the commits only its
// HEAD reaches are stranded all the same.
func TestCollectAllLocal_DetachedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	wtDir := filepath.Join(t.TempDir(), "detached-wt")
	r.RunGit("worktree", "add", "-q", "--detach", wtDir, "main")
	gitIn := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", wtDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeIn := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeIn("d.txt", "on no branch\n")
	gitIn("add", "d.txt")
	gitIn("commit", "-q", "-m", "commit on a detached HEAD")
	writeIn("scratch.txt", "never added\n")

	branches, known := collectAllLocal(context.Background(), &git.ExecRunner{Dir: r.Dir}, nil)
	if !known {
		t.Fatal("a remote exists → push state must be knowable")
	}
	var det *localBranchReport
	for i := range branches {
		if branches[i].Detached {
			det = &branches[i]
		}
	}
	if det == nil {
		t.Fatalf("the detached worktree must appear as its own row, got %+v", branches)
	}
	if det.Unpushed != 1 || det.Unstaged != 1 || det.Clean {
		t.Errorf("detached row: want 1 commit on no branch, 1 unstaged, clean=false; got %+v", *det)
	}
}

// When the state of a paused operation cannot be read, it is not "no
// operation": a worktree row turns unknown and the current-branch check fails.
// The per-branch rev-list it replaced read a failure as 0 unpushed, so a
// branch could pass as clean; a failed read must mark every branch unknown.
func TestCollectAllLocal_UnreadableUnpushedIsNotClean(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	prev := localUnpushedFn
	t.Cleanup(func() { localUnpushedFn = prev })
	localUnpushedFn = func(context.Context, *git.ExecRunner) (map[string]int, error) {
		return nil, errors.New("log failed")
	}

	branches, _ := collectAllLocal(context.Background(), &git.ExecRunner{Dir: r.Dir}, nil)
	if len(branches) == 0 {
		t.Fatal("want the branch rows even when the unpushed read fails")
	}
	for _, b := range branches {
		if !b.Unknown || b.Clean {
			t.Errorf("%s: want unknown and not clean, got %+v", b.Branch, b)
		}
	}
}

func TestLocal_UnreadableOperationIsNotClean(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	wtDir := filepath.Join(t.TempDir(), "detached-wt")
	r.RunGit("worktree", "add", "-q", "--detach", wtDir, "main")

	prev := localDetectFn
	t.Cleanup(func() { localDetectFn = prev })
	localDetectFn = func(context.Context, string) (*gitstate.State, error) {
		return nil, errors.New("cannot read the git dir")
	}

	branches, known := collectAllLocal(context.Background(), &git.ExecRunner{Dir: r.Dir}, nil)
	if !known {
		t.Fatal("a remote exists → push state must be knowable")
	}
	for _, b := range branches {
		if b.Worktree != "" && (!b.Unknown || b.Clean) {
			t.Errorf("row %q in %s: want unknown and not clean, got %+v", b.Branch, b.Worktree, b)
		}
	}
	if _, err := collectLocal(context.Background(), r.Dir, false, 10); err == nil {
		t.Error("the current-branch check must fail when the operation state cannot be read")
	}
}

// A rebase stopped on a conflict is unfinished work even before anything is
// committed: the report must name it and point at the command that resumes.
func TestCollectLocal_PausedRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	r.WriteFile("c.txt", "base\n")
	r.Commit("add c")
	r.CreateBranch("topic")
	r.WriteFile("c.txt", "topic\n")
	r.Commit("topic edits c")
	r.Checkout("main")
	r.WriteFile("c.txt", "main\n")
	r.Commit("main edits c")
	r.Checkout("topic")
	if _, err := r.TryGit("rebase", "main"); err == nil {
		t.Fatal("precondition: the rebase must stop on a conflict")
	}

	rep, err := collectLocal(context.Background(), r.Dir, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rep.Operation, "rebase") || rep.Resume == "" {
		t.Errorf("want a rebase operation with a resume command, got op=%q resume=%q", rep.Operation, rep.Resume)
	}
	if rep.Clean {
		t.Error("a paused rebase is not clean")
	}
	found := false
	for _, b := range rep.Branches {
		if b.Detached && strings.HasPrefix(b.Operation, "rebase") {
			found = true
		}
	}
	if !found {
		t.Errorf("the rebasing worktree must be listed with its operation, got %+v", rep.Branches)
	}
}
