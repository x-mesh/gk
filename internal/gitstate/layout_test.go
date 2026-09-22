package gitstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-qm", "init")
}

// TestLayoutDescribes_MainWorktree covers the ordinary case in both directions:
// the layout git reported is accepted, and one belonging to a different
// repository is not — even though that other common dir exists perfectly well.
func TestLayoutDescribes_MainWorktree(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	initRepo(t, a)
	initRepo(t, b)

	if !LayoutDescribes(a, filepath.Join(a, ".git")) {
		t.Error("a repo's own layout was rejected")
	}
	if LayoutDescribes(a, filepath.Join(b, ".git")) {
		t.Error("another repo's existing common dir was accepted for this path")
	}
}

// TestLayoutDescribes_ReplacedRepoAtSamePath is the hole this check closes. The
// worktree at a path is removed while its common dir survives elsewhere (the
// main repo still holds it), then a different repository appears at that same
// path. Checking only that the common dir exists would keep answering with the
// old repository's layout for the rest of the process.
func TestLayoutDescribes_ReplacedRepoAtSamePath(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	initRepo(t, main)
	linked := filepath.Join(root, "linked")
	run(t, main, "worktree", "add", "-q", linked, "-b", "feat")

	common := filepath.Join(main, ".git")
	if !LayoutDescribes(linked, common) {
		t.Fatal("the linked worktree's own layout was rejected")
	}

	// The worktree goes away; the common dir does not.
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	if !dirExists(common) {
		t.Fatal("the common dir should have survived — the scenario needs it to")
	}
	initRepo(t, linked) // something else now lives at that path

	if LayoutDescribes(linked, common) {
		t.Error("a replaced repo still matched the old layout — the cache would answer for the wrong repository")
	}
}

// TestLayoutDescribes_BareRepo pins the degraded case: a bare repository has no
// .git entry to follow, so existence of the common dir is all that can be
// established without a fork. It must not evict on that.
func TestLayoutDescribes_BareRepo(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	initRepo(t, src)
	bare := filepath.Join(root, "bare.git")
	run(t, root, "clone", "-q", "--bare", src, bare)

	if !LayoutDescribes(bare, bare) {
		t.Error("a bare repository's own layout was rejected")
	}
	if LayoutDescribes(bare, filepath.Join(root, "gone")) {
		t.Error("a missing common dir was accepted")
	}
}

// TestLayoutDescribes_ReplacedLinkedWorktree covers the other branch: the path
// still holds a LINKED worktree, but of a different repository. Following .git
// as a directory would never be reached here — the check has to read the file
// and compare the gitdir it names.
func TestLayoutDescribes_ReplacedLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	initRepo(t, a)
	initRepo(t, b)

	wt := filepath.Join(root, "wt")
	run(t, a, "worktree", "add", "-q", wt, "-b", "feat")
	aCommon := filepath.Join(a, ".git")
	if !LayoutDescribes(wt, aCommon) {
		t.Fatal("a's own linked worktree was rejected")
	}

	// Hand that same path to b instead.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	run(t, a, "worktree", "prune")
	run(t, b, "worktree", "add", "-q", wt, "-b", "feat")

	if LayoutDescribes(wt, aCommon) {
		t.Error("a linked worktree of another repo matched a's layout — the cache would answer for the wrong repository")
	}
	if !LayoutDescribes(wt, filepath.Join(b, ".git")) {
		t.Error("b's own linked worktree was rejected")
	}
}

// TestLayoutDescribes_GitDirIsCommonDir covers the layouts where git reports the
// gitdir and the common dir as the same directory. A submodule and a repository
// created with --separate-git-dir are both like that, and accepting only the
// linked-worktree shape (<common>/worktrees/<name>) rejected them outright — so
// a correct entry was evicted on every call and rev-parse re-forked for an
// answer that had not changed.
func TestLayoutDescribes_GitDirIsCommonDir(t *testing.T) {
	root := t.TempDir()

	t.Run("submodule", func(t *testing.T) {
		super, child := filepath.Join(root, "super"), filepath.Join(root, "child")
		initRepo(t, super)
		initRepo(t, child)
		run(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", child, "sub")

		sub := filepath.Join(super, "sub")
		common, _, err := Dirs(context.Background(), sub)
		if err != nil {
			t.Fatalf("Dirs: %v", err)
		}
		if !LayoutDescribes(sub, common) {
			t.Errorf("a submodule's own layout (%s) was rejected", common)
		}
		// A different repo's common dir must still not match.
		if LayoutDescribes(sub, filepath.Join(child, ".git")) {
			t.Error("another repo's common dir was accepted for the submodule path")
		}
	})

	t.Run("separate-git-dir", func(t *testing.T) {
		wt, elsewhere := filepath.Join(root, "wt"), filepath.Join(root, "elsewhere")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		run(t, root, "init", "-q", "-b", "main", "--separate-git-dir="+elsewhere, wt)

		common, _, err := Dirs(context.Background(), wt)
		if err != nil {
			t.Fatalf("Dirs: %v", err)
		}
		if !LayoutDescribes(wt, common) {
			t.Errorf("a --separate-git-dir layout (%s) was rejected", common)
		}
	})
}
