package gitstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeFile creates a file at path with the given content, creating parent dirs as needed.
func makeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// makeDir creates a directory at path.
func makeDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// TestDetectFromGitDir_None: empty directory → StateNone
func TestDetectFromGitDir_None(t *testing.T) {
	dir := t.TempDir()
	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateNone {
		t.Errorf("want StateNone, got %s", s.Kind)
	}
}

// TestDetectFromGitDir_RebaseMerge: rebase-merge dir with all fields
func TestDetectFromGitDir_RebaseMerge(t *testing.T) {
	dir := t.TempDir()
	rbDir := filepath.Join(dir, "rebase-merge")
	makeDir(t, rbDir)
	makeFile(t, filepath.Join(rbDir, "head-name"), "refs/heads/feat/x\n")
	makeFile(t, filepath.Join(rbDir, "onto"), "abc1234\n")
	makeFile(t, filepath.Join(rbDir, "orig-head"), "def5678\n")
	makeFile(t, filepath.Join(rbDir, "msgnum"), "3\n")
	makeFile(t, filepath.Join(rbDir, "end"), "7\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateRebaseMerge {
		t.Errorf("want StateRebaseMerge, got %s", s.Kind)
	}
	if s.HeadName != "refs/heads/feat/x" {
		t.Errorf("HeadName: want %q, got %q", "refs/heads/feat/x", s.HeadName)
	}
	if s.Onto != "abc1234" {
		t.Errorf("Onto: want %q, got %q", "abc1234", s.Onto)
	}
	if s.OrigHead != "def5678" {
		t.Errorf("OrigHead: want %q, got %q", "def5678", s.OrigHead)
	}
	if s.Current != 3 {
		t.Errorf("Current: want 3, got %d", s.Current)
	}
	if s.Total != 7 {
		t.Errorf("Total: want 7, got %d", s.Total)
	}
}

// TestDetectFromGitDir_RebaseApply: rebase-apply dir with all fields
func TestDetectFromGitDir_RebaseApply(t *testing.T) {
	dir := t.TempDir()
	rbDir := filepath.Join(dir, "rebase-apply")
	makeDir(t, rbDir)
	makeFile(t, filepath.Join(rbDir, "head-name"), "refs/heads/main\n")
	makeFile(t, filepath.Join(rbDir, "onto"), "aabbcc\n")
	makeFile(t, filepath.Join(rbDir, "orig-head"), "112233\n")
	makeFile(t, filepath.Join(rbDir, "next"), "2\n")
	makeFile(t, filepath.Join(rbDir, "last"), "5\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateRebaseApply {
		t.Errorf("want StateRebaseApply, got %s", s.Kind)
	}
	if s.HeadName != "refs/heads/main" {
		t.Errorf("HeadName: want %q, got %q", "refs/heads/main", s.HeadName)
	}
	if s.Onto != "aabbcc" {
		t.Errorf("Onto: want %q, got %q", "aabbcc", s.Onto)
	}
	if s.OrigHead != "112233" {
		t.Errorf("OrigHead: want %q, got %q", "112233", s.OrigHead)
	}
	if s.Current != 2 {
		t.Errorf("Current: want 2, got %d", s.Current)
	}
	if s.Total != 5 {
		t.Errorf("Total: want 5, got %d", s.Total)
	}
}

// TestDetectFromGitDir_Merge: MERGE_HEAD only → StateMerge
func TestDetectFromGitDir_Merge(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, filepath.Join(dir, "MERGE_HEAD"), "aabbccdd\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateMerge {
		t.Errorf("want StateMerge, got %s", s.Kind)
	}
}

// TestDetectFromGitDir_CherryPick: CHERRY_PICK_HEAD → StateCherryPick
func TestDetectFromGitDir_CherryPick(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, filepath.Join(dir, "CHERRY_PICK_HEAD"), "deadbeef\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateCherryPick {
		t.Errorf("want StateCherryPick, got %s", s.Kind)
	}
}

// TestDetectFromGitDir_RebaseMergeWinsOverMerge: rebase-merge + MERGE_HEAD → rebase-merge wins
func TestDetectFromGitDir_RebaseMergeWinsOverMerge(t *testing.T) {
	dir := t.TempDir()
	makeDir(t, filepath.Join(dir, "rebase-merge"))
	makeFile(t, filepath.Join(dir, "MERGE_HEAD"), "aabbccdd\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateRebaseMerge {
		t.Errorf("want StateRebaseMerge (priority over merge), got %s", s.Kind)
	}
}

