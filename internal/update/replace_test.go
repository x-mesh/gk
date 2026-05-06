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
