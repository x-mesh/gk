package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// landCmd builds the land cobra command scoped to dir, with child
// executions captured by the returned recorder instead of spawning real
// gk processes.
type landRecorder struct {
	calls   [][]string
	failOn  string // step arg[0] that should fail ("pull", "push", ...)
	failErr error
}

func setupLandTest(t *testing.T, dir string) (*cobra.Command, *landRecorder, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	rec := &landRecorder{}
	prevChild := landRunChild
	landRunChild = func(ctx context.Context, gkPath, repo string, jsonMode bool, args ...string) error {
		rec.calls = append(rec.calls, args)
		if len(args) > 0 && args[0] == rec.failOn {
			if rec.failErr != nil {
				return rec.failErr
			}
			return context.DeadlineExceeded // any error
		}
		return nil
	}
	t.Cleanup(func() { landRunChild = prevChild })

	cmd := &cobra.Command{Use: "land", RunE: runLand, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().Bool("with-base", true, "")
	cmd.Flags().Bool("cleanup", false, "")
	cmd.Flags().String("promote", "", "")
	cmd.Flags().Lookup("promote").NoOptDefVal = landPromoteUseBase
	cmd.SetContext(context.Background())
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd, rec, stdout, stderr
}

func landCallNames(rec *landRecorder) string {
	names := make([]string, 0, len(rec.calls))
	for _, c := range rec.calls {
		names = append(names, strings.Join(c, " "))
	}
	return strings.Join(names, " | ")
}

func TestLand_DirtyTreeRunsAllSteps(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("wip.txt", "x\n") // dirty

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land: %v", err)
	}
	want := "commit -f | pull --with-base=true | push"
	if got := landCallNames(rec); got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

func TestLand_CleanTreeSkipsCommit(t *testing.T) {
	repo := testutil.NewRepo(t)
	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land: %v", err)
	}
	if got := landCallNames(rec); got != "pull --with-base=true | push" {
		t.Errorf("steps = %q", got)
	}
}

func TestLand_WithBaseOptOut(t *testing.T) {
	repo := testutil.NewRepo(t)
	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	if err := cmd.Flags().Set("with-base", "false"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land: %v", err)
	}
	// Both polarities must reach the child — config pull.with_base could
	// otherwise re-enable what the flag promised to skip (Codex P2).
	if got := landCallNames(rec); got != "pull --with-base=false | push" {
		t.Errorf("steps = %q", got)
	}
}

func TestLand_FailureStopsAndNamesStep(t *testing.T) {
	repo := testutil.NewRepo(t)
	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	rec.failOn = "pull"

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `step "pull" failed`) {
		t.Fatalf("want pull step failure, got %v", err)
	}
	if !strings.Contains(HintFrom(err), "gk resolve --ai") {
		t.Errorf("resume hint: %q", HintFrom(err))
	}
	if got := landCallNames(rec); strings.Contains(got, "push") {
		t.Errorf("push must not run after pull failure: %q", got)
	}
	r := RemediesFrom(err)
	if len(r) == 0 || r[0].Command != "gk land" {
		t.Errorf("remedies: %+v", r)
	}
}

func TestLand_JSONResultContract(t *testing.T) {
	repo := testutil.NewRepo(t)
	prevJ := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJ })

	cmd, rec, stdout, _ := setupLandTest(t, repo.Dir)
	rec.failOn = "push"

	_ = cmd.Execute() // fails at push; result JSON still lands on stdout

	var res landResultJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", err, stdout.String())
	}
	if res.Result != "failed" || res.FailedStep != "push" || res.Resume == "" {
		t.Errorf("result: %+v", res)
	}
	if len(res.Steps) != 3 || res.Steps[0].Result != "skipped" || res.Steps[1].Result != "ok" || res.Steps[2].Result != "failed" {
		t.Errorf("steps: %+v", res.Steps)
	}
}

func TestLand_DryRunExecutesNothing(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("wip.txt", "x\n")
	prevD := flagDryRun
	flagDryRun = true
	t.Cleanup(func() { flagDryRun = prevD })

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --dry-run: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("dry-run must not exec children: %v", rec.calls)
	}
}

func TestLand_PromoteForwardMergesAndPushesBase(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feature")
	repo.WriteFile("wip.txt", "x\n") // dirty

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	if err := cmd.Flags().Set("promote", "main"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --promote=main: %v", err)
	}
	want := "commit -f | pull --with-base=true | push | merge --into main --no-ai | push --from main"
	if got := landCallNames(rec); got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

func TestLand_PromoteDefaultsToBaseBranch(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feature")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main") // no remote in the fixture — pin the base
	// A bare --promote resolves to the configured base branch.
	if err := cmd.Flags().Set("promote", landPromoteUseBase); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --promote: %v", err)
	}
	if got := landCallNames(rec); !strings.Contains(got, "merge --into main --no-ai | push --from main") {
		t.Errorf("bare --promote must target base main: %q", got)
	}
}

func TestLand_PromoteSkippedOnBaseBranch(t *testing.T) {
	repo := testutil.NewRepo(t) // already on the base branch (main)

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	if err := cmd.Flags().Set("promote", "main"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --promote=main on main: %v", err)
	}
	if got := landCallNames(rec); strings.Contains(got, "merge") {
		t.Errorf("promote must skip when land already runs on the base: %q", got)
	}
}

// TestLand_CleanupReclaimsMergedBranch: --cleanup deletes fully-merged
// branches and removes the worktrees holding them — the safe subset of
// branch clean (merged-only, no AI, protected excluded).
func TestLand_CleanupReclaimsMergedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	// A branch fully merged into main (ff merge → tips equal).
	repo.RunGit("checkout", "-b", "done-feature")
	repo.WriteFile("f.txt", "x\n")
	repo.Commit("feat: done work")
	repo.Checkout("main")
	repo.RunGit("merge", "--ff-only", "done-feature")
	// Hold it in a worktree so cleanup must reclaim that too.
	wt := t.TempDir() + "/done-wt"
	repo.RunGit("worktree", "add", wt, "done-feature")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main") // no remote in the fixture — pin the base
	if err := cmd.Flags().Set("cleanup", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --cleanup: %v", err)
	}
	_ = rec

	branches := repo.RunGit("branch", "--list", "done-feature")
	if strings.TrimSpace(branches) != "" {
		t.Errorf("merged branch must be reclaimed, still have: %q", branches)
	}
	worktrees := repo.RunGit("worktree", "list")
	if strings.Contains(worktrees, "done-wt") {
		t.Errorf("worktree must be reclaimed:\n%s", worktrees)
	}
}
