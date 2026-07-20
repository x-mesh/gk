package cli

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/git"
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

	branches, known := collectAllLocal(ctx, runner)
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

// Untracked files count as stranded work. loadWorktreeDirtyStates drops
// them as picker noise, which is why --all does not reuse it: a file you
// wrote and never added is exactly what gets lost on a machine switch.
func TestCollectAllLocal_CountsUntrackedInWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := newAllScopeRepo(t)
	r.WriteFile("scratch.txt", "never added\n") // untracked, not committed

	branches, _ := collectAllLocal(context.Background(), &git.ExecRunner{Dir: r.Dir})
	mainRep, _ := branchByName(branches, "main")
	if mainRep.Unstaged == 0 {
		t.Errorf("untracked file must count as local-only work, got %+v", mainRep)
	}
	if mainRep.Clean {
		t.Error("a branch holding an untracked file is not clean")
	}
}

// With no remote at all, "not pushed" cannot be told apart from "nothing
// to push" — nothing may be reported clean on that basis.
func TestCollectAllLocal_NoRemoteIsNeverClean(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	branches, known := collectAllLocal(context.Background(), &git.ExecRunner{Dir: r.Dir})
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
