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
	// Production land inherits the persistent --repo flag; config.Load reads
	// it to point repo-local .gk.yaml discovery at the fixture instead of
	// the cwd (this package's own repo).
	cmd.Flags().String("repo", dir, "")
	cmd.Flags().Bool("with-base", true, "")
	cmd.Flags().Bool("cleanup", false, "")
	cmd.Flags().String("to", "", "")
	cmd.Flags().Bool("no-push", false, "")
	cmd.Flags().String("promote", "", "")
	cmd.Flags().Lookup("promote").NoOptDefVal = landPromoteUseBase
	cmd.Flags().Bool("no-promote", false, "")
	cmd.Flags().Bool("autostash", false, "")
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
	want := "commit -f | pull --with-base=true | push | merge feature --into main --no-ai | push --from main"
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
	if got := landCallNames(rec); !strings.Contains(got, "merge feature --into main --no-ai | push --from main") {
		t.Errorf("bare --promote must target base main: %q", got)
	}
}

// TestLand_PromoteBareTargetsParent: in a main→develop→feat stack with
// branch.feat.gk-parent=develop, a bare --promote climbs ONE hop to the
// parent — the same target gk status names — not straight to the trunk.
func TestLand_PromoteBareTargetsParent(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main") // trunk pinned — the parent must still win
	if err := cmd.Flags().Set("promote", landPromoteUseBase); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --promote: %v", err)
	}
	got := landCallNames(rec)
	if !strings.Contains(got, "merge feat --into develop --no-ai | push --from develop") {
		t.Errorf("bare --promote must target parent develop: %q", got)
	}
	if strings.Contains(got, "--into main") {
		t.Errorf("must not skip the parent and jump to trunk: %q", got)
	}
}

// TestLand_ToParentTargetsParent: `--to parent` is the clearer spelling of a
// bare --promote — one hop to the gk-parent, not the trunk.
func TestLand_ToParentTargetsParent(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("to", "parent"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --to parent: %v", err)
	}
	got := landCallNames(rec)
	if !strings.Contains(got, "merge feat --into develop --no-ai | push --from develop") {
		t.Errorf("--to parent must target parent develop: %q", got)
	}
	if strings.Contains(got, "--into main") {
		t.Errorf("--to parent must not jump to the trunk: %q", got)
	}
}

// TestLand_ToAutostashForwardsToMerge: --autostash threads into the --to merge
// hop so a dirty receiver worktree (the parent checkout) is stashed around the
// merge instead of blocking land.
func TestLand_ToAutostashForwardsToMerge(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("to", "parent"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("autostash", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --to parent --autostash: %v", err)
	}
	if got := landCallNames(rec); !strings.Contains(got, "merge feat --into develop --no-ai --autostash") {
		t.Errorf("--autostash must reach the merge hop: %q", got)
	}
}

// TestLand_ToBaseMergesDirectlyIntoBase: `--to base` merges straight into the
// configured base in one hop, skipping the intermediate parent (that's what
// gk promote <branch> is for).
func TestLand_ToBaseMergesDirectlyIntoBase(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("to", "base"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --to base: %v", err)
	}
	got := landCallNames(rec)
	if !strings.Contains(got, "merge feat --into main --no-ai | push --from main") {
		t.Errorf("--to base must merge directly into base main: %q", got)
	}
	if strings.Contains(got, "--into develop") {
		t.Errorf("--to base must not walk the intermediate parent: %q", got)
	}
}

// TestLand_NoPushIsLocal: `--no-push` skips the branch push AND the integration
// push — commit + pull + local merge only.
func TestLand_NoPushIsLocal(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")
	repo.WriteFile("wip.txt", "x\n") // dirty

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("to", "parent"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("no-push", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --to parent --no-push: %v", err)
	}
	want := "commit -f | pull --with-base=true | merge feat --into develop --no-ai"
	if got := landCallNames(rec); got != want {
		t.Errorf("--no-push steps = %q, want %q (no push, no push --from)", got, want)
	}
}

// TestLand_PromoteBareParentMissingFallsBack: a gk-parent pointing at a
// deleted branch falls back to the trunk with a stderr warning — same
// degrade-and-warn contract as status.
func TestLand_PromoteBareParentMissingFallsBack(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "gone")

	cmd, rec, _, stderr := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("promote", landPromoteUseBase); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --promote: %v", err)
	}
	if got := landCallNames(rec); !strings.Contains(got, "merge feat --into main --no-ai") {
		t.Errorf("missing parent must fall back to trunk main: %q", got)
	}
	if !strings.Contains(stderr.String(), "parent gone not found") {
		t.Errorf("fallback must warn on stderr: %q", stderr.String())
	}
}

