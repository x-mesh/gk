package forget

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestEnsureFilterRepo(t *testing.T) {
	err := EnsureFilterRepo()
	if _, lookErr := exec.LookPath("git-filter-repo"); lookErr != nil {
		// Not installed on this host — EnsureFilterRepo must surface the
		// canonical install hint, not a generic LookPath error.
		if !errors.Is(err, ErrFilterRepoNotInstalled) {
			t.Errorf("EnsureFilterRepo without binary = %v, want ErrFilterRepoNotInstalled", err)
		}
		return
	}
	if err != nil {
		t.Errorf("EnsureFilterRepo with binary present = %v, want nil", err)
	}
}

func TestCaptureOriginNone(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	got, err := CaptureOrigin(context.Background(), runner)
	if err != nil {
		t.Fatalf("CaptureOrigin: %v", err)
	}
	if got == nil || got.Name != "" || got.URL != "" {
		t.Errorf("CaptureOrigin on origin-less repo = %+v, want empty", got)
	}
}

func TestCaptureAndRestoreOrigin(t *testing.T) {
	r := testutil.NewRepo(t)
	r.RunGit("remote", "add", "origin", "git@example.com:foo/bar.git")

	runner := &git.ExecRunner{Dir: r.Dir}
	captured, err := CaptureOrigin(context.Background(), runner)
	if err != nil {
		t.Fatalf("CaptureOrigin: %v", err)
	}
	if captured.URL != "git@example.com:foo/bar.git" {
		t.Errorf("captured URL = %q", captured.URL)
	}

	// Simulate filter-repo wiping origin.
	r.RunGit("remote", "remove", "origin")

	if err := RestoreOrigin(context.Background(), runner, captured); err != nil {
		t.Fatalf("RestoreOrigin: %v", err)
	}

	gotURL, _, err := runner.Run(context.Background(), "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("remote get-url after restore: %v", err)
	}
	if got := string(gotURL); got == "" {
		t.Errorf("origin not restored, get-url returned %q", got)
	}
}

func TestRestoreOriginIdempotent(t *testing.T) {
	r := testutil.NewRepo(t)
	r.RunGit("remote", "add", "origin", "git@example.com:foo/bar.git")

	runner := &git.ExecRunner{Dir: r.Dir}
	captured, err := CaptureOrigin(context.Background(), runner)
	if err != nil {
		t.Fatalf("CaptureOrigin: %v", err)
	}

	// Calling RestoreOrigin while origin still exists should fall through
	// to set-url, not fail loudly.
	if err := RestoreOrigin(context.Background(), runner, captured); err != nil {
		t.Errorf("RestoreOrigin (origin already present) = %v, want nil", err)
	}
}

func TestClearAlreadyRanMarker(t *testing.T) {
	gitDir := t.TempDir()
	marker := filepath.Join(gitDir, "filter-repo", "already_ran")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ClearAlreadyRanMarker(gitDir)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("marker still present after clear: %v", err)
	}
	// Missing marker and empty gitDir are both silent no-ops.
	ClearAlreadyRanMarker(gitDir)
	ClearAlreadyRanMarker("")
}

// Reproduces the field failure: a >1-day-old already_ran marker from a
// previous filter-repo run makes filter-repo block on an interactive
// "continuation? (Y/N)" prompt, which crashes with EOFError under gk's
// no-terminal invocation. RunFilterRepo must clear the marker first.
func TestRunFilterRepoClearsStaleMarker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	if _, err := exec.LookPath("git-filter-repo"); err != nil {
		t.Skip("git-filter-repo not installed")
	}
	r := testutil.NewRepo(t)
	r.WriteFile("secret/creds.txt", "oops\n")
	r.Commit("add secret")
	r.WriteFile("keep.txt", "fine\n")
	r.Commit("add keep")

	marker := filepath.Join(r.GitDir, "filter-repo", "already_ran")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(marker, stale, stale); err != nil {
		t.Fatal(err)
	}

	if err := RunFilterRepo(context.Background(), r.Dir, r.GitDir, []string{"secret/"}); err != nil {
		t.Fatalf("RunFilterRepo with stale marker: %v", err)
	}
	runner := &git.ExecRunner{Dir: r.Dir}
	out, _, err := runner.Run(context.Background(), "log", "--all", "--name-only", "--format=")
	if err != nil {
		t.Fatalf("log after rewrite: %v", err)
	}
	if strings.Contains(string(out), "secret/creds.txt") {
		t.Errorf("secret/ still in history:\n%s", out)
	}
	if !strings.Contains(string(out), "keep.txt") {
		t.Errorf("keep.txt lost from history:\n%s", out)
	}
}

// Locks in the rollback guarantee for the DELEGATED engine: filter-repo
// must not rewrite gk's backup refs nor prune the pre-rewrite objects.
// Without the --refs limiting in RunFilterRepo, both happen (observed in
// the field: backup ref re-pointed at the new history, old objects gc'd,
// manifest rollback dead).
func TestRunFilterRepoPreservesBackupRefsAndOldObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	if _, err := exec.LookPath("git-filter-repo"); err != nil {
		t.Skip("git-filter-repo not installed")
	}
	r := testutil.NewRepo(t)
	r.WriteFile("secret/creds.txt", "oops\n")
	r.Commit("add secret")
	r.WriteFile("keep.txt", "fine\n")
	r.Commit("add keep")

	oldTip := r.RunGit("rev-parse", "HEAD")
	r.RunGit("update-ref", "refs/gk/forget-backup/main/1", oldTip)

	if err := RunFilterRepo(context.Background(), r.Dir, r.GitDir, []string{"secret/"}); err != nil {
		t.Fatalf("RunFilterRepo: %v", err)
	}
	if got := r.RunGit("rev-parse", "refs/gk/forget-backup/main/1"); got != oldTip {
		t.Errorf("backup ref rewritten: %s, want %s", got, oldTip)
	}
	r.RunGit("cat-file", "-e", oldTip) // old objects must survive (no gc)
	if got := r.RunGit("rev-parse", "main"); got == oldTip {
		t.Error("main was not rewritten")
	}
	if hist := r.RunGit("log", "--name-only", "--format=", "main"); strings.Contains(hist, "secret/") {
		t.Errorf("secret/ still in main history:\n%s", hist)
	}
	// rollback round-trip: restore the manifest ref and read the old file
	r.RunGit("update-ref", "refs/heads/main", oldTip)
	if got := r.RunGit("show", "main:secret/creds.txt"); got != "oops" {
		t.Errorf("rollback content = %q", got)
	}
}
