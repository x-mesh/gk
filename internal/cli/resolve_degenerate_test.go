package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/testutil"
)

// setupDeleteModifyRebase builds a rebase paused on a delete/modify
// conflict: main deleted f.txt, the feat pick modified it. During the
// rebase ours(=main) has no stage 2 for the path and the file is
// missing from the working tree — the case the marker parser cannot
// touch.
func setupDeleteModifyRebase(t *testing.T) *testutil.Repo {
	t.Helper()
	r := testutil.NewRepo(t)

	r.WriteFile("f.txt", "base\n")
	r.WriteFile("other.txt", "keep\n")
	r.Commit("base")

	r.CreateBranch("feat")
	r.WriteFile("f.txt", "feat change\n")
	r.Commit("feat: modify f")

	r.Checkout("main")
	r.RunGit("rm", "-q", "f.txt")
	r.Commit("main: delete f")

	r.Checkout("feat")
	if _, err := r.TryGit("rebase", "main"); err == nil {
		t.Skip("expected delete/modify conflict but rebase succeeded")
	}
	return r
}

func TestResolveDegenerate_TheirsKeepsModifiedFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	r := setupDeleteModifyRebase(t)
	t.Setenv("GIT_EDITOR", "false")

	var out bytes.Buffer
	if err := runResolveCmd(t, r.Dir, &out, map[string]string{"strategy": "theirs"}); err != nil {
		t.Fatalf("resolve --strategy theirs: %v\noutput:\n%s", err, out.String())
	}

	state, err := gitstate.Detect(context.Background(), r.Dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Kind != gitstate.StateNone {
		t.Fatalf("expected rebase finished, still in %s\noutput:\n%s", state.Kind, out.String())
	}
	data, err := os.ReadFile(filepath.Join(r.Dir, "f.txt"))
	if err != nil {
		t.Fatalf("f.txt should survive (theirs modified it): %v", err)
	}
	if string(data) != "feat change\n" {
		t.Errorf("f.txt = %q, want the pick's modification", data)
	}
}

func TestResolveDegenerate_OursDeletesAndSkipsEmptyPick(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	r := setupDeleteModifyRebase(t)
	t.Setenv("GIT_EDITOR", "false")

	var out bytes.Buffer
	if err := runResolveCmd(t, r.Dir, &out, map[string]string{"strategy": "ours"}); err != nil {
		t.Fatalf("resolve --strategy ours: %v\noutput:\n%s", err, out.String())
	}

	state, err := gitstate.Detect(context.Background(), r.Dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Kind != gitstate.StateNone {
		t.Fatalf("expected rebase finished, still in %s\noutput:\n%s", state.Kind, out.String())
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "f.txt")); !os.IsNotExist(err) {
		t.Errorf("f.txt should be gone (ours deleted it), stat err = %v", err)
	}
	// Taking the deletion empties the pick — it must be skipped, and the
	// emptied commit must not survive in history.
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("expected empty-pick skip narration, got:\n%s", out.String())
	}
	if log := r.RunGit("log", "--oneline"); strings.Contains(log, "feat: modify f") {
		t.Errorf("emptied pick should have been dropped:\n%s", log)
	}
}

func TestResolveDegenerate_NoManualHintsInStrategyMode(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	r := setupDeleteModifyRebase(t)
	t.Setenv("GIT_EDITOR", "false")

	cmd, _, _ := rootCmd.Find([]string{"resolve"})
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	t.Cleanup(func() { cmd.SetErr(nil) })

	var out bytes.Buffer
	if err := runResolveCmd(t, r.Dir, &out, map[string]string{"strategy": "theirs"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The old flow printed "hint: to drop the file, run: git rm ..." and
	// then left the user stranded; with auto-handling those hints are gone.
	if strings.Contains(errBuf.String(), "hint: to drop the file") {
		t.Errorf("manual-resolution hints should be suppressed when gk handles the path itself:\n%s", errBuf.String())
	}
}
