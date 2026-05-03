package aichat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestExecutor creates a CommandExecutor with a FakeRunner and buffers.
func newTestExecutor() (*CommandExecutor, *bytes.Buffer, *bytes.Buffer, *git.FakeRunner) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
		},
	}
	exec := &CommandExecutor{
		Runner:       runner,
		Out:          out,
		ErrOut:       errOut,
		SafetyConfig: SafetyConfig{SafetyConfirm: true},
		ConfirmFunc: func(prompt string) (bool, error) {
			return true, nil // default: always confirm
		},
	}
	return exec, out, errOut, runner
}

// alwaysConfirm returns a ConfirmFunc that always says yes.
func alwaysConfirm() ConfirmFunc {
	return func(prompt string) (bool, error) { return true, nil }
}

// alwaysDeny returns a ConfirmFunc that always says no.
func alwaysDeny() ConfirmFunc {
	return func(prompt string) (bool, error) { return false, nil }
}

// confirmCounter returns a ConfirmFunc that counts calls and always says yes.
func confirmCounter() (ConfirmFunc, *int) {
	count := 0
	return func(prompt string) (bool, error) {
		count++
		return true, nil
	}, &count
}

// confirmNth returns a ConfirmFunc that says yes for the first n calls,
// then says no.
func confirmNth(n int) ConfirmFunc {
	count := 0
	return func(prompt string) (bool, error) {
		count++
		if count <= n {
			return true, nil
		}
		return false, nil
	}
}

// safePlan creates a simple plan with safe commands.
func safePlan(cmds ...string) *ExecutionPlan {
	plan := &ExecutionPlan{}
	for _, c := range cmds {
		plan.Commands = append(plan.Commands, PlannedCommand{
			Command:     c,
			Description: "test command",
		})
	}
	return plan
}

// dangerousPlan creates a plan with a mix of safe and dangerous commands.
func dangerousPlan() *ExecutionPlan {
	return &ExecutionPlan{
		Commands: []PlannedCommand{
			{Command: "git add .", Description: "stage all", Risk: RiskNone},
			{Command: "git push --force", Description: "force push", Dangerous: true, Risk: RiskHigh, RiskReason: "overwrites remote history"},
			{Command: "git status", Description: "check status", Risk: RiskNone},
		},
	}
}

// ---------------------------------------------------------------------------
// Test: Preview output contains all commands
// ---------------------------------------------------------------------------

func TestPreview_ContainsAllCommands(t *testing.T) {
	exec, _, _, _ := newTestExecutor()
	plan := safePlan("git add .", "git commit -m 'test'", "gk push")

	preview := exec.Preview(plan)

	for _, cmd := range plan.Commands {
		if !strings.Contains(preview, cmd.Command) {
			t.Errorf("Preview should contain %q, got:\n%s", cmd.Command, preview)
		}
	}

	// Check numbering
	if !strings.Contains(preview, "1.") {
		t.Error("Preview should contain numbering '1.'")
	}
	if !strings.Contains(preview, "3.") {
		t.Error("Preview should contain numbering '3.'")
	}
}

// ---------------------------------------------------------------------------
// Test: Preview shows ⚠️ for dangerous commands
// ---------------------------------------------------------------------------

func TestPreview_ShowsDangerLabel(t *testing.T) {
	exec, _, _, _ := newTestExecutor()
	plan := dangerousPlan()

	preview := exec.Preview(plan)

	if !strings.Contains(preview, "⚠️") {
		t.Errorf("Preview should contain ⚠️ for dangerous commands, got:\n%s", preview)
	}
	if !strings.Contains(preview, "위험") {
		t.Errorf("Preview should contain '위험' label, got:\n%s", preview)
	}
	if !strings.Contains(preview, "overwrites remote history") {
		t.Errorf("Preview should contain risk reason, got:\n%s", preview)
	}
}

// ---------------------------------------------------------------------------
// Test: Preview with nil/empty plan
// ---------------------------------------------------------------------------