// TestLand_PromoteChainWalksParents: --promote=main from feat in a
// main→develop→feat stack runs one merge+push per boundary, in order, with
// the source passed explicitly (hop 2's source is develop, not the
// checked-out feat).
func TestLand_PromoteChainWalksParents(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("promote", "main"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --promote=main: %v", err)
	}
	want := "pull --with-base=true | push" +
		" | merge feat --into develop --no-ai | push --from develop" +
		" | merge develop --into main --no-ai | push --from main"
	if got := landCallNames(rec); got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

// TestLand_ToBranchChainWalks: `--to <branch>` (a named target, not parent or
// base) chain-walks the parent stack hop by hop, exactly like the deprecated
// --promote=<branch> — so --to fully replaces --promote on the named-target
// axis, not just for parent/base.
func TestLand_ToBranchChainWalks(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("to", "main"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --to main: %v", err)
	}
	want := "pull --with-base=true | push" +
		" | merge feat --into develop --no-ai | push --from develop" +
		" | merge develop --into main --no-ai | push --from main"
	if got := landCallNames(rec); got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

// TestLand_PromoteChainTargetNotInChain: a target the parent chain never
// reaches aborts before ANY step runs — land must not silently degrade to a
// direct merge that skips intermediates.
func TestLand_PromoteChainTargetNotInChain(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "staging")
	repo.RunGit("checkout", "main")
	repo.RunGit("checkout", "-b", "feat")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("promote", "staging"); err != nil {
		t.Fatal(err)
	}
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not in the parent chain") {
		t.Fatalf("want not-in-chain error, got %v", err)
	}
	if !strings.Contains(HintFrom(err), "gk merge feat --into staging") {
		t.Errorf("hint must offer the direct-merge escape: %q", HintFrom(err))
	}
	if len(rec.calls) != 0 {
		t.Errorf("no step may run after a chain resolution failure: %v", rec.calls)
	}
}

// TestLand_PromoteChainCycleErrors: a gk-parent loop (only producible via
// raw git config — set-parent validates writes) errors instead of walking
// forever.
func TestLand_PromoteChainCycleErrors(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")
	repo.RunGit("config", "branch.develop.gk-parent", "feat")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("promote", "main"); err != nil {
		t.Fatal(err)
	}
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "loops") {
		t.Fatalf("want chain-loop error, got %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("no step may run after a chain resolution failure: %v", rec.calls)
	}
}

// TestLand_PromoteChainFailedHopNamesBoundary: a failure at the first
// boundary reports failed_step as promote:<target> of THAT hop and stops —
// the second hop never runs.
func TestLand_PromoteChainFailedHopNamesBoundary(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	prevJ := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJ })

	cmd, rec, stdout, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	rec.failOn = "merge" // first merge child (feat→develop) fails
	if err := cmd.Flags().Set("promote", "main"); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Execute() // fails at the first hop

	var res landResultJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", err, stdout.String())
	}
	if res.FailedStep != "promote:develop" {
		t.Errorf("failed_step = %q, want promote:develop", res.FailedStep)
	}
	// Even when invoked via the deprecated --promote, reruns are steered to the
	// --to spelling (--promote=<branch> → --to <branch>).
	if !strings.Contains(res.Resume, "--to main") {
		t.Errorf("resume must name the full chain rerun in --to form: %q", res.Resume)
	}
	if got := landCallNames(rec); strings.Contains(got, "--into main") {
		t.Errorf("second hop must not run after the first fails: %q", got)
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

// TestLand_PromoteConfigParent: land.promote: parent in config turns the
// promote step on by default with bare-promote semantics — same target
// resolution as a bare --promote flag (gk-parent first, trunk fallback).
func TestLand_PromoteConfigParent(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile(".gk.yaml", "land:\n  promote: parent\n")
	repo.Commit("chore: config")
	repo.RunGit("checkout", "-b", "feature")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land with land.promote=parent: %v", err)
	}
	if got := landCallNames(rec); !strings.Contains(got, "merge feature --into main --no-ai | push --from main") {
		t.Errorf("config parent must behave like a bare --promote: %q", got)
	}
}

// TestLand_PromoteConfigBranchTarget: a branch name in land.promote is the
// config twin of --promote=<branch> — the chain walk applies, so the YAML
// boolean tolerances must not swallow real branch names.
func TestLand_PromoteConfigBranchTarget(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile(".gk.yaml", "land:\n  promote: main\n")
	repo.Commit("chore: config")
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land with land.promote=main: %v", err)
	}
	got := landCallNames(rec)
	if !strings.Contains(got, "merge feat --into develop --no-ai | push --from develop | merge develop --into main --no-ai | push --from main") {
		t.Errorf("config target must walk the chain like --promote=main: %q", got)
	}
}

// TestLand_NoPromoteOverridesConfig: --no-promote must beat land.promote
// for one run — the per-invocation escape from the config default.
func TestLand_NoPromoteOverridesConfig(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile(".gk.yaml", "land:\n  promote: parent\n")
	repo.Commit("chore: config")
	repo.RunGit("checkout", "-b", "feature")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("no-promote", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --no-promote: %v", err)
	}
	if got := landCallNames(rec); strings.Contains(got, "merge") {
		t.Errorf("--no-promote must skip the promote step: %q", got)
	}
}

// TestLand_PromoteFlagBeatsConfig: an explicit --promote=<branch> wins over
// the config default — the flag is the per-invocation override, not an
// addition on top of config.
func TestLand_PromoteFlagBeatsConfig(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile(".gk.yaml", "land:\n  promote: main\n")
	repo.Commit("chore: config")
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupLandTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Flags().Set("promote", "develop"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("land --promote=develop: %v", err)
	}
	got := landCallNames(rec)
	if !strings.Contains(got, "merge feat --into develop --no-ai | push --from develop") {
		t.Errorf("flag target develop must run: %q", got)
	}
	if strings.Contains(got, "--into main") {
		t.Errorf("config target main must not run when the flag overrides: %q", got)
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
