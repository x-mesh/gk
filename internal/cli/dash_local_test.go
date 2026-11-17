package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/testutil"
)

// dashLocalRepo is a repository with an origin, a pushed branch fix/a in a
// second worktree, a local-only branch fix/b, and main one commit behind
// origin/main.
func dashLocalRepo(t *testing.T) (*testutil.Repo, string) {
	t.Helper()
	r := testutil.NewRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	r.RunGit("init", "--bare", "-q", remote)
	r.AddRemote("origin", remote)
	r.RunGit("push", "-q", "-u", "origin", "main")

	r.CreateBranch("fix/a")
	r.WriteFile("a.txt", "a\n")
	r.Commit("a")
	r.RunGit("push", "-q", "-u", "origin", "fix/a")
	r.Checkout("main")
	r.RunGit("branch", "fix/b")

	r.WriteFile("m.txt", "m\n")
	r.Commit("m")
	r.RunGit("push", "-q", "origin", "main")
	r.RunGit("reset", "-q", "--hard", "HEAD~1")

	wt := filepath.Join(t.TempDir(), "wt-a")
	r.RunGit("worktree", "add", "-q", wt, "fix/a")
	return r, wt
}

func dashBranchByName(t *testing.T, loc dashLocal, name string) dashLocalBranch {
	t.Helper()
	for _, b := range loc.Branches {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("branch %s not listed in %+v", name, loc.Branches)
	return dashLocalBranch{}
}

func TestCollectDashLocal(t *testing.T) {
	r, wt := dashLocalRepo(t)
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runner := &git.ExecRunner{Dir: r.Dir}
	before := r.RunGit("for-each-ref") + r.RunGit("status", "--porcelain")

	loc, err := collectDashLocal(ctx, runner, "origin", []string{"main", "develop"})
	if err != nil {
		t.Fatalf("collectDashLocal: %v", err)
	}
	if after := r.RunGit("for-each-ref") + r.RunGit("status", "--porcelain"); after != before {
		t.Errorf("refs or status changed:\n%s\n---\n%s", before, after)
	}

	a := dashBranchByName(t, loc, "fix/a")
	if a.Upstream != "origin/fix/a" || !samePath(a.Worktree, wt) || a.Current || a.Tree != dashTreeDirty {
		t.Errorf("fix/a = %+v, want upstream origin/fix/a in %s, dirty, not current", a, wt)
	}
	m := dashBranchByName(t, loc, "main")
	if !m.Current || m.Tree != dashTreeClean {
		t.Errorf("main = %+v, want the current clean worktree", m)
	}
	if b := dashBranchByName(t, loc, "fix/b"); b.Worktree != "" || b.Tree != dashTreeNone || b.Upstream != "" {
		t.Errorf("fix/b = %+v, want no worktree and no upstream", b)
	}
	if len(loc.Behind) != 1 || loc.Behind[0] != (dashBaseBehind{Base: "main", Behind: 1}) {
		t.Errorf("behind = %+v, want main 1 and no develop entry", loc.Behind)
	}
}

// A worktree whose git status fails is unknown, not clean.
func TestCollectDashLocalUnreadableWorktreeIsUnknown(t *testing.T) {
	r, wt := dashLocalRepo(t)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	loc, err := collectDashLocal(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", nil)
	if err != nil {
		t.Fatalf("collectDashLocal: %v", err)
	}
	if a := dashBranchByName(t, loc, "fix/a"); a.Tree != dashTreeUnknown {
		t.Errorf("fix/a tree = %q, want unknown", a.Tree)
	}
}

func TestDashLocalLinkedBranches(t *testing.T) {
	loc := dashLocal{Remote: "origin", Branches: []dashLocalBranch{
		{Name: "fix/a", Tip: "a1", Upstream: "origin/fix/a"},
		{Name: "pr-a", Tip: "a1", Upstream: "origin/fix/a"},
		{Name: "fix/b", Tip: "b1"},
		{Name: "fix/c", Tip: "c1", Upstream: "other/fix/c"},
		{Name: "main", Tip: "f1"},
	}}
	names := func(bs []dashLocalBranch) []string {
		var out []string
		for _, b := range bs {
			out = append(out, b.Name)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		pr   ghapi.DashPR
		want []string
	}{
		{"tracks the head, two branches", ghapi.DashPR{HeadRef: "fix/a", HeadOID: "zz"}, []string{"fix/a", "pr-a"}},
		{"same name without upstream", ghapi.DashPR{HeadRef: "fix/b", HeadOID: "zz"}, []string{"fix/b"}},
		{"same name tracking another remote", ghapi.DashPR{HeadRef: "fix/c", HeadOID: "zz"}, nil},
		{"fork matches the head commit only", ghapi.DashPR{HeadRef: "main", HeadOID: "b1", CrossRepo: true}, []string{"fix/b"}},
		{"fork with the name of a local branch", ghapi.DashPR{HeadRef: "main", HeadOID: "zz", CrossRepo: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := names(loc.linkedBranches(tc.pr))
			if len(got) != len(tc.want) {
				t.Fatalf("linked = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("linked = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