func TestPreview_EmptyPlan(t *testing.T) {
	exec, _, _, _ := newTestExecutor()

	if got := exec.Preview(nil); got != "" {
		t.Errorf("Preview(nil) = %q, want empty", got)
	}
	if got := exec.Preview(&ExecutionPlan{}); got != "" {
		t.Errorf("Preview(empty) = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Test: DryRun mode doesn't execute commands
// ---------------------------------------------------------------------------

func TestExecute_DryRun_NoExecution(t *testing.T) {
	exec, out, _, runner := newTestExecutor()
	plan := safePlan("git add .", "git commit -m 'test'")

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Executed) != 0 {
		t.Errorf("DryRun should not execute commands, got %d results", len(result.Executed))
	}
	// Runner should not have been called for command execution
	// (only preview is shown)
	if len(runner.Calls) != 0 {
		t.Errorf("DryRun should not call Runner, got %d calls", len(runner.Calls))
	}
	// Output should contain the preview
	if !strings.Contains(out.String(), "git add .") {
		t.Errorf("DryRun output should contain command preview, got:\n%s", out.String())
	}
}

// ---------------------------------------------------------------------------
// Test: Sequential execution stops on error
// ---------------------------------------------------------------------------

func TestExecute_StopsOnError(t *testing.T) {
	exec, _, errOut, runner := newTestExecutor()

	// Second command fails
	runner.Responses["commit -m test"] = git.FakeResponse{
		Stderr:   "nothing to commit",
		ExitCode: 1,
	}

	plan := safePlan("git add .", "git commit -m test", "gk push")

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{Yes: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have executed 2 commands (add succeeded, commit failed)
	if len(result.Executed) != 2 {
		t.Fatalf("expected 2 executed commands, got %d", len(result.Executed))
	}
	if result.Executed[0].Error != nil {
		t.Error("first command should succeed")
	}
	if result.Executed[1].Error == nil {
		t.Error("second command should fail")
	}
	if !result.Aborted {
		t.Error("result should be aborted")
	}

	// Error output should mention the failure
	if !strings.Contains(errOut.String(), "command failed") {
		t.Errorf("error output should mention failure, got:\n%s", errOut.String())
	}
	// Should mention remaining commands skipped
	if !strings.Contains(errOut.String(), "1 remaining") {
		t.Errorf("error output should mention remaining commands, got:\n%s", errOut.String())
	}
}

// ---------------------------------------------------------------------------
// Test: NonTTY without --yes returns error
// ---------------------------------------------------------------------------

func TestExecute_NonTTY_NoYes_ReturnsError(t *testing.T) {
	exec, _, _, _ := newTestExecutor()
	plan := safePlan("git status")

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{NonTTY: true})
	if err == nil {
		t.Fatal("expected error for NonTTY without --yes")
	}
	if result != nil {
		t.Error("expected nil result for NonTTY error")
	}

	nonTTYErr, ok := err.(*NonTTYError)
	if !ok {
		t.Fatalf("expected *NonTTYError, got %T", err)
	}
	if nonTTYErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2", nonTTYErr.ExitCode())
	}
	if !strings.Contains(err.Error(), "non-interactive") {
		t.Errorf("error should mention non-interactive, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: NonTTY with --yes executes normally
// ---------------------------------------------------------------------------

func TestExecute_NonTTY_WithYes_Executes(t *testing.T) {
	exec, _, _, _ := newTestExecutor()
	plan := safePlan("git status")

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{NonTTY: true, Yes: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Executed) != 1 {
		t.Errorf("expected 1 executed command, got %d", len(result.Executed))
	}
}

// ---------------------------------------------------------------------------
// Test: NonTTY with --dry-run works
// ---------------------------------------------------------------------------

func TestExecute_NonTTY_WithDryRun_Works(t *testing.T) {
	exec, _, _, runner := newTestExecutor()
	plan := safePlan("git status")

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{NonTTY: true, DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Executed) != 0 {
		t.Errorf("DryRun should not execute, got %d results", len(result.Executed))
	}
	if len(runner.Calls) != 0 {
		t.Errorf("DryRun should not call Runner, got %d calls", len(runner.Calls))
	}
}

// ---------------------------------------------------------------------------
// Test: Backup ref created before dangerous commands
// ---------------------------------------------------------------------------

func TestExecute_BackupRef_CreatedBeforeDangerous(t *testing.T) {
	exec, _, _, runner := newTestExecutor()
	exec.ConfirmFunc = alwaysConfirm()

	// Set up responses for backup ref creation
	runner.Responses["rev-parse --abbrev-ref HEAD"] = git.FakeResponse{Stdout: "feature\n"}

	plan := &ExecutionPlan{
		Commands: []PlannedCommand{
			{Command: "git add .", Description: "stage all"},
			{Command: "git reset --hard HEAD~1", Description: "undo commit", Risk: RiskHigh, RiskReason: "resets working tree"},
		},
	}

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.BackupRef == "" {
		t.Fatal("expected backup ref to be created")
	}
	if !strings.HasPrefix(result.BackupRef, "refs/gk/do-backup/feature/") {
		t.Errorf("backup ref = %q, want prefix 'refs/gk/do-backup/feature/'", result.BackupRef)
	}

	// Verify update-ref was called
	found := false
	for _, call := range runner.Calls {
		if len(call.Args) >= 2 && call.Args[0] == "update-ref" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected update-ref call for backup")
	}
}

// ---------------------------------------------------------------------------
// Test: --force skips all confirmations
// ---------------------------------------------------------------------------

func TestExecute_Force_SkipsAllConfirmations(t *testing.T) {
	confirmFn, count := confirmCounter()
	exec, _, _, _ := newTestExecutor()
	exec.ConfirmFunc = confirmFn

	plan := dangerousPlan()

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No confirmations should have been asked
	if *count != 0 {
		t.Errorf("--force should skip all confirmations, got %d calls", *count)
	}
	if len(result.Executed) != 3 {
		t.Errorf("expected 3 executed commands, got %d", len(result.Executed))
	}
}

// ---------------------------------------------------------------------------
// Test: --yes skips normal but not dangerous confirmations
// ---------------------------------------------------------------------------

func TestExecute_Yes_SkipsNormalButNotDangerous(t *testing.T) {
	prompts := []string{}
	exec, _, _, _ := newTestExecutor()
	exec.ConfirmFunc = func(prompt string) (bool, error) {
		prompts = append(prompts, prompt)
		return true, nil
	}

	plan := dangerousPlan()

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{Yes: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With --yes, normal confirmation is skipped.
	// Only dangerous command confirmation should be asked (1 dangerous command).
	if len(prompts) != 1 {
		t.Errorf("expected 1 dangerous confirmation prompt, got %d: %v", len(prompts), prompts)
	}
	if len(prompts) > 0 && !strings.Contains(prompts[0], "위험") {
		t.Errorf("prompt should mention danger, got: %s", prompts[0])
	}
	if len(result.Executed) != 3 {
		t.Errorf("expected 3 executed commands, got %d", len(result.Executed))
	}
}

// ---------------------------------------------------------------------------
// Test: User rejection aborts execution
// ---------------------------------------------------------------------------

func TestExecute_UserRejection_Aborts(t *testing.T) {
	exec, _, _, runner := newTestExecutor()
	exec.ConfirmFunc = alwaysDeny()

	plan := safePlan("git add .", "git commit -m 'test'")

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Aborted {
		t.Error("result should be aborted")
	}
	if len(result.Executed) != 0 {
		t.Errorf("no commands should be executed, got %d", len(result.Executed))
	}
	// Runner should not have been called for command execution
	if len(runner.Calls) != 0 {
		t.Errorf("Runner should not be called on rejection, got %d calls", len(runner.Calls))
	}
}

// ---------------------------------------------------------------------------
// Test: JSON output mode
// ---------------------------------------------------------------------------

func TestExecute_JSON_OutputsJSON(t *testing.T) {
	exec, out, _, runner := newTestExecutor()
	plan := safePlan("git add .", "git commit -m 'test'")

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{JSON: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Executed) != 0 {
		t.Errorf("JSON mode should not execute commands, got %d results", len(result.Executed))
	}
	if len(runner.Calls) != 0 {
		t.Errorf("JSON mode should not call Runner, got %d calls", len(runner.Calls))
	}

	// Output should be valid JSON
	var parsed ExecutionPlan
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("output should be valid JSON: %v\noutput:\n%s", err, out.String())
	}
	if len(parsed.Commands) != 2 {
		t.Errorf("JSON should contain 2 commands, got %d", len(parsed.Commands))
	}
}

// ---------------------------------------------------------------------------
// Test: safety_confirm=false — dangerous commands still require confirmation
// because --yes mode always confirms dangerous commands (security hardening).
// ---------------------------------------------------------------------------

func TestExecute_SafetyConfirmFalse_StillConfirmsDangerous(t *testing.T) {
	prompts := []string{}
	exec, _, _, _ := newTestExecutor()
	exec.SafetyConfig.SafetyConfirm = false
	exec.ConfirmFunc = func(prompt string) (bool, error) {
		prompts = append(prompts, prompt)
		return true, nil
	}

	plan := dangerousPlan()

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Normal confirmation + dangerous command confirmation (security hardening:
	// dangerous commands always require confirmation regardless of SafetyConfirm).
	if len(prompts) != 2 {
		t.Errorf("expected 2 confirmation prompts (normal + dangerous), got %d: %v", len(prompts), prompts)
	}
	if len(result.Executed) != 3 {
		t.Errorf("expected 3 executed commands, got %d", len(result.Executed))
	}
}

// ---------------------------------------------------------------------------
// Test: Context cancellation stops execution
// ---------------------------------------------------------------------------

func TestExecute_ContextCancellation(t *testing.T) {
	exec, _, errOut, _ := newTestExecutor()

	plan := safePlan("git add .", "git commit -m 'test'", "gk push")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := exec.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Aborted {
		t.Error("result should be aborted on context cancellation")
	}
	if !strings.Contains(errOut.String(), "interrupted") {
		t.Errorf("error output should mention interruption, got:\n%s", errOut.String())
	}
}

// ---------------------------------------------------------------------------
// Test: Empty/nil plan returns empty result
// ---------------------------------------------------------------------------

func TestExecute_EmptyPlan(t *testing.T) {
	exec, _, _, _ := newTestExecutor()

	result, err := exec.Execute(context.Background(), nil, ExecuteOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Executed) != 0 {
		t.Errorf("empty plan should return empty result, got %d", len(result.Executed))
	}

	result, err = exec.Execute(context.Background(), &ExecutionPlan{}, ExecuteOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Executed) != 0 {
		t.Errorf("empty plan should return empty result, got %d", len(result.Executed))
	}
}

// ---------------------------------------------------------------------------
// Test: Dangerous command rejection shows executed count
// ---------------------------------------------------------------------------

func TestExecute_DangerousRejection_ShowsExecutedCount(t *testing.T) {
	callCount := 0
	exec, _, errOut, _ := newTestExecutor()
	exec.ConfirmFunc = func(prompt string) (bool, error) {
		callCount++
		// Accept normal confirmation, reject dangerous
		if strings.Contains(prompt, "위험") {
			return false, nil
		}
		return true, nil
	}

	plan := &ExecutionPlan{
		Commands: []PlannedCommand{
			{Command: "git add .", Description: "stage all"},
			{Command: "git reset --hard HEAD~1", Description: "undo", Risk: RiskHigh, RiskReason: "resets working tree"},
		},
	}

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Aborted {
		t.Error("result should be aborted")
	}
	// First command should have executed, second rejected
	if len(result.Executed) != 1 {
		t.Errorf("expected 1 executed command, got %d", len(result.Executed))
	}
	if !strings.Contains(errOut.String(), "위험 명령어 거부") {
		t.Errorf("error output should mention dangerous rejection, got:\n%s", errOut.String())
	}
}

// ---------------------------------------------------------------------------
// Test: Error output includes recovery hint with backup ref
// ---------------------------------------------------------------------------

func TestExecute_ErrorWithBackupRef_ShowsRecoveryHint(t *testing.T) {
	exec, _, errOut, runner := newTestExecutor()
	exec.ConfirmFunc = alwaysConfirm()

	runner.Responses["rev-parse --abbrev-ref HEAD"] = git.FakeResponse{Stdout: "main\n"}
	// The dangerous command fails
	runner.Responses["reset --hard HEAD~1"] = git.FakeResponse{
		Stderr:   "fatal: error",
		ExitCode: 1,
	}

	plan := &ExecutionPlan{
		Commands: []PlannedCommand{
			{Command: "git reset --hard HEAD~1", Description: "undo", Risk: RiskHigh, RiskReason: "resets working tree"},
		},
	}

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.BackupRef == "" {
		t.Fatal("expected backup ref")
	}
	if !strings.Contains(errOut.String(), "restore with") {
		t.Errorf("error output should contain recovery hint, got:\n%s", errOut.String())
	}
}

// ---------------------------------------------------------------------------
// Test: Successful execution shows done markers
// ---------------------------------------------------------------------------

func TestExecute_Success_ShowsDoneMarkers(t *testing.T) {
	exec, out, _, _ := newTestExecutor()
	plan := safePlan("git add .", "git status")

	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{Yes: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Executed) != 2 {
		t.Errorf("expected 2 executed commands, got %d", len(result.Executed))
	}
	if result.Aborted {
		t.Error("result should not be aborted")
	}

	output := out.String()
	if strings.Count(output, "✓ done") != 2 {
		t.Errorf("expected 2 '✓ done' markers, got output:\n%s", output)
	}
}

// ---------------------------------------------------------------------------
// Test: ConfirmFunc error propagates
// ---------------------------------------------------------------------------

func TestExecute_ConfirmError_Propagates(t *testing.T) {
	exec, _, _, _ := newTestExecutor()
	exec.ConfirmFunc = func(prompt string) (bool, error) {
		return false, fmt.Errorf("terminal broken")
	}

	plan := safePlan("git status")

	_, err := exec.Execute(context.Background(), plan, ExecuteOptions{})
	if err == nil {
		t.Fatal("expected error from broken ConfirmFunc")
	}
	if !strings.Contains(err.Error(), "confirmation error") {
		t.Errorf("error should mention confirmation, got: %v", err)
	}
}
