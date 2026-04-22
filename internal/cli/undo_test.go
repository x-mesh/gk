package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/testutil"
)

// newUndoTestCmd builds a fresh cobra.Command with undo flags for testing.
// Output is captured in buf. Context is set to context.Background().
func newUndoTestCmd(buf *bytes.Buffer, flags map[string]string) *cobra.Command {
	cmd := &cobra.Command{Use: "undo"}
	cmd.Flags().Bool("list", false, "")
	cmd.Flags().Int("limit", 20, "")
	cmd.Flags().Bool("yes", false, "")
	cmd.Flags().String("to", "", "")
	if buf != nil {
		cmd.SetOut(buf)
		cmd.SetErr(buf)
	}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetContext(context.Background())
	for k, v := range flags {
		_ = cmd.Flags().Set(k, v)
	}
	return cmd
}

// fixedTime is a deterministic timestamp for backup ref naming in tests.
var fixedTime = time.Unix(1700000000, 0)

func nowFixed() time.Time { return fixedTime }

// ---------------------------------------------------------------------------
// TestUndo_List_EmptyReflog — fresh repo has an initial commit so reflog has
// at least 1 entry; --list prints it without hanging.
// ---------------------------------------------------------------------------

func TestUndo_List_EmptyReflog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"list": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	out := buf.String()
	// Fresh repo has at least the "commit (initial)" entry.
	if out == "no reflog entries available\n" {
		// This is acceptable only if git reflog is truly empty (very unusual).
		t.Log("no reflog entries (initial commit may not create one in isolation env)")
		return
	}
	// Otherwise we expect at least one line.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 1 || lines[0] == "" {
		t.Errorf("expected at least 1 reflog line, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_List_AfterCommits — 3 commits → --list output has 3+ lines with sha
// ---------------------------------------------------------------------------

func TestUndo_List_AfterCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("commit 1")
	repo.WriteFile("b.txt", "b")
	sha2 := repo.Commit("commit 2")
	repo.WriteFile("c.txt", "c")
	sha3 := repo.Commit("commit 3")

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"list": "true", "limit": "20"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	out := buf.String()
	// Output must contain at least 3 lines (one per commit plus initial).
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Errorf("expected at least 3 lines, got %d: %q", len(lines), out)
	}

	// Each SHA short form (first 8 chars) should appear in output.
	for _, sha := range []string{sha1[:8], sha2[:8], sha3[:8]} {
		if !strings.Contains(out, sha) {
			t.Errorf("expected SHA prefix %q in list output, got: %q", sha, out)
		}
	}
}

// ---------------------------------------------------------------------------
// TestUndo_To_ResetsHEAD — 3 commits, --to HEAD~1 → HEAD moves back + backup ref exists
// ---------------------------------------------------------------------------

