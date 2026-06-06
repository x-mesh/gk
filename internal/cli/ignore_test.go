package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

// setupIgnoreRepo creates a temp git repo with one committed (tracked) file
// and one untracked file. It returns the runner and the repo root (resolved
// via git so macOS /var symlinks don't break path math).
func setupIgnoreRepo(t *testing.T) (git.Runner, string) {
	t.Helper()
	dir := t.TempDir()
	runner := &git.ExecRunner{Dir: dir}
	ctx := context.Background()

	must := func(args ...string) {
		if _, stderr, err := runner.Run(ctx, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, stderr)
		}
	}
	must("init")
	must("config", "user.email", "t@example.com")
	must("config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must("add", "tracked.txt")
	must("commit", "-m", "add tracked.txt")

	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("noise\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := gitToplevel(ctx, runner)
	if err != nil || root == "" {
		t.Fatalf("gitToplevel: %v", err)
	}
	return runner, root
}

func TestIgnore_UntracksTrackedFileButKeepsIt(t *testing.T) {
	runner, root := setupIgnoreRepo(t)
	ctx := context.Background()

	targets, err := resolveIgnoreTargets(ctx, runner, root, []string{
		filepath.Join(root, "tracked.txt"),
		filepath.Join(root, "untracked.txt"),
	})
	if err != nil {
		t.Fatalf("resolveIgnoreTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("want 2 targets, got %d", len(targets))
	}
	if !targets[0].tracked {
		t.Error("tracked.txt should be detected as tracked")
	}
	if targets[1].tracked {
		t.Error("untracked.txt should be detected as untracked")
	}

	var buf bytes.Buffer
	if err := applyIgnore(ctx, runner, root, &buf, targets, false); err != nil {
		t.Fatalf("applyIgnore: %v", err)
	}

	// .gitignore now lists both paths.
	data, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	for _, want := range []string{"tracked.txt", "untracked.txt"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf(".gitignore missing %q; got:\n%s", want, data)
		}
	}

	// tracked.txt is no longer tracked...
	if _, _, err := runner.Run(ctx, "ls-files", "--error-unmatch", "--", "tracked.txt"); err == nil {
		t.Error("tracked.txt should no longer be tracked after ignore")
	}
	// ...but the working-tree file is preserved (the user's key requirement).
	if _, err := os.Stat(filepath.Join(root, "tracked.txt")); err != nil {
		t.Errorf("tracked.txt working file should still exist: %v", err)
	}
}

func TestIgnore_CommitFinalizes(t *testing.T) {
	runner, root := setupIgnoreRepo(t)
	ctx := context.Background()

	targets, err := resolveIgnoreTargets(ctx, runner, root, []string{filepath.Join(root, "tracked.txt")})
	if err != nil {
		t.Fatalf("resolveIgnoreTargets: %v", err)
	}

	var buf bytes.Buffer
	if err := applyIgnore(ctx, runner, root, &buf, targets, true); err != nil {
		t.Fatalf("applyIgnore --commit: %v", err)
	}

	// After --commit there are no staged/modified leftovers (the pre-existing
	// untracked.txt noise — "??" — is expected and ignored).
	status, _, err := runner.Run(ctx, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(status)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "??") {
			continue
		}
		t.Errorf("unexpected uncommitted change after --commit: %q", line)
	}
	// HEAD subject is the generated ignore message.
	subj, _, _ := runner.Run(ctx, "log", "-1", "--format=%s")
	if !bytes.Contains(subj, []byte("ignore")) {
		t.Errorf("commit subject should mention ignore, got %q", subj)
	}
}

func TestRepoRelPath_RejectsOutsideRepo(t *testing.T) {
	_, root := setupIgnoreRepo(t)
	if _, err := repoRelPath(root, filepath.Join(root, "..", "escape.txt")); err == nil {
		t.Error("expected error for path outside the repository")
	}
}
