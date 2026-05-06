package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestStashIfChangedReturnsFalseWhenNothingToStash locks in the
// regression that motivated the helper: `git stash push` exits 0 with
// "No local changes to save" on a clean tree, and our caller must not
// treat that as a successful stash and pop later.
func TestStashIfChangedReturnsFalseWhenNothingToStash(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}

	created, err := stashIfChanged(context.Background(), runner, "push", "-m", "test")
	if err != nil {
		t.Fatalf("stashIfChanged on clean tree: %v", err)
	}
	if created {
		t.Errorf("clean tree returned created=true; want false")
	}

	// refs/stash must remain absent — nothing to pop.
	if tip := stashTip(context.Background(), runner); tip != "" {
		t.Errorf("stashTip after no-op push = %q, want empty", tip)
	}
}

func TestStashIfChangedReturnsTrueOnRealDiff(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("a.txt", "v1\n")
	r.RunGit("add", "a.txt")
	r.RunGit("commit", "-m", "add a")

	// Modify so working tree is dirty in a way `git stash push` accepts.
	r.WriteFile("a.txt", "v2\n")

	runner := &git.ExecRunner{Dir: r.Dir}
	created, err := stashIfChanged(context.Background(), runner, "push", "-m", "real")
	if err != nil {
		t.Fatalf("stashIfChanged: %v", err)
	}
	if !created {
		t.Fatalf("real diff returned created=false; want true")
	}
	if tip := stashTip(context.Background(), runner); tip == "" {
		t.Errorf("refs/stash absent after successful push")
	}
}

func TestStashIfChangedSurfacesPushError(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}

	// `git stash push --not-a-flag` is a guaranteed error path that
	// exercises the wrapper's stderr-capture branch.
	_, err := stashIfChanged(context.Background(), runner, "push", "--definitely-not-a-flag")
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if !strings.Contains(err.Error(), "stash push") {
		t.Errorf("error = %q, want stash push wrapping", err)
	}
}

func TestDescribeDirtyButNotStashedDetectsModeOnly(t *testing.T) {
	// Skip on Windows-y filesystems where chmod is meaningless. testutil
	// repos live in t.TempDir() which is unix on darwin/linux runners.
	r := testutil.NewRepo(t)
	r.WriteFile("script.sh", "echo hi\n")
	r.RunGit("add", "script.sh")
	r.RunGit("commit", "-m", "add script")

	// Flip the executable bit. core.filemode is enabled by testutil, so
	// `git diff --raw` reports an old-mode/new-mode difference with the
	// same blob hashes — the canonical "stash silently skips this" case.
	if err := chmodPlusX(r.Dir + "/script.sh"); err != nil {
		t.Skipf("chmod unsupported on this filesystem: %v", err)
	}

	runner := &git.ExecRunner{Dir: r.Dir}
	hint := describeDirtyButNotStashed(context.Background(), runner)
	if !strings.Contains(hint, "mode") {
		t.Errorf("describeDirtyButNotStashed = %q, want mode-bit hint", hint)
	}
}

// chmodPlusX is split out so callers can skip on filesystems where mode
// bits are meaningless without dragging the test logic into a guard.
func chmodPlusX(path string) error {
	return chmodOrSkip(path)
}
