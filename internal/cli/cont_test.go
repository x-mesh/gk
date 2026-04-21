package cli

import (
	"context"
	"os/exec"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/testutil"
)

// setupRebaseConflict creates a repo with a rebase conflict and returns the repo.
// Branch structure:
//
//	main:  initial -> main-change (modifies file.txt)
//	feat:  initial -> feat-change (modifies file.txt differently)
//
// feat is rebased onto main, producing a conflict.
func setupRebaseConflict(t *testing.T) *testutil.Repo {
	t.Helper()
	r := testutil.NewRepo(t)

	// Write file.txt on main
	r.WriteFile("file.txt", "base\n")
	r.Commit("add file.txt")

	// Create feat branch and modify file.txt
	r.CreateBranch("feat")
	r.WriteFile("file.txt", "feat change\n")
	r.Commit("feat: change file")

	// Switch to main and make a conflicting change
	r.Checkout("main")
	r.WriteFile("file.txt", "main change\n")
	r.Commit("main: change file")

	// Checkout feat and start rebase onto main — this should conflict
	r.Checkout("feat")
	_, err := r.TryGit("rebase", "main")
	if err == nil {
		t.Skip("expected rebase conflict but none occurred")
	}

	return r
}

func TestRunContinue_NoStateInProgress(t *testing.T) {
	r := testutil.NewRepo(t)

	cmd := &cobra.Command{Use: "continue"}
	cmd.Flags().Bool("yes", false, "")
	cmd.SetContext(context.Background())

	// Override flagRepo for this test.
	flagRepo = r.Dir

	err := runContinue(cmd, nil)
	if err == nil {
		t.Fatal("expected error for no in-progress state")
	}
}

func TestRunContinue_RebaseConflictResolvedAndContinue(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	r := setupRebaseConflict(t)

	// Verify state is RebaseMerge
	ctx := context.Background()
	state, err := gitstate.Detect(ctx, r.Dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Kind != gitstate.StateRebaseMerge {
		t.Fatalf("expected StateRebaseMerge, got %s", state.Kind)
	}

	// Resolve conflict: accept ours and stage
	r.WriteFile("file.txt", "feat change\n")
	r.RunGit("add", "file.txt")

	// Set GIT_EDITOR to avoid interactive editor during --continue
	t.Setenv("GIT_EDITOR", "true")

	// Set flagRepo and call runContinue
	flagRepo = r.Dir
	cmd := &cobra.Command{Use: "continue"}
	cmd.Flags().Bool("yes", true, "")
	cmd.SetContext(ctx)
	if err := cmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	if err := runContinue(cmd, nil); err != nil {
		t.Fatalf("runContinue: %v", err)
	}

	// After continue, state should be None
	state, err = gitstate.Detect(ctx, r.Dir)
	if err != nil {
		t.Fatalf("Detect after continue: %v", err)
	}
	if state.Kind != gitstate.StateNone {
		t.Errorf("expected StateNone after continue, got %s", state.Kind)
	}
}

func TestRunAbort_RebaseConflictAborted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	r := setupRebaseConflict(t)

	ctx := context.Background()

	// Verify state
	state, err := gitstate.Detect(ctx, r.Dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Kind != gitstate.StateRebaseMerge {
		t.Fatalf("expected StateRebaseMerge, got %s", state.Kind)
	}

	flagRepo = r.Dir
	cmd := &cobra.Command{Use: "abort"}
	cmd.Flags().Bool("yes", false, "")
	cmd.SetContext(ctx)

	if err := runAbort(cmd, nil); err != nil {
		t.Fatalf("runAbort: %v", err)
	}

	// After abort, state should be None
	state, err = gitstate.Detect(ctx, r.Dir)
	if err != nil {
		t.Fatalf("Detect after abort: %v", err)
	}
	if state.Kind != gitstate.StateNone {
		t.Errorf("expected StateNone after abort, got %s", state.Kind)
	}
}
