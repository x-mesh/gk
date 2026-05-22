package aichat

import (
	"context"
	"strings"
	"testing"
)

// New destructive patterns must be classified RiskHigh so `gk do` requires
// an extra confirmation before running them.
func TestClassify_DestructiveDataLossPatterns(t *testing.T) {
	sc := &SafetyClassifier{}
	high := []string{
		"git rm file.txt",
		"git rm -r dir",
		"git restore file.txt",
		"git restore --staged .",
		"git checkout -- main.go",
		"git stash drop",
		"git stash clear",
		"git update-ref -d refs/heads/x",
		"git reflog expire --all",
		"git gc --prune=now",
	}
	for _, cmd := range high {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
	// Safe forms must NOT be flagged dangerous.
	for _, cmd := range []string{"git status", "git log", "git stash list", "git stash show", "git checkout main"} {
		if risk, _ := sc.Classify(cmd); risk == RiskHigh {
			t.Errorf("Classify(%q) = RiskHigh, want not high", cmd)
		}
	}
}

// runCommand must re-check the git subcommand whitelist at execution time,
// not only at parse time, so a non-whitelisted subcommand (e.g. `git config`)
// is refused even if it reaches the runner directly.
func TestExecute_BlocksNonWhitelistedGitSubcommandAtRuntime(t *testing.T) {
	exec, _, _, _ := newTestExecutor()
	plan := &ExecutionPlan{Commands: []PlannedCommand{
		{Command: "git config user.email evil@example.com"},
	}}
	// Force skips confirmation so we reach runCommand directly.
	result, err := exec.Execute(context.Background(), plan, ExecuteOptions{Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Executed) != 1 {
		t.Fatalf("expected 1 executed entry, got %d", len(result.Executed))
	}
	got := result.Executed[0]
	if got.Error == nil {
		t.Fatal("expected `git config` to be blocked at runtime, got nil error")
	}
	if !strings.Contains(got.Error.Error(), "not allowed") {
		t.Errorf("unexpected error: %v", got.Error)
	}
	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
}
