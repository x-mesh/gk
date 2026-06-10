package forget

// Differential testing: the native engine and git filter-repo must produce
// IDENTICAL histories — compared by raw SHA over every branch and tag —
// for the path-removal slice. filter-repo runs with
// --preserve-commit-hashes because the v1 native engine deliberately does
// not rewrite SHA references inside commit messages.

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/testutil"
)

// copyRepo clones the repo directory byte-for-byte so both engines start
// from the same object store and refs.
func copyRepo(t *testing.T, src *testutil.Repo) *testutil.Repo {
	t.Helper()
	dst := t.TempDir()
	if out, err := exec.Command("cp", "-R", src.Dir+"/.", dst).CombinedOutput(); err != nil {
		t.Fatalf("copy repo: %v\n%s", err, out)
	}
	return testutil.Attach(t, dst)
}

// refSnapshot maps every branch and tag to its target SHA.
func refSnapshot(t *testing.T, r *testutil.Repo) map[string]string {
	t.Helper()
	out := r.RunGit("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags")
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}

func diffEngines(t *testing.T, build func(r *testutil.Repo), targets []string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	if _, err := exec.LookPath("git-filter-repo"); err != nil {
		t.Skip("git-filter-repo not installed")
	}
	src := testutil.NewRepo(t)
	build(src)

	native := copyRepo(t, src)
	delegated := copyRepo(t, src)

	if _, err := RunNative(context.Background(), native.Dir, native.GitDir, targets); err != nil {
		t.Fatalf("native engine: %v", err)
	}

	args := []string{"filter-repo", "--invert-paths", "--force", "--preserve-commit-hashes"}
	for _, p := range targets {
		args = append(args, "--path", p)
	}
	delegated.RunGit(args...)

	got := refSnapshot(t, native)
	want := refSnapshot(t, delegated)
	for ref, sha := range want {
		if got[ref] != sha {
			t.Errorf("ref %s: native=%s filter-repo=%s", ref, got[ref], sha)
		}
	}
	for ref := range got {
		if _, ok := want[ref]; !ok {
			t.Errorf("ref %s: kept by native, deleted by filter-repo", ref)
		}
	}
	// belt and braces: the forgotten paths must be gone on both sides
	if hist := historyPaths(t, native); containsTarget(hist, targets) {
		t.Errorf("native left targets in history:\n%s", hist)
	}
}

func containsTarget(hist string, targets []string) bool {
	for _, line := range strings.Split(hist, "\n") {
		for _, tg := range targets {
			tg = strings.TrimSuffix(tg, "/")
			if line == tg || strings.HasPrefix(line, tg+"/") {
				return true
			}
		}
	}
	return false
}

func TestDiffLinearMixed(t *testing.T) {
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("a.txt", "1\n")
		r.Commit("code 1")
		r.WriteFile(".xm/s.json", "s\n")
		r.Commit("xm only")
		r.WriteFile("a.txt", "1\n2\n")
		r.WriteFile(".xm/t.json", "t\n")
		r.Commit("mixed")
		r.RunGit("commit", "--allow-empty", "-m", "deliberately empty")
	}, []string{".xm"})
}

func TestDiffMergeCollapse(t *testing.T) {
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("a.txt", "base\n")
		r.Commit("base")
		r.RunGit("checkout", "-qb", "side")
		r.WriteFile(".xm/x.json", "1\n")
		r.Commit("xm side 1")
		r.WriteFile(".xm/y.json", "2\n")
		r.Commit("xm side 2")
		r.RunGit("checkout", "-q", "main")
		r.WriteFile("a.txt", "base\nmain\n")
		r.Commit("main code")
		r.RunGit("merge", "-q", "--no-ff", "side", "-m", "merge xm side")
		r.WriteFile("b.txt", "after\n")
		r.Commit("after merge")
	}, []string{".xm"})
}

func TestDiffEvilMerge(t *testing.T) {
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("a.txt", "base\n")
		r.Commit("base")
		r.RunGit("checkout", "-qb", "side")
		r.WriteFile(".xm/s.json", "s\n")
		r.Commit("xm only")
		r.RunGit("checkout", "-q", "main")
		r.WriteFile("b.txt", "m\n")
		r.Commit("main work")
		r.RunGit("merge", "-q", "--no-ff", "--no-commit", "side")
		r.WriteFile("evil.txt", "extra\n")
		r.RunGit("add", ".")
		r.RunGit("commit", "-qm", "evil merge")
	}, []string{".xm"})
}

func TestDiffEvilMergeIdenticalParents(t *testing.T) {
	// the side branch prunes INTO the first parent itself → the merge's
	// parents become identical; the evil content must survive as a
	// normal single-parent commit.
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("a.txt", "base\n")
		r.Commit("base")
		r.RunGit("checkout", "-qb", "side")
		r.WriteFile(".xm/s.json", "s\n")
		r.Commit("xm only")
		r.RunGit("checkout", "-q", "main")
		r.RunGit("merge", "-q", "--no-ff", "--no-commit", "side")
		r.WriteFile("evil.txt", "extra\n")
		r.RunGit("add", ".")
		r.RunGit("commit", "-qm", "evil merge identical")
	}, []string{".xm"})
}

