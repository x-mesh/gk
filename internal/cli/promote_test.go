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

// setupPromoteTest mirrors setupLandTest: the promote cobra command scoped
// to dir, with child executions captured instead of spawning gk processes.
func setupPromoteTest(t *testing.T, dir string, args ...string) (*cobra.Command, *landRecorder, *bytes.Buffer, *bytes.Buffer) {
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

	cmd := &cobra.Command{Use: "promote", Args: cobra.MaximumNArgs(1), RunE: runPromote, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().Bool("push", false, "")
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd, rec, stdout, stderr
}

// Bare promote on a dirty tree: commit, then ONE merge into the parent —
// and crucially no pull, no push.
func TestPromote_DirtyCommitsThenMergesParent(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")
	repo.WriteFile("wip.txt", "x\n") // dirty

	cmd, rec, _, _ := setupPromoteTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main") // trunk pinned — the parent must still win
	if err := cmd.Execute(); err != nil {
		t.Fatalf("promote: %v", err)
	}
	want := "commit -f | merge feat --into develop --no-ai"
	if got := landCallNames(rec); got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

func TestPromote_CleanTreeSkipsCommit(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feature")

	cmd, rec, _, _ := setupPromoteTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if got := landCallNames(rec); got != "merge feature --into main --no-ai" {
		t.Errorf("steps = %q", got)
	}
}

// --push publishes each advanced branch after its merge — the land
// --promote behavior, opt-in here.
func TestPromote_PushOptIn(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feature")

	cmd, rec, _, _ := setupPromoteTest(t, repo.Dir, "--push")
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("promote --push: %v", err)
	}
	want := "merge feature --into main --no-ai | push --from main"
	if got := landCallNames(rec); got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

// promote <target> walks the parent chain hop by hop, merge only — no
// push anywhere without --push.
func TestPromote_ChainWalksParents(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "develop")
	repo.RunGit("checkout", "-b", "feat")
	repo.RunGit("config", "branch.feat.gk-parent", "develop")

	cmd, rec, _, _ := setupPromoteTest(t, repo.Dir, "main")
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("promote main: %v", err)
	}
	want := "merge feat --into develop --no-ai | merge develop --into main --no-ai"
	if got := landCallNames(rec); got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

// Already on the target: a quiet no-op — and the dirty tree must NOT be
// auto-committed when there is no merge to perform.
func TestPromote_NothingToPromoteOnTarget(t *testing.T) {
	repo := testutil.NewRepo(t) // on main
	repo.WriteFile("wip.txt", "x\n")

	cmd, rec, stdout, _ := setupPromoteTest(t, repo.Dir, "main")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("promote main on main: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("no child may run: %v", rec.calls)
	}
	if !strings.Contains(stdout.String(), "nothing to promote") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// A target outside the parent chain aborts before any step, with the
// promote-flavored direct-merge escape (no push suggestion).
func TestPromote_TargetNotInChain(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "staging")
	repo.RunGit("checkout", "main")
	repo.RunGit("checkout", "-b", "feat")

	cmd, rec, _, _ := setupPromoteTest(t, repo.Dir, "staging")
	t.Setenv("GK_BASE_BRANCH", "main")
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not in the parent chain") {
		t.Fatalf("want not-in-chain error, got %v", err)
	}
	if !strings.Contains(err.Error(), "promote staging") {
		t.Errorf("error must carry the promote flavor, got %v", err)
	}
	hint := HintFrom(err)
	if !strings.Contains(hint, "gk merge feat --into staging") {
		t.Errorf("hint must offer the direct-merge escape: %q", hint)
	}
	if strings.Contains(hint, "push --from") {
		t.Errorf("promote (no-push verb) must not suggest a push: %q", hint)
	}
	if len(rec.calls) != 0 {
		t.Errorf("no step may run after a chain resolution failure: %v", rec.calls)
	}
}

// The resume must match the failure kind: a conflict pause (child exit 3)
// points at resolve/continue, but a guard refusal (exit 1 — dirty
// receiver, precheck conflicts) must NOT — there is no merge to resolve.
func TestPromote_ResumeMatchesFailureKind(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feature")
	t.Setenv("GK_BASE_BRANCH", "main")

	// Plain refusal: exit 1.
	cmd, rec, _, _ := setupPromoteTest(t, repo.Dir)
	rec.failOn = "merge"
	rec.failErr = &childExitError{Code: 1}
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want step failure")
	}
	hint := HintFrom(err)
	if strings.Contains(hint, "resolve --ai") {
		t.Errorf("guard refusal must not suggest conflict resolution: %q", hint)
	}
	if !strings.Contains(hint, "rerun: gk promote") {
		t.Errorf("resume must name the rerun: %q", hint)
	}

	// Conflict pause: exit 3.
	cmd2, rec2, _, _ := setupPromoteTest(t, repo.Dir)
	rec2.failOn = "merge"
	rec2.failErr = &childExitError{Code: 3}
	err2 := cmd2.Execute()
	if err2 == nil {
		t.Fatal("want step failure")
	}
	if hint2 := HintFrom(err2); !strings.Contains(hint2, "gk resolve --ai && gk continue") {
		t.Errorf("conflict pause must point at resolve/continue: %q", hint2)
	}
}

// A failed merge names the step with a promote rerun remedy and the JSON
// contract carries failed_step/resume — same shape as land.
func TestPromote_FailureJSONContract(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feature")

	prevJ := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJ })

	cmd, rec, stdout, _ := setupPromoteTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	rec.failOn = "merge"
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `promote: step "promote" failed`) {
		t.Fatalf("want promote step failure, got %v", err)
	}
	r := RemediesFrom(err)
	if len(r) == 0 || r[0].Command != "gk promote" {
		t.Errorf("remedies: %+v", r)
	}

	var res landResultJSON
	if jerr := json.Unmarshal(stdout.Bytes(), &res); jerr != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if res.Result != "failed" || res.FailedStep != "promote" {
		t.Errorf("result = %q, failed_step = %q", res.Result, res.FailedStep)
	}
	if !strings.Contains(res.Resume, "gk promote") {
		t.Errorf("resume = %q", res.Resume)
	}
}

func TestPromote_JSONSuccessContract(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feature")

	prevJ := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJ })

	cmd, _, stdout, _ := setupPromoteTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("promote: %v", err)
	}
	var res landResultJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", err, stdout.String())
	}
	if res.Result != "promoted" {
		t.Errorf("result = %q, want promoted", res.Result)
	}
}

func TestPromote_DryRunExecutesNothing(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("checkout", "-b", "feature")
	repo.WriteFile("wip.txt", "x\n")
	prevD := flagDryRun
	flagDryRun = true
	t.Cleanup(func() { flagDryRun = prevD })

	cmd, rec, stdout, _ := setupPromoteTest(t, repo.Dir)
	t.Setenv("GK_BASE_BRANCH", "main")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("promote --dry-run: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("dry-run must not exec children: %v", rec.calls)
	}
	out := stdout.String()
	if !strings.Contains(out, "merge feature --into main") || strings.Contains(out, "push --from") {
		t.Errorf("plan must show a push-free merge: %q", out)
	}
}
