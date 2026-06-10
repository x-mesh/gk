package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReplaceLeavesBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gk")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	staged := filepath.Join(dir, "gk.new")
	if err := os.WriteFile(staged, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := AtomicReplace(staged, target); err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" {
		t.Errorf("target after replace = %q, want %q", got, "NEW")
	}

	bak, err := os.ReadFile(target + ".bak")
	if err != nil {
		t.Fatalf("expected .bak to exist: %v", err)
	}
	if string(bak) != "OLD" {
		t.Errorf(".bak = %q, want %q (previous binary)", bak, "OLD")
	}

	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("staged file should be gone after rename, got err = %v", err)
	}
}

func TestAtomicReplaceMissingStaged(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gk")
	if err := os.WriteFile(target, []byte("X"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "does-not-exist")
	if err := AtomicReplace(staged, target); err == nil {
		t.Fatal("expected error on missing staged file")
	}
}

func TestWritable(t *testing.T) {
	dir := t.TempDir()
	if !writable(dir) {
		t.Errorf("writable(%q) = false, want true", dir)
	}
	// /proc on Linux is read-only when present; on macOS this branch is
	// skipped because the path does not exist.
	if _, err := os.Stat("/proc/self"); err == nil {
		if writable("/proc") {
			t.Errorf("writable(/proc) = true, want false")
		}
	}
}

func TestPickStagingDir(t *testing.T) {
	// Writable dir → returned as-is so the rename stays same-filesystem.
	dir := t.TempDir()
	if got := PickStagingDir(dir); got != dir {
		t.Errorf("PickStagingDir(writable) = %q, want %q", got, dir)
	}

	// Unwritable dir → falls back to os.TempDir(). On Linux /proc is the
	// canonical read-only directory; macOS sandboxes do not expose one,
	// so use a non-existent path which os.CreateTemp also rejects.
	bad := "/proc/cannot-write-here"
	if _, err := os.Stat("/proc/self"); err != nil {
		bad = filepath.Join(t.TempDir(), "does-not-exist")
	}
	if got := PickStagingDir(bad); got != os.TempDir() {
		t.Errorf("PickStagingDir(unwritable) = %q, want %q", got, os.TempDir())
	}
}

func TestLinkAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gk"), []byte("BIN"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := LinkAlias(dir, "gk", "git-kit"); err != nil {
		t.Fatalf("LinkAlias: %v", err)
	}

	alias := filepath.Join(dir, "git-kit")
	// The link must be relative (target "gk", not an absolute path) so it
	// survives the bin dir being relocated.
	if target, err := os.Readlink(alias); err != nil {
		t.Fatalf("alias is not a symlink: %v", err)
	} else if target != "gk" {
		t.Errorf("alias target = %q, want relative %q", target, "gk")
	}
	// And it must resolve to the real binary's bytes.
	if got, err := os.ReadFile(alias); err != nil {
		t.Fatalf("read through alias: %v", err)
	} else if string(got) != "BIN" {
		t.Errorf("alias resolves to %q, want BIN", got)
	}
}

func TestLinkAliasReplacesStale(t *testing.T) {
	// A pre-existing alias — here a plain copy, as install.sh's cp fallback
	// would leave — must be replaced by a fresh symlink, not left stale.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gk"), []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "git-kit"), []byte("OLD-COPY"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := LinkAlias(dir, "gk", "git-kit"); err != nil {
		t.Fatalf("LinkAlias: %v", err)
	}

	alias := filepath.Join(dir, "git-kit")
	if target, err := os.Readlink(alias); err != nil {
		t.Fatalf("stale copy was not replaced by a symlink: %v", err)
	} else if target != "gk" {
		t.Errorf("alias target = %q, want %q", target, "gk")
	}
	if got, _ := os.ReadFile(alias); string(got) != "NEW" {
		t.Errorf("alias resolves to %q, want NEW", got)
	}
}

func TestAtomicReplaceWithSudoFastPath(t *testing.T) {
	// Confirms the writable-dir fast path bypasses sudo entirely; we set up
	// a writable temp dir so sudo is never invoked and the test is hermetic.
	dir := t.TempDir()
	target := filepath.Join(dir, "gk")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "gk.new")
	if err := os.WriteFile(staged, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicReplaceWithSudo(staged, target); err != nil {
		t.Fatalf("AtomicReplaceWithSudo: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "NEW" {
		t.Errorf("target = %q, want NEW", got)
	}
}