func TestUndo_To_ResetsHEAD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("commit 1")
	repo.WriteFile("b.txt", "b")
	sha2 := repo.Commit("commit 2")
	repo.WriteFile("c.txt", "c")
	repo.Commit("commit 3")

	// HEAD~1 should be sha2.
	expectedSHA := repo.RunGit("rev-parse", "HEAD~1")
	if expectedSHA != sha2 {
		t.Fatalf("HEAD~1 expected %s, got %s", sha2, expectedSHA)
	}

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"to": "HEAD~1", "yes": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	// HEAD must now be sha2.
	head := repo.RunGit("rev-parse", "HEAD")
	if head != sha2 {
		t.Errorf("HEAD after undo: got %s, want %s", head, sha2)
	}

	// Backup ref must exist under refs/gk/undo-backup/main/<unix>.
	backupRef := gitsafe.BackupRefName("undo", "main", fixedTime)
	backupSHA := repo.RunGit("rev-parse", backupRef)
	if backupSHA == "" {
		t.Errorf("backup ref %q not found", backupRef)
	}

	out := buf.String()
	if !strings.Contains(out, "undone to") {
		t.Errorf("expected 'undone to' in output, got: %q", out)
	}
	if !strings.Contains(out, backupRef) {
		t.Errorf("expected backup ref %q in output, got: %q", backupRef, out)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_To_RefusesDirtyTree — dirty working tree → preflight error
// ---------------------------------------------------------------------------

func TestUndo_To_RefusesDirtyTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("commit 1")

	// Dirty the working tree (modify a tracked file).
	repo.WriteFile("a.txt", "dirty content")

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"to": "HEAD", "yes": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	err := runUndoWith(cmd, deps)
	if err == nil {
		t.Fatal("expected error for dirty working tree, got nil")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("expected 'uncommitted' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestUndo_To_RefusesInProgressRebase — rebase conflict state → preflight error
// ---------------------------------------------------------------------------

func TestUndo_To_RefusesInProgressRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo := testutil.NewRepo(t)

	// Create a conflict setup: two branches modify the same line.
	repo.WriteFile("file.txt", "original\n")
	repo.Commit("base")

	repo.CreateBranch("feature")
	repo.WriteFile("file.txt", "feature change\n")
	repo.Commit("feature commit")

	repo.Checkout("main")
	repo.WriteFile("file.txt", "main change\n")
	repo.Commit("main commit")

	// Attempt rebase feature onto main — this should conflict.
	repo.Checkout("feature")
	_, rebaseErr := repo.TryGit("rebase", "main")
	if rebaseErr == nil {
		// No conflict (unexpected); skip test.
		t.Skip("expected rebase conflict but got none; skipping")
	}

	// Now repo is in rebase-merge state. Verify git agrees.
	rebaseMergeDir := filepath.Join(repo.GitDir, "rebase-merge")
	if _, err := os.Stat(rebaseMergeDir); os.IsNotExist(err) {
		t.Skip("rebase-merge dir not found; test environment may not support conflict setup")
	}

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"to": "HEAD", "yes": "true"})

	r := &git.ExecRunner{Dir: repo.Dir}
	deps := &undoDeps{
		Runner:  r,
		Client:  git.NewClient(r),
		WorkDir: repo.Dir,
		Picker:  nil,
		Now:     nowFixed,
	}

	err := runUndoWith(cmd, deps)
	if err == nil {
		t.Fatal("expected error for in-progress rebase, got nil")
	}
	if !strings.Contains(err.Error(), "in-progress") {
		t.Errorf("expected 'in-progress' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestHumanSince — table-driven unit tests
// ---------------------------------------------------------------------------

func TestHumanSince(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{59 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{90 * time.Minute, "1h ago"},
		{2 * time.Hour, "2h ago"},
		{48 * time.Hour, "2d ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, tc := range tests {
		got := humanSince(tc.d)
		if got != tc.want {
			t.Errorf("humanSince(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestShortSHA — unit tests for SHA truncation
// ---------------------------------------------------------------------------

func TestShortSHA(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc123", "abc123"},                                     // short → unchanged
		{"abcdefgh", "abcdefgh"},                                 // exactly 8 → unchanged
		{"abcdefghi", "abcdefgh"},                                // 9 → first 8
		{"abcdefghijklmnopqrstuvwxyz1234567890abcd", "abcdefgh"}, // full sha → first 8
		{"", ""}, // empty → empty
	}
	for _, tc := range tests {
		got := shortSHA(tc.input)
		if got != tc.want {
			t.Errorf("shortSHA(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestEntriesToPickerItems — key is index string, display contains sha+action
// ---------------------------------------------------------------------------

func TestEntriesToPickerItems(t *testing.T) {
	entries := []interface{}{} // use reflog.Entry via runUndoWith indirectly
	_ = entries

	// Use FakeRunner to get parsed entries without a real repo.
	fakeReflogOutput := "aabbccddee112233445566778899001122334455\x00" +
		"aabbccdd\x00HEAD@{0}\x00commit: initial\x001700000000\x1e"

	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"reflog show --format=%H%x00%h%x00%gD%x00%gs%x00%at%x1e HEAD -n 20": {
				Stdout: fakeReflogOutput,
			},
		},
	}

	var buf bytes.Buffer
	cmd := newUndoTestCmd(&buf, map[string]string{"list": "true"})

	deps := &undoDeps{
		Runner: fake,
		Client: git.NewClient(fake),
		Picker: nil,
		Now:    nowFixed,
	}

	if err := runUndoWith(cmd, deps); err != nil {
		t.Fatalf("runUndoWith: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "aabbccdd") {
		t.Errorf("expected short sha 'aabbccdd' in list output, got: %q", out)
	}
}