func TestDiffOursMerge(t *testing.T) {
	// merge -s ours discards the side branch's tree: the merge's delta vs
	// its first parent is empty, but splicing it onto the SECOND parent
	// would change content. Both engines must keep history content-correct.
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("a.txt", "base\n")
		r.Commit("base")
		r.RunGit("checkout", "-qb", "side")
		r.WriteFile("side.txt", "real side work\n")
		r.Commit("side code")
		r.WriteFile(".xm/s.json", "s\n")
		r.Commit("xm on side")
		r.RunGit("checkout", "-q", "main")
		r.RunGit("merge", "-q", "-s", "ours", "--no-ff", "side", "-m", "ours merge")
		r.WriteFile("b.txt", "after\n")
		r.Commit("after")
	}, []string{".xm"})
}

func TestDiffPartialBranchMerge(t *testing.T) {
	// side branch with BOTH xm and code commits: merge survives with its
	// side parent remapped, not collapsed.
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("a.txt", "base\n")
		r.Commit("base")
		r.RunGit("checkout", "-qb", "side")
		r.WriteFile(".xm/s.json", "s\n")
		r.Commit("xm on side")
		r.WriteFile("side.txt", "real\n")
		r.Commit("code on side")
		r.RunGit("checkout", "-q", "main")
		r.WriteFile("b.txt", "m\n")
		r.Commit("main work")
		r.RunGit("merge", "-q", "--no-ff", "side", "-m", "merge side")
	}, []string{".xm"})
}

func TestDiffRootPrune(t *testing.T) {
	diffEngines(t, func(r *testutil.Repo) {
		// testutil's initial commit touches only .gkkeep — forgetting it
		// prunes the root.
		r.WriteFile("code.txt", "x\n")
		r.Commit("real work")
		r.WriteFile("code.txt", "x\ny\n")
		r.Commit("more work")
	}, []string{".gkkeep"})
}

func TestDiffTags(t *testing.T) {
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("a.txt", "1\n")
		r.Commit("code")
		r.RunGit("tag", "-a", "v1", "-m", "on kept")
		r.WriteFile(".xm/x.json", "x\n")
		r.Commit("xm only")
		r.RunGit("tag", "-a", "v2", "-m", "on pruned")
		r.RunGit("tag", "light-on-pruned")
		r.WriteFile("b.txt", "2\n")
		r.Commit("more code")
		r.RunGit("tag", "light-on-kept")
	}, []string{".xm"})
}

func TestDiffUnicodePaths(t *testing.T) {
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("정상 코드.txt", "1\n")
		r.Commit("korean kept file")
		r.WriteFile(".xm/op/한글 파일.json", "x\n")
		r.Commit("korean xm file")
		r.WriteFile("with space.txt", "2\n")
		r.Commit("space kept file")
	}, []string{".xm"})
}

func TestDiffMultiBranch(t *testing.T) {
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("a.txt", "1\n")
		r.Commit("shared code")
		r.WriteFile(".xm/x.json", "x\n")
		r.Commit("xm only")
		r.RunGit("branch", "frozen") // second head parked on the pruned tip
		r.WriteFile("b.txt", "2\n")
		r.Commit("main moves on")
	}, []string{".xm"})
}

func TestDiffFileTarget(t *testing.T) {
	// single-file target (not a directory)
	diffEngines(t, func(r *testutil.Repo) {
		r.WriteFile("keep.txt", "k\n")
		r.WriteFile("secret.env", "TOKEN=x\n")
		r.Commit("adds secret")
		r.WriteFile("secret.env", "TOKEN=y\n")
		r.Commit("rotates secret")
		r.WriteFile("keep.txt", "k2\n")
		r.Commit("normal work")
	}, []string{"secret.env"})
}

func TestDiffDeepHistory(t *testing.T) {
	// a longer linear run to exercise the streaming path beyond toy sizes
	diffEngines(t, func(r *testutil.Repo) {
		for i := 0; i < 30; i++ {
			switch i % 3 {
			case 0:
				r.WriteFile("code.txt", strings.Repeat("x", i+1)+"\n")
			case 1:
				r.WriteFile(".xm/state.json", strings.Repeat("s", i+1)+"\n")
			default:
				r.WriteFile("code.txt", strings.Repeat("y", i+1)+"\n")
				r.WriteFile(".xm/plan.md", strings.Repeat("p", i+1)+"\n")
			}
			r.Commit("step " + strings.Repeat("i", i+1))
		}
	}, []string{".xm"})
}
