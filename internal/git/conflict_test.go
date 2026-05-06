package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRebaseConflictStatus_NoRebaseInProgress(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"rev-parse --git-dir": {Stdout: gitDir + "\n"},
		},
	}
	c := NewClient(fake)

	got, err := c.RebaseConflictStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no rebase, got %+v", got)
	}
}

func TestRebaseConflictStatus_RebaseMergeMetadata(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, ".git")
	rebaseDir := filepath.Join(gitDir, "rebase-merge")
	if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(rebaseDir, "stopped-sha"), "abc1234\n")
	mustWrite(t, filepath.Join(rebaseDir, "msgnum"), "2\n")
	mustWrite(t, filepath.Join(rebaseDir, "end"), "5\n")

	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"rev-parse --git-dir": {Stdout: gitDir + "\n"},
			// Subject lookup for stopped commit.
			"log -1 --format=%s abc1234": {Stdout: "fix: thing\n"},
			"diff --name-only --diff-filter=U": {
				Stdout: "internal/foo.go\ninternal/bar.go\n",
			},
			"diff --name-only --cached --diff-filter=ACMRT": {
				Stdout: "internal/baz.go\n",
			},
		},
	}
	c := NewClient(fake)

	got, err := c.RebaseConflictStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil info")
		return
	}
	if got.StoppedSHA != "abc1234" {
		t.Errorf("StoppedSHA = %q", got.StoppedSHA)
	}
	if got.StoppedSubj != "fix: thing" {
		t.Errorf("StoppedSubj = %q", got.StoppedSubj)
	}
	if got.Done != 2 {
		t.Errorf("Done = %d, want 2", got.Done)
	}
	if got.Total != 5 {
		t.Errorf("Total = %d, want 5", got.Total)
	}
	if got.Remaining() != 3 {
		t.Errorf("Remaining = %d, want 3", got.Remaining())
	}
	wantUnmerged := []string{"internal/foo.go", "internal/bar.go"}
	if !equal(got.Unmerged, wantUnmerged) {
		t.Errorf("Unmerged = %v, want %v", got.Unmerged, wantUnmerged)
	}
	wantStaged := []string{"internal/baz.go"}
	if !equal(got.Staged, wantStaged) {
		t.Errorf("Staged = %v, want %v", got.Staged, wantStaged)
	}
}

func TestRebaseConflictStatus_RebaseApplyLegacy(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, ".git")
	applyDir := filepath.Join(gitDir, "rebase-apply")
	if err := os.MkdirAll(applyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// rebase-apply uses `next`/`last` instead of `msgnum`/`end`.
	mustWrite(t, filepath.Join(applyDir, "next"), "1\n")
	mustWrite(t, filepath.Join(applyDir, "last"), "3\n")

	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"rev-parse --git-dir": {Stdout: gitDir + "\n"},
		},
	}
	c := NewClient(fake)
	got, err := c.RebaseConflictStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil info")
		return
	}
	if got.Done != 1 || got.Total != 3 {
		t.Errorf("Done/Total = %d/%d, want 1/3", got.Done, got.Total)
	}
}

func TestLatestBackupRef(t *testing.T) {
	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"for-each-ref --sort=-refname --count=1 --format=%(refname) refs/gk/backup/main/*": {
				Stdout: "refs/gk/backup/main/1730000999\n",
			},
		},
	}
	c := NewClient(fake)
	got := c.LatestBackupRef(context.Background(), "main")
	want := "refs/gk/backup/main/1730000999"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLatestBackupRef_NoBackups(t *testing.T) {
	fake := &FakeRunner{
		Responses: map[string]FakeResponse{
			"for-each-ref --sort=-refname --count=1 --format=%(refname) refs/gk/backup/main/*": {
				Stdout: "",
			},
		},
	}
	c := NewClient(fake)
	if got := c.LatestBackupRef(context.Background(), "main"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestLatestBackupRef_RejectsEmptyBranch(t *testing.T) {
	c := NewClient(&FakeRunner{})
	if got := c.LatestBackupRef(context.Background(), ""); got != "" {
		t.Errorf("got %q, want empty for empty branch", got)
	}
}

// ---------------------------------------------------------------------------

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
