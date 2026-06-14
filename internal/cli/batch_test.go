package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestResolveBatchWorktree covers branch-name and absolute-path resolution
// plus the miss cases that must fail a step rather than run in the repo root.
func TestResolveBatchWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	wtPath := filepath.Join(t.TempDir(), "wt-a")
	if _, _, err := runner.Run(ctx, "worktree", "add", "-b", "feat-a", wtPath); err != nil {
		t.Fatalf("setup worktree: %v", err)
	}
	want := canonWorktreePath(wtPath)

	got, err := resolveBatchWorktree(ctx, runner, "feat-a")
	if err != nil {
		t.Fatalf("resolve by branch: %v", err)
	}
	if canonWorktreePath(got) != want {
		t.Errorf("by branch: got %q want %q", got, want)
	}

	got2, err := resolveBatchWorktree(ctx, runner, wtPath)
	if err != nil {
		t.Fatalf("resolve by path: %v", err)
	}
	if canonWorktreePath(got2) != want {
		t.Errorf("by path: got %q want %q", got2, want)
	}

	if _, err := resolveBatchWorktree(ctx, runner, "no-such-branch"); err == nil {
		t.Error("expected error for an unknown branch")
	}
	if _, err := resolveBatchWorktree(ctx, runner, filepath.Join(t.TempDir(), "not-a-worktree")); err == nil {
		t.Error("expected error for an unregistered absolute path")
	}
}

// ---------- plan validation ----------

func TestValidateBatchPlan(t *testing.T) {
	cases := []struct {
		name    string
		plan    batchPlanJSON
		wantErr string // empty = valid
	}{
		{"valid two steps", batchPlanJSON{Schema: 1, Steps: []batchStepJSON{
			{Args: []string{"pull", "--with-base"}},
			{Args: []string{"push"}, OnFailure: "continue"},
		}}, ""},
		{"schema 0 accepted", batchPlanJSON{Steps: []batchStepJSON{{Args: []string{"push"}}}}, ""},
		{"alias accepted", batchPlanJSON{Steps: []batchStepJSON{{Args: []string{"ctx"}}}}, ""},
		{"bad schema", batchPlanJSON{Schema: 9, Steps: []batchStepJSON{{Args: []string{"push"}}}}, "unsupported plan schema"},
		{"no steps", batchPlanJSON{Schema: 1}, "no steps"},
		{"empty args", batchPlanJSON{Steps: []batchStepJSON{{}}}, "no args"},
		{"flag first", batchPlanJSON{Steps: []batchStepJSON{{Args: []string{"--json"}}}}, "must start with a sub-command"},
		{"nested batch", batchPlanJSON{Steps: []batchStepJSON{{Args: []string{"batch", "--plan", "-"}}}}, "nested batch"},
		{"unknown subcommand", batchPlanJSON{Steps: []batchStepJSON{{Args: []string{"frobnicate"}}}}, "unknown gk sub-command"},
		{"bad on_failure", batchPlanJSON{Steps: []batchStepJSON{{Args: []string{"push"}, OnFailure: "retry"}}}, "on_failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBatchPlan(tc.plan)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateBatchPlan_TooManySteps(t *testing.T) {
	plan := batchPlanJSON{Schema: 1}
	for i := 0; i < batchMaxSteps+1; i++ {
		plan.Steps = append(plan.Steps, batchStepJSON{Args: []string{"push"}})
	}
	err := validateBatchPlan(plan)
	if err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("want max-steps error, got %v", err)
	}
}

// ---------- execution via stubbed children ----------

// batchRecorder captures stubbed child executions; exits scripts the exit
// code per sub-command word (missing = 0/success).
type batchRecorder struct {
	calls [][]string
	exits map[string]int
}

// setupBatchTest mirrors setupLandTest: the batch cobra command scoped to
// dir, child executions captured instead of spawning real gk processes.
func setupBatchTest(t *testing.T, dir string) (*cobra.Command, *batchRecorder, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	prev := flagRepo
	flagRepo = dir
	t.Cleanup(func() { flagRepo = prev })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	rec := &batchRecorder{}
	prevChild := batchRunChild
	batchRunChild = func(ctx context.Context, gkPath, repo string, jsonMode bool, args ...string) (int, error) {
		rec.calls = append(rec.calls, args)
		if code := rec.exits[args[0]]; code != 0 {
			return code, fmt.Errorf("exit %d", code)
		}
		return 0, nil
	}
	t.Cleanup(func() { batchRunChild = prevChild })

	cmd := &cobra.Command{Use: "batch", RunE: runBatch, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().String("plan", "", "")
	cmd.Flags().Bool("plan-template", false, "")
	cmd.SetContext(context.Background())
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd, rec, stdout, stderr
}

func withBatchJSON(t *testing.T) {
	t.Helper()
	prev := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prev })
}

func batchExec(t *testing.T, cmd *cobra.Command, plan string) error {
	t.Helper()
	if err := cmd.Flags().Set("plan", "-"); err != nil {
		t.Fatal(err)
	}
	cmd.SetIn(strings.NewReader(plan))
	return cmd.Execute()
}

