package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// ---------------------------------------------------------------------------
// Unit tests — conflictFiles
// ---------------------------------------------------------------------------

func TestConflictFiles_Empty(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	files, err := conflictFiles(context.Background(), runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 conflict files, got %d: %v", len(files), files)
	}
}

func TestConflictFiles_WithConflict(t *testing.T) {
	r := testutil.NewRepo(t)

	// Create conflict: main and feat both modify foo.txt differently.
	r.WriteFile("foo.txt", "original\n")
	r.Commit("init foo")

	r.CreateBranch("feat")
	r.WriteFile("foo.txt", "feat change\n")
	r.Commit("feat change")

	r.Checkout("main")
	r.WriteFile("foo.txt", "main change\n")
	r.Commit("main change")

	// Merge produces a conflict; we expect a non-zero exit which is fine.
	_, _ = r.TryGit("merge", "feat")

	runner := &git.ExecRunner{Dir: r.Dir}
	files, err := conflictFiles(context.Background(), runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 conflict file, got %d: %v", len(files), files)
	}
	if files[0] != "foo.txt" {
		t.Errorf("expected conflict file 'foo.txt', got %q", files[0])
	}
}

// ---------------------------------------------------------------------------
// Unit tests — firstMarkerLine
// ---------------------------------------------------------------------------

func TestFirstMarkerLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conflict.txt")
	content := "line1\nline2\n<<<<<<< HEAD\nline4\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := firstMarkerLine(path)
	if got != 3 {
		t.Errorf("expected line 3, got %d", got)
	}
}

func TestFirstMarkerLine_NoMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.txt")
	if err := os.WriteFile(path, []byte("no conflict here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := firstMarkerLine(path)
	if got != 1 {
		t.Errorf("expected 1 for no-marker file, got %d", got)
	}
}

func TestFirstMarkerLine_MissingFile(t *testing.T) {
	got := firstMarkerLine("/nonexistent/path/file.txt")
	if got != 1 {
		t.Errorf("expected 1 for missing file, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Unit tests — appendEditorTarget
// ---------------------------------------------------------------------------

func TestAppendEditorTarget_Vim(t *testing.T) {
	argv := appendEditorTarget([]string{"vim"}, "vim", "/tmp/foo", 5)
	want := []string{"vim", "+5", "/tmp/foo"}
	if !equalStrSlice(argv, want) {
		t.Errorf("got %v, want %v", argv, want)
	}
}

func TestAppendEditorTarget_VSCode(t *testing.T) {
	argv := appendEditorTarget([]string{"code"}, "code", "/tmp/foo", 5)
	want := []string{"code", "--goto", "/tmp/foo:5"}
	if !equalStrSlice(argv, want) {
		t.Errorf("got %v, want %v", argv, want)
	}
}

func TestAppendEditorTarget_Unknown(t *testing.T) {
	argv := appendEditorTarget([]string{"myeditor"}, "myeditor", "/tmp/foo", 5)
	want := []string{"myeditor", "/tmp/foo"}
	if !equalStrSlice(argv, want) {
		t.Errorf("got %v, want %v", argv, want)
	}
}

// ---------------------------------------------------------------------------
// Integration tests — runEditConflict via cobra command
// ---------------------------------------------------------------------------

// buildEditConflictCmd builds a fresh cobra.Command that mirrors init() but
// allows the test to set flags programmatically and capture stdout.
func buildEditConflictCmd(repoDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "edit-conflict",
		RunE: runEditConflict,
	}
	cmd.Flags().String("editor", "", "")
	cmd.Flags().Bool("list", false, "")
	// Patch the global flagRepo so RepoFlag() returns repoDir.
	flagRepo = repoDir
	cmd.SetContext(context.Background())
	return cmd
}

func TestRunEditConflict_NoConflicts(t *testing.T) {
	r := testutil.NewRepo(t)

	cmd := buildEditConflictCmd(r.Dir)
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "no conflicts") {
		t.Errorf("expected 'no conflicts' output, got: %q", buf.String())
	}
}

func TestRunEditConflict_List(t *testing.T) {
	r := testutil.NewRepo(t)

	// Create a merge conflict.
	r.WriteFile("foo.txt", "original\n")
	r.Commit("init foo")
	r.CreateBranch("feat")
	r.WriteFile("foo.txt", "feat change\n")
	r.Commit("feat change")
	r.Checkout("main")
	r.WriteFile("foo.txt", "main change\n")
	r.Commit("main change")
	_, _ = r.TryGit("merge", "feat")

	cmd := buildEditConflictCmd(r.Dir)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Flags().Set("list", "true"); err != nil {
		t.Fatal(err)
	}

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "foo.txt") {
		t.Errorf("expected 'foo.txt' in output, got: %q", output)
	}
}

func TestRunEditConflict_EditorSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping editor smoke test in short mode")
	}

	// Find /bin/true or equivalent no-op binary.
	trueBin, err := findTrueBin()
	if err != nil {
		t.Skipf("no no-op binary found: %v", err)
	}

	r := testutil.NewRepo(t)

	// Create a merge conflict so there's something to open.
	r.WriteFile("foo.txt", "original\n")
	r.Commit("init foo")
	r.CreateBranch("feat")
	r.WriteFile("foo.txt", "feat change\n")
	r.Commit("feat change")
	r.Checkout("main")
	r.WriteFile("foo.txt", "main change\n")
	r.Commit("main change")
	_, _ = r.TryGit("merge", "feat")

	cmd := buildEditConflictCmd(r.Dir)
	cmd.SetOut(os.Stdout)
	if err := cmd.Flags().Set("editor", trueBin); err != nil {
		t.Fatal(err)
	}

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("editor smoke test failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func equalStrSlice(a, b []string) bool {
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

// findTrueBin returns path to a no-op binary (/bin/true, /usr/bin/true).
func findTrueBin() (string, error) {
	for _, p := range []string{"/bin/true", "/usr/bin/true"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}
