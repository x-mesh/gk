package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxFixture builds a real directory tree exercising every boundary:
//
//	root/
//	  a.go
//	  .env                  (deny glob target)
//	  docs/guide.md
//	  sub/                  (fake submodule: has a .git file)
//	    .git
//	    inner.txt
//	  link-out → /tmp-ish outside dir
//	  link-in  → a.go
//	outside/
//	  secret.txt
func sandboxFixture(t *testing.T) (*Sandbox, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(root, "docs"), filepath.Join(root, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(root, "a.go"):             "package a",
		filepath.Join(root, ".env"):             "SECRET=x",
		filepath.Join(root, "docs", "guide.md"): "# guide",
		filepath.Join(root, "sub", ".git"):      "gitdir: ../../.git/modules/sub",
		filepath.Join(root, "sub", "inner.txt"): "inner",
		filepath.Join(outside, "secret.txt"):    "outside secret",
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a.go"), filepath.Join(root, "link-in")); err != nil {
		t.Fatal(err)
	}
	sb, err := NewSandbox(root, []string{".env", "*.pem"})
	if err != nil {
		t.Fatal(err)
	}
	return sb, root, outside
}

func TestSandboxAllowsNormalPaths(t *testing.T) {
	sb, _, _ := sandboxFixture(t)
	for _, p := range []string{"a.go", "docs/guide.md", "./docs/../a.go"} {
		abs, rel, err := sb.Resolve(p)
		if err != nil {
			t.Errorf("Resolve(%q): %v", p, err)
			continue
		}
		if !strings.HasPrefix(abs, sb.Root) {
			t.Errorf("Resolve(%q) abs = %q, not under root", p, abs)
		}
		if strings.Contains(rel, "\\") {
			t.Errorf("rel %q must be slash-separated", rel)
		}
	}
}

// An in-repo symlink whose TARGET is inside the repo is fine.
func TestSandboxAllowsInternalSymlink(t *testing.T) {
	sb, _, _ := sandboxFixture(t)
	_, rel, err := sb.Resolve("link-in")
	if err != nil {
		t.Fatalf("Resolve(link-in): %v", err)
	}
	if rel != "a.go" {
		t.Errorf("rel = %q, want a.go (resolved target)", rel)
	}
}

func TestSandboxBlocksTraversalAndAbsolute(t *testing.T) {
	sb, _, outside := sandboxFixture(t)
	denied := []string{
		"../outside/secret.txt",
		"docs/../../outside/secret.txt",
		filepath.Join(outside, "secret.txt"), // absolute, outside
		"..",
	}
	for _, p := range denied {
		if _, _, err := sb.Resolve(p); err == nil {
			t.Errorf("Resolve(%q): want containment error, got nil", p)
		}
	}
	// Absolute path INSIDE the repo is acceptable (models echo them).
	if _, _, err := sb.Resolve(filepath.Join(sb.Root, "a.go")); err != nil {
		t.Errorf("absolute in-repo path should resolve: %v", err)
	}
}

// The signature attack: a symlink named inside the repo pointing outside
// must be denied AFTER resolution, not trusted by its name.
func TestSandboxBlocksSymlinkEscape(t *testing.T) {
	sb, _, _ := sandboxFixture(t)
	for _, p := range []string{"link-out", "link-out/secret.txt"} {
		if _, _, err := sb.Resolve(p); err == nil {
			t.Errorf("Resolve(%q): want escape error, got nil", p)
		}
	}
}

func TestSandboxBlocksGitDir(t *testing.T) {
	sb, root, _ := sandboxFixture(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git", ".git/config", ".git/hooks/pre-commit"} {
		if _, _, err := sb.Resolve(p); err == nil {
			t.Errorf("Resolve(%q): want .git error, got nil", p)
		}
	}
}

func TestSandboxBlocksSubmoduleBoundary(t *testing.T) {
	sb, _, _ := sandboxFixture(t)
	if _, _, err := sb.Resolve("sub/inner.txt"); err == nil {
		t.Error("Resolve(sub/inner.txt): want submodule-boundary error, got nil")
	}
}

func TestSandboxBlocksDenyGlobs(t *testing.T) {
	sb, _, _ := sandboxFixture(t)
	if _, _, err := sb.Resolve(".env"); err == nil {
		t.Error("Resolve(.env): want deny_paths error, got nil")
	}
	if _, _, err := sb.Resolve("keys/server.pem"); err == nil {
		t.Error("Resolve(keys/server.pem): want deny_paths error, got nil")
	}
}

func TestSandboxRejectsDegenerateInput(t *testing.T) {
	sb, _, _ := sandboxFixture(t)
	for _, p := range []string{"", "   "} {
		if _, _, err := sb.Resolve(p); err == nil {
			t.Errorf("Resolve(%q): want error, got nil", p)
		}
	}
}

// Non-existent paths still containment-check via ancestor resolution: an
// existing symlinked ancestor cannot smuggle a missing leaf outside.
func TestSandboxNonExistentPaths(t *testing.T) {
	sb, _, _ := sandboxFixture(t)
	if _, _, err := sb.Resolve("does/not/exist.txt"); err != nil {
		t.Errorf("missing in-repo path should still resolve (I/O errors later): %v", err)
	}
	if _, _, err := sb.Resolve("link-out/missing.txt"); err == nil {
		t.Error("missing leaf under escaping symlink must still be denied")
	}
}

// Unicode and spaces are ordinary path bytes — no special-casing, no
// rejection.
func TestSandboxUnicodePaths(t *testing.T) {
	sb, root, _ := sandboxFixture(t)
	p := filepath.Join(root, "한글 파일.md")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sb.Resolve("한글 파일.md"); err != nil {
		t.Errorf("unicode path: %v", err)
	}
}
