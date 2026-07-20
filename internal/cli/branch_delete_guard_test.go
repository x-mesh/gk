package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func protectedCfg(names ...string) *config.Config {
	c := &config.Config{}
	c.Branch.Protected = names
	return c
}

// newDeleteRunner answers the two probes the guard makes plus the delete
// itself. worktrees is `git worktree list --porcelain` output.
func newDeleteRunner(worktrees string) *git.FakeRunner {
	return &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"worktree list --porcelain": {Stdout: worktrees},
		},
	}
}

func deleteCalls(f *git.FakeRunner) []string {
	var out []string
	for _, c := range f.Calls {
		if len(c.Args) >= 2 && c.Args[0] == "branch" && (c.Args[1] == "-d" || c.Args[1] == "-D") {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

// The gap this guard was built for: a protected branch that no worktree
// holds used to be deletable, because occupancy was the only check on the
// `worktree add` orphan path.
func TestDeleteBranchGuarded_RefusesProtectedNotCheckedOut(t *testing.T) {
	r := newDeleteRunner("worktree /repo\nbranch refs/heads/develop\n")
	err := deleteBranchGuarded(context.Background(), r, protectedCfg("main", "develop"),
		"main", branchDeleteOpts{Force: true})
	if err == nil {
		t.Fatal("a protected branch must not be deletable just because no worktree holds it")
	}
	if !errors.Is(err, errBranchProtected) {
		t.Errorf("refusal should be recognisable as protection, got %v", err)
	}
	if got := deleteCalls(r); len(got) != 0 {
		t.Errorf("no git delete may run after a refusal, got %v", got)
	}
}

// Force is about git's merged check, not about gk's policy — it must not
// double as permission to remove trunk.
func TestDeleteBranchGuarded_ForceDoesNotLiftProtection(t *testing.T) {
	r := newDeleteRunner("worktree /repo\nbranch refs/heads/feat/x\n")
	err := deleteBranchGuarded(context.Background(), r, protectedCfg("main"),
		"main", branchDeleteOpts{Force: true})
	if !errors.Is(err, errBranchProtected) {
		t.Errorf("--force must not bypass branch.protected, got %v", err)
	}
}

// AllowProtected is the explicit per-branch approval a confirm flow gives.
func TestDeleteBranchGuarded_AllowProtectedLetsItThrough(t *testing.T) {
	r := newDeleteRunner("worktree /repo\nbranch refs/heads/feat/x\n")
	err := deleteBranchGuarded(context.Background(), r, protectedCfg("main"),
		"main", branchDeleteOpts{Force: true, AllowProtected: true})
	if err != nil {
		t.Fatalf("confirmed deletion should proceed, got %v", err)
	}
	if got := deleteCalls(r); len(got) != 1 || got[0] != "branch -D main" {
		t.Errorf("want a single `branch -D main`, got %v", got)
	}
}

// The base branch is protected even when the user never listed it: losing
// it breaks every ahead/behind and merge-target computation gk makes.
func TestDeleteBranchGuarded_BaseBranchProtectedImplicitly(t *testing.T) {
	cfg := &config.Config{BaseBranch: "trunk"}
	r := newDeleteRunner("worktree /repo\nbranch refs/heads/feat/x\n")
	if err := deleteBranchGuarded(context.Background(), r, cfg, "trunk",
		branchDeleteOpts{Force: true}); !errors.Is(err, errBranchProtected) {
		t.Errorf("base branch should be protected without being listed, got %v", err)
	}
}

// A branch a worktree holds is refused before git gets a chance, and the
// refusal names the worktree so the user knows what to deal with.
func TestDeleteBranchGuarded_RefusesCheckedOutAndNamesWorktree(t *testing.T) {
	r := newDeleteRunner("worktree /repo\nbranch refs/heads/develop\n\nworktree /wt/featx\nbranch refs/heads/feat/x\n")
	err := deleteBranchGuarded(context.Background(), r, protectedCfg(), "feat/x",
		branchDeleteOpts{Force: true})
	if err == nil {
		t.Fatal("expected a refusal for a checked-out branch")
	}
	if !strings.Contains(err.Error(), "/wt/featx") {
		t.Errorf("refusal should name the holding worktree, got %v", err)
	}
	if got := deleteCalls(r); len(got) != 0 {
		t.Errorf("no git delete may run after a refusal, got %v", got)
	}
}

// SelfCreated covers rollback and --cleanup: a branch this very command
// made cannot be one the user wanted kept, even if it shares a name on
// the protected list by coincidence.
func TestDeleteBranchGuarded_SelfCreatedSkipsEveryCheck(t *testing.T) {
	r := newDeleteRunner("worktree /repo\nbranch refs/heads/develop\n")
	err := deleteBranchGuarded(context.Background(), r, protectedCfg("main"),
		"main", branchDeleteOpts{Force: true, SelfCreated: true})
	if err != nil {
		t.Fatalf("rollback of a self-created branch must not be blocked, got %v", err)
	}
	if got := deleteCalls(r); len(got) != 1 {
		t.Errorf("want the delete to run, got %v", got)
	}
}

func TestDeleteBranchGuarded_ForceSelectsCapitalD(t *testing.T) {
	for _, tc := range []struct {
		force bool
		want  string
	}{{false, "branch -d feat/x"}, {true, "branch -D feat/x"}} {
		r := newDeleteRunner("worktree /repo\nbranch refs/heads/develop\n")
		if err := deleteBranchGuarded(context.Background(), r, protectedCfg(), "feat/x",
			branchDeleteOpts{Force: tc.force}); err != nil {
			t.Fatalf("force=%v: %v", tc.force, err)
		}
		if got := deleteCalls(r); len(got) != 1 || got[0] != tc.want {
			t.Errorf("force=%v: want %q, got %v", tc.force, tc.want, got)
		}
	}
}

func TestDeleteBranchGuarded_EmptyNameRefused(t *testing.T) {
	r := newDeleteRunner("")
	if err := deleteBranchGuarded(context.Background(), r, nil, "  ",
		branchDeleteOpts{}); err == nil {
		t.Error("an empty branch name must not reach git")
	}
}

// A nil config means "no policy configured", not "protect nothing by
// accident" — it must still be safe to call.
func TestProtectedBranchNames(t *testing.T) {
	if got := protectedBranchNames(nil); got != nil {
		t.Errorf("nil cfg should yield no names, got %v", got)
	}
	cfg := &config.Config{BaseBranch: "main"}
	cfg.Branch.Protected = []string{"main", "develop"}
	got := protectedBranchNames(cfg)
	if len(got) != 2 {
		t.Errorf("base already listed must not duplicate, got %v", got)
	}
}

// --- orphan prompt (item 2: hide, don't confirm) ---

// Non-TTY is the only path assertable without a terminal, and it carries
// the same policy: a protected name is never offered for deletion.
func TestPromptOrphanBranchResolution_ProtectedNeverOffersDelete(t *testing.T) {
	_, err := promptOrphanBranchResolution("main", "tip: abc", true)
	if err == nil {
		t.Fatal("expected a refusal for a protected orphan name")
	}
	if strings.Contains(err.Error(), "branch -D") {
		t.Errorf("must not suggest deleting a protected branch, got %v", err)
	}
	if !strings.Contains(err.Error(), "protected") {
		t.Errorf("the reason should be stated, got %v", err)
	}
}

func TestPromptOrphanBranchResolution_UnprotectedStillOffersDelete(t *testing.T) {
	_, err := promptOrphanBranchResolution("feat/x", "tip: abc", false)
	if err == nil {
		t.Fatal("expected the non-TTY error")
	}
	if !strings.Contains(err.Error(), "branch -D") {
		t.Errorf("an ordinary orphan keeps the delete escape hatch, got %v", err)
	}
}
