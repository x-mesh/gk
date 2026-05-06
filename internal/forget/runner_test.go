package forget

import (
	"context"
	"errors"
	"os/exec"
	"testing"

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
