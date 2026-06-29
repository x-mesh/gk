package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func fleetGitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
}

func TestDiscoverReposScanAndExclude(t *testing.T) {
	root := t.TempDir()
	fleetGitInit(t, filepath.Join(root, "a"))
	fleetGitInit(t, filepath.Join(root, "b"))
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A repo nested inside an excluded dir must NOT be discovered.
	fleetGitInit(t, filepath.Join(root, "node_modules", "pkg"))

	ids, err := discoverRepos(context.Background(), nil, []string{root}, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id.Label] = true
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("expected repos a and b, got %v", ids)
	}
	if got["pkg"] {
		t.Errorf("repo under node_modules should be excluded, got %v", ids)
	}
	if len(ids) != 2 {
		t.Errorf("expected exactly 2 repos, got %d (%v)", len(ids), ids)
	}
}

func TestDiscoverReposExplicitNonGitErrors(t *testing.T) {
	dir := t.TempDir() // not a git repo
	if _, err := discoverRepos(context.Background(), []string{dir}, nil, nil, 2); err == nil {
		t.Fatal("expected an error for a non-git explicit path")
	}
}

func TestDiscoverReposDedupBySymlink(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "r")
	fleetGitInit(t, repo)
	link := filepath.Join(root, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skip("symlinks unsupported on this platform")
	}
	// Same repo reached two ways (real path + symlink) collapses to one entry
	// via the git-common-dir dedup key.
	ids, err := discoverRepos(context.Background(), []string{repo, link}, nil, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("symlinked duplicate should collapse to 1 repo, got %d (%v)", len(ids), ids)
	}
}

func TestDiscoverReposDepthLimit(t *testing.T) {
	root := t.TempDir()
	// root/x/y/deep is 3 directories below root.
	fleetGitInit(t, filepath.Join(root, "x", "y", "deep"))

	if ids, _ := discoverRepos(context.Background(), nil, []string{root}, nil, 2); len(ids) != 0 {
		t.Errorf("depth-3 repo must be missed at maxDepth=2, got %v", ids)
	}
	if ids, _ := discoverRepos(context.Background(), nil, []string{root}, nil, 3); len(ids) != 1 {
		t.Errorf("depth-3 repo must be found at maxDepth=3, got %v", ids)
	}
}