// TestDetectFromGitDir_BadNumbers: non-numeric msgnum/end → Current/Total=0, no error
func TestDetectFromGitDir_BadNumbers(t *testing.T) {
	dir := t.TempDir()
	rbDir := filepath.Join(dir, "rebase-merge")
	makeDir(t, rbDir)
	makeFile(t, filepath.Join(rbDir, "msgnum"), "notanumber\n")
	makeFile(t, filepath.Join(rbDir, "end"), "also-bad\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateRebaseMerge {
		t.Errorf("want StateRebaseMerge, got %s", s.Kind)
	}
	if s.Current != 0 {
		t.Errorf("Current: want 0, got %d", s.Current)
	}
	if s.Total != 0 {
		t.Errorf("Total: want 0, got %d", s.Total)
	}
}

// TestDetectFromGitDir_Revert: REVERT_HEAD → StateRevert
func TestDetectFromGitDir_Revert(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, filepath.Join(dir, "REVERT_HEAD"), "cafecafe\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateRevert {
		t.Errorf("want StateRevert, got %s", s.Kind)
	}
}

// TestDetectFromGitDir_Bisect: BISECT_LOG → StateBisect
func TestDetectFromGitDir_Bisect(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, filepath.Join(dir, "BISECT_LOG"), "# bad: deadbeef\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateBisect {
		t.Errorf("want StateBisect, got %s", s.Kind)
	}
}

// TestDetectFromGitDir_RebaseApply_AbortSafety: orig-head missing, uses abort-safety fallback
func TestDetectFromGitDir_RebaseApply_AbortSafety(t *testing.T) {
	dir := t.TempDir()
	rbDir := filepath.Join(dir, "rebase-apply")
	makeDir(t, rbDir)
	makeFile(t, filepath.Join(rbDir, "head-name"), "refs/heads/fix\n")
	makeFile(t, filepath.Join(rbDir, "onto"), "deadbeef\n")
	makeFile(t, filepath.Join(rbDir, "abort-safety"), "safehash\n")
	makeFile(t, filepath.Join(rbDir, "next"), "1\n")
	makeFile(t, filepath.Join(rbDir, "last"), "3\n")

	s, err := DetectFromGitDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Kind != StateRebaseApply {
		t.Errorf("want StateRebaseApply, got %s", s.Kind)
	}
	if s.OrigHead != "safehash" {
		t.Errorf("OrigHead: want %q (abort-safety fallback), got %q", "safehash", s.OrigHead)
	}
}

// TestDetect_Smoke: integration test using a real `git init` repo.
func TestDetect_Smoke(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	dir := t.TempDir()

	// Isolated git init
	initCmd := exec.Command("git", "init", dir)
	initCmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"LC_ALL=C",
	)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Manually plant a rebase-merge state
	rbDir := filepath.Join(dir, ".git", "rebase-merge")
	makeDir(t, rbDir)
	makeFile(t, filepath.Join(rbDir, "head-name"), "refs/heads/feature\n")
	makeFile(t, filepath.Join(rbDir, "onto"), "cafebabe\n")
	makeFile(t, filepath.Join(rbDir, "orig-head"), "deadc0de\n")
	makeFile(t, filepath.Join(rbDir, "msgnum"), "1\n")
	makeFile(t, filepath.Join(rbDir, "end"), "4\n")

	ctx := context.Background()
	s, err := Detect(ctx, dir)
	if err != nil {
		t.Fatalf("Detect error: %v", err)
	}
	if s.Kind != StateRebaseMerge {
		t.Errorf("want StateRebaseMerge, got %s", s.Kind)
	}
	if s.HeadName != "refs/heads/feature" {
		t.Errorf("HeadName: want %q, got %q", "refs/heads/feature", s.HeadName)
	}
	if s.Current != 1 || s.Total != 4 {
		t.Errorf("Current/Total: want 1/4, got %d/%d", s.Current, s.Total)
	}
	if s.CommonDir == "" {
		t.Error("CommonDir should not be empty")
	}
}

// TestDetect_NotAGitRepo: Detect returns error when workDir is not a git repo
func TestDetect_NotAGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	dir := t.TempDir()
	ctx := context.Background()
	_, err := Detect(ctx, dir)
	if err == nil {
		t.Fatal("expected error for non-git dir, got nil")
	}
}

// TestDetect_EmptyWorkDir: Detect with empty workDir uses os.Getwd (smoke)
func TestDetect_EmptyWorkDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	// We just verify it doesn't panic; it may succeed or fail depending on cwd.
	ctx := context.Background()
	_, _ = Detect(ctx, "")
}

// TestStateKind_String: verify all String() values
func TestStateKind_String(t *testing.T) {
	cases := []struct {
		k    StateKind
		want string
	}{
		{StateNone, "none"},
		{StateRebaseMerge, "rebase-merge"},
		{StateRebaseApply, "rebase-apply"},
		{StateMerge, "merge"},
		{StateCherryPick, "cherry-pick"},
		{StateRevert, "revert"},
		{StateBisect, "bisect"},
		{StateKind(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("StateKind(%d).String() = %q, want %q", c.k, got, c.want)
		}
	}
}
