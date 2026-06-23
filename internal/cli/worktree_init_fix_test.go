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

func TestEscapesRoot(t *testing.T) {
	root := filepath.Join("/x", "main")
	if !escapesRoot(root, filepath.Join(root, "../../etc/passwd")) {
		t.Error("`../../etc/passwd` should be flagged as escaping")
	}
	if escapesRoot(root, filepath.Join(root, ".env")) {
		t.Error(".env should not escape")
	}
	if escapesRoot(root, filepath.Join(root, "a/b/.env")) {
		t.Error("nested .env should not escape")
	}
}

func TestLinkTargetsEqual(t *testing.T) {
	if !linkTargetsEqual("/a/b/.env", "/a/b/.env") {
		t.Error("identical targets")
	}
	if !linkTargetsEqual("/a/b/.env", "/a/b/../b/.env") {
		t.Error("clean-equivalent targets")
	}
	if linkTargetsEqual("/a/b/.env", "/a/c/.env") {
		t.Error("different dir should be unequal")
	}
	if linkTargetsEqual("/a/b/.env", "/a/b/.other") {
		t.Error("different basename should be unequal")
	}
}

// TestCopyFromMain_ReconcilesPartialDir guards D1: a half-applied directory copy
// is COMPLETED on re-run (missing children filled) while an existing,
// user-edited file is preserved (never clobbered).
func TestCopyFromMain_ReconcilesPartialDir(t *testing.T) {
	main, target := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(main, "config", f+".txt"), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// target/config exists with only a.txt, and a.txt carries a local edit.
	if err := os.MkdirAll(filepath.Join(target, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config", "a.txt"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := copyFromMain(&buf, main, target, "config", false); err != nil {
		t.Fatalf("copyFromMain: %v", err)
	}
	for _, f := range []string{"b", "c"} {
		if _, err := os.Stat(filepath.Join(target, "config", f+".txt")); err != nil {
			t.Errorf("%s.txt not reconciled: %v", f, err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(target, "config", "a.txt"))
	if string(got) != "edited" {
		t.Errorf("existing a.txt clobbered: got %q, want edited", got)
	}
}

// TestCopyFromMain_DereferencesSymlinkSource guards D4: a copy of a symlinked
// source yields a REAL independent file, not a link that re-resolves here.
func TestCopyFromMain_DereferencesSymlinkSource(t *testing.T) {
	main, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(main, ".env.shared"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env.shared", filepath.Join(main, ".env")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := copyFromMain(&buf, main, target, ".env", false); err != nil {
		t.Fatalf("copyFromMain: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(target, ".env"))
	if err != nil {
		t.Fatalf("target .env missing: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("target .env is a symlink; want a real independent file")
	}
	got, _ := os.ReadFile(filepath.Join(target, ".env"))
	if string(got) != "secret" {
		t.Errorf("content = %q, want secret", got)
	}
}

// TestLinkFromMain_RepointsDanglingLink guards D3: a stale (dangling) symlink we
// own — the main-worktree-moved case — is re-pointed on re-run.
func TestLinkFromMain_RepointsDanglingLink(t *testing.T) {
	main, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dangling link pointing at a now-nonexistent old main path.
	if err := os.Symlink(filepath.Join(t.TempDir(), "old", ".env"), filepath.Join(target, ".env")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := linkFromMain(&buf, main, target, ".env", false); err != nil {
		t.Fatalf("linkFromMain: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(target, ".env"))
	if err != nil {
		t.Fatalf("link still dangling: %v", err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(main, ".env"))
	if resolved != want {
		t.Errorf("re-pointed to %q, want %q", resolved, want)
	}
	if !strings.Contains(buf.String(), "re-pointed") {
		t.Errorf("expected a re-pointed message, got: %q", buf.String())
	}
}

// TestLinkFromMain_KeepsValidUserSymlink guards D3's safety bound: a VALID
// symlink the user deliberately placed (resolves fine) is never clobbered.
func TestLinkFromMain_KeepsValidUserSymlink(t *testing.T) {
	main, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("main"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.env")
	if err := os.WriteFile(other, []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(target, ".env")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := linkFromMain(&buf, main, target, ".env", false); err != nil {
		t.Fatalf("linkFromMain: %v", err)
	}
	cur, _ := os.Readlink(filepath.Join(target, ".env"))
	if cur != other {
		t.Errorf("valid user symlink clobbered: now points at %q, want %q", cur, other)
	}
	if !strings.Contains(buf.String(), "skipped") {
		t.Errorf("expected skip, got: %q", buf.String())
	}
}

// TestInitEscapeGuard guards D5: a `..` rel that would escape the worktree is
// refused for both link and copy.
func TestInitEscapeGuard(t *testing.T) {
	main, target := t.TempDir(), t.TempDir()
	var buf bytes.Buffer
	if err := linkFromMain(&buf, main, target, "../../../etc/passwd", false); err != nil {
		t.Fatalf("linkFromMain: %v", err)
	}
	if err := copyFromMain(&buf, main, target, "../../../etc/passwd", false); err != nil {
		t.Fatalf("copyFromMain: %v", err)
	}
	if c := strings.Count(buf.String(), "escapes"); c != 2 {
		t.Errorf("expected 2 escape skips, got %d: %q", c, buf.String())
	}
}

// TestMainWorktreePath_SkipsBareRepo guards D2: in a bare + worktrees layout the
// bare repo is listed first; mainWorktreePath must return the real worktree.
func TestMainWorktreePath_SkipsBareRepo(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	srcRunner := &git.ExecRunner{Dir: src}
	mustGit(t, ctx, srcRunner, "init")
	mustGit(t, ctx, srcRunner, "config", "user.email", "t@e.com")
	mustGit(t, ctx, srcRunner, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, srcRunner, "add", ".")
	mustGit(t, ctx, srcRunner, "commit", "-m", "init")

	bare := filepath.Join(root, "repo.git")
	mustGit(t, ctx, &git.ExecRunner{Dir: root}, "clone", "--bare", src, bare)

	bareRunner := &git.ExecRunner{Dir: bare}
	wt := filepath.Join(root, "wt")
	mustGit(t, ctx, bareRunner, "worktree", "add", wt)

	got, err := mainWorktreePath(ctx, bareRunner)
	if err != nil {
		t.Fatalf("mainWorktreePath: %v", err)
	}
	if sameDir(got, bare) {
		t.Errorf("returned the bare repo %q; want the worktree", got)
	}
	if !sameDir(got, wt) {
		t.Errorf("got %q, want worktree %q", got, wt)
	}
}

func mustGit(t *testing.T, ctx context.Context, r *git.ExecRunner, args ...string) {
	t.Helper()
	if _, stderr, err := r.Run(ctx, args...); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, string(stderr))
	}
}