func TestBatch_AllStepsOK(t *testing.T) {
	withBatchJSON(t)
	repo := testutil.NewRepo(t)
	cmd, rec, stdout, _ := setupBatchTest(t, repo.Dir)

	err := batchExec(t, cmd, `{"schema":1,"steps":[{"args":["pull","--with-base"]},{"args":["push"]}]}`)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	var res batchResultJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &res); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if res.Result != "completed" || len(res.Steps) != 2 {
		t.Fatalf("result = %+v, want completed with 2 steps", res)
	}
	if len(rec.calls) != 2 || rec.calls[0][0] != "pull" || rec.calls[1][0] != "push" {
		t.Fatalf("calls = %v", rec.calls)
	}
	for _, s := range res.Steps {
		if s.Result != "ok" {
			t.Errorf("step %s = %s, want ok", s.Name, s.Result)
		}
	}
}

func TestBatch_AbortSkipsRest(t *testing.T) {
	withBatchJSON(t)
	repo := testutil.NewRepo(t)
	cmd, rec, stdout, _ := setupBatchTest(t, repo.Dir)
	rec.exits = map[string]int{"pull": 1}

	err := batchExec(t, cmd, `{"steps":[{"args":["commit","-f"]},{"args":["pull"]},{"args":["push"]}]}`)
	if err == nil {
		t.Fatal("want error on gating failure")
	}
	if len(rec.calls) != 2 {
		t.Fatalf("push must not run after pull failed; calls = %v", rec.calls)
	}
	var res batchResultJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &res); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if res.Result != "failed" || res.FailedStep != "pull" || res.Resume == "" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Steps) != 3 || res.Steps[2].Result != "skipped" {
		t.Fatalf("steps = %+v, want trailing step skipped", res.Steps)
	}
}

func TestBatch_ContinuePolicyYieldsPartial(t *testing.T) {
	withBatchJSON(t)
	repo := testutil.NewRepo(t)
	cmd, rec, stdout, _ := setupBatchTest(t, repo.Dir)
	rec.exits = map[string]int{"push": 1}

	err := batchExec(t, cmd, `{"steps":[{"args":["push"],"on_failure":"continue"},{"args":["pull"]}]}`)
	if err != nil {
		t.Fatalf("continue policy must not fail the call: %v", err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("pull must still run; calls = %v", rec.calls)
	}
	var res batchResultJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &res); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if res.Result != "partial" {
		t.Fatalf("result = %s, want partial", res.Result)
	}
	if res.Steps[0].Result != "failed" || res.Steps[1].Result != "ok" {
		t.Fatalf("steps = %+v", res.Steps)
	}
}

func TestBatch_PausedChildStopsEvenWithContinue(t *testing.T) {
	withBatchJSON(t)
	repo := testutil.NewRepo(t)
	cmd, rec, stdout, _ := setupBatchTest(t, repo.Dir)
	rec.exits = map[string]int{"pull": 3}

	err := batchExec(t, cmd, `{"steps":[{"args":["pull"],"on_failure":"continue"},{"args":["push"]}]}`)
	if err == nil {
		t.Fatal("paused child must stop the plan")
	}
	if len(rec.calls) != 1 {
		t.Fatalf("push must not run over a paused state; calls = %v", rec.calls)
	}
	var res batchResultJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &res); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if res.Steps[0].Result != "paused" || res.Steps[0].ExitCode != 3 {
		t.Fatalf("steps[0] = %+v, want paused exit 3", res.Steps[0])
	}
}

func TestBatch_InvalidPlanRunsNothing(t *testing.T) {
	repo := testutil.NewRepo(t)
	cmd, rec, _, _ := setupBatchTest(t, repo.Dir)

	err := batchExec(t, cmd, `{"steps":[{"args":["pull"]},{"args":["frobnicate"]}]}`)
	if err == nil || !strings.Contains(err.Error(), "unknown gk sub-command") {
		t.Fatalf("want validation error, got %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("validation must reject before any step runs; calls = %v", rec.calls)
	}
}

func TestBatch_DryRunExecutesNothing(t *testing.T) {
	repo := testutil.NewRepo(t)
	prevD := flagDryRun
	flagDryRun = true
	t.Cleanup(func() { flagDryRun = prevD })

	cmd, rec, _, _ := setupBatchTest(t, repo.Dir)
	if err := batchExec(t, cmd, `{"steps":[{"args":["pull"]},{"args":["push"]}]}`); err != nil {
		t.Fatalf("batch --dry-run: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("dry-run must not exec children: %v", rec.calls)
	}
}

func TestBatch_PlanTemplateIsValid(t *testing.T) {
	repo := testutil.NewRepo(t)
	cmd, _, stdout, _ := setupBatchTest(t, repo.Dir)
	if err := cmd.Flags().Set("plan-template", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("plan-template: %v", err)
	}
	var plan batchPlanJSON
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("template not parseable: %v\n%s", err, stdout.String())
	}
	if err := validateBatchPlan(plan); err != nil {
		t.Fatalf("template must validate against its own rules: %v", err)
	}
}
