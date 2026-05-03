package aichat

import "testing"

// ---------------------------------------------------------------------------
// SafetyClassifier.Classify — dangerous commands → RiskHigh
// ---------------------------------------------------------------------------

func TestClassify_GitPushForce(t *testing.T) {
	sc := &SafetyClassifier{}

	cases := []string{
		"git push --force",
		"git push --force origin main",
		"git push -f",
		"git push -f origin main",
		"git push origin main --force",
		"git push origin main -f",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

func TestClassify_GitResetHard(t *testing.T) {
	sc := &SafetyClassifier{}

	cases := []string{
		"git reset --hard",
		"git reset --hard HEAD~1",
		"git reset --hard origin/main",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

func TestClassify_GitClean(t *testing.T) {
	sc := &SafetyClassifier{}

	cases := []string{
		"git clean -f",
		"git clean -fd",
		"git clean -f -d",
		"git clean -fdx",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

func TestClassify_GitBranchForceDelete(t *testing.T) {
	sc := &SafetyClassifier{}

	cases := []string{
		"git branch -D feature/old",
		"git branch -D main",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

func TestClassify_GitRebase(t *testing.T) {
	sc := &SafetyClassifier{}

	cases := []string{
		"git rebase main",
		"git rebase origin/main",
		"git rebase --onto main feature",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

func TestClassify_GitCheckoutDot(t *testing.T) {
	sc := &SafetyClassifier{}

	cmd := "git checkout -- ."
	risk, reason := sc.Classify(cmd)
	if risk != RiskHigh {
		t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
	}
	if reason == "" {
		t.Errorf("Classify(%q) reason is empty", cmd)
	}
}

func TestClassify_GkWipe(t *testing.T) {
	sc := &SafetyClassifier{}

	cases := []string{
		"gk wipe",
		"gk wipe --all",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

func TestClassify_GkReset(t *testing.T) {
	sc := &SafetyClassifier{}

	cases := []string{
		"gk reset",
		"gk reset --hard",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

// ---------------------------------------------------------------------------
// SafetyClassifier.Classify — safe commands → RiskNone
// ---------------------------------------------------------------------------

func TestClassify_SafeCommands(t *testing.T) {
	sc := &SafetyClassifier{}

	cases := []string{
		"git status",
		"git log --oneline",
		"git add .",
		"git commit -m 'hello'",
		"git push",
		"git push origin main",
		"git pull",
		"git fetch",
		"git branch feature/new",
		"git branch -d feature/merged",
		"git checkout main",
		"git diff",
		"git stash",
		"gk log",
		"gk sync",
		"gk push",
		"gk status",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskNone {
			t.Errorf("Classify(%q) risk = %v, want RiskNone", cmd, risk)
		}
		if reason != "" {
			t.Errorf("Classify(%q) reason = %q, want empty", cmd, reason)
		}
	}
}

// ---------------------------------------------------------------------------
// SafetyClassifier.Classify — whitespace handling
// ---------------------------------------------------------------------------

func TestClassify_LeadingTrailingWhitespace(t *testing.T) {
	sc := &SafetyClassifier{}

	risk, _ := sc.Classify("  git push --force  ")
	if risk != RiskHigh {
		t.Errorf("expected RiskHigh for whitespace-padded force push, got %v", risk)
	}
}

// ---------------------------------------------------------------------------
// SafetyClassifier.ClassifyPlan
// ---------------------------------------------------------------------------

func TestClassifyPlan_NoDangerous(t *testing.T) {
	sc := &SafetyClassifier{}
	plan := &ExecutionPlan{
		Commands: []PlannedCommand{
			{Command: "git add .", Description: "stage all"},
			{Command: "git commit -m 'fix'", Description: "commit"},
			{Command: "gk push", Description: "push"},
		},
	}

	if sc.ClassifyPlan(plan) {
		t.Error("ClassifyPlan should return false for safe-only plan")
	}
	for i, cmd := range plan.Commands {
		if cmd.Risk != RiskNone {
			t.Errorf("command[%d] %q: Risk = %v, want RiskNone", i, cmd.Command, cmd.Risk)
		}
	}
}

func TestClassifyPlan_WithDangerous(t *testing.T) {
	sc := &SafetyClassifier{}
	plan := &ExecutionPlan{
		Commands: []PlannedCommand{
			{Command: "git add .", Description: "stage all"},
			{Command: "git push --force", Description: "force push"},
			{Command: "git status", Description: "check status"},
		},
	}

	if !sc.ClassifyPlan(plan) {
		t.Error("ClassifyPlan should return true when plan contains dangerous command")
	}

	// Verify individual risk fields are populated.
	if plan.Commands[0].Risk != RiskNone {
		t.Errorf("command[0] Risk = %v, want RiskNone", plan.Commands[0].Risk)
	}
	if plan.Commands[1].Risk != RiskHigh {
		t.Errorf("command[1] Risk = %v, want RiskHigh", plan.Commands[1].Risk)
	}
	if plan.Commands[1].RiskReason == "" {
		t.Error("command[1] RiskReason should not be empty")
	}
	if plan.Commands[2].Risk != RiskNone {
		t.Errorf("command[2] Risk = %v, want RiskNone", plan.Commands[2].Risk)
	}
}

func TestClassifyPlan_NilPlan(t *testing.T) {
	sc := &SafetyClassifier{}
	if sc.ClassifyPlan(nil) {
		t.Error("ClassifyPlan(nil) should return false")
	}
}

func TestClassifyPlan_EmptyPlan(t *testing.T) {
	sc := &SafetyClassifier{}
	plan := &ExecutionPlan{}
	if sc.ClassifyPlan(plan) {
		t.Error("ClassifyPlan with empty commands should return false")
	}
}

// ---------------------------------------------------------------------------
// SafetyClassifier.Classify — newly added dangerous patterns
// ---------------------------------------------------------------------------

func TestClassify_GitConfig(t *testing.T) {
	sc := &SafetyClassifier{}
	cases := []string{
		"git config user.name evil",
		"git config core.sshCommand malicious",
		"git config --global alias.push '!rm -rf /'",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

func TestClassify_GitCredential(t *testing.T) {
	sc := &SafetyClassifier{}
	risk, reason := sc.Classify("git credential fill")
	if risk != RiskHigh {
		t.Errorf("Classify(git credential fill) risk = %v, want RiskHigh", risk)
	}
	if reason == "" {
		t.Error("reason is empty")
	}
}

func TestClassify_GitRemoteSetUrl(t *testing.T) {
	sc := &SafetyClassifier{}
	cases := []string{
		"git remote set-url origin https://evil.com/repo",
		"git remote add evil https://evil.com/repo",
		"git remote remove origin",
	}
	for _, cmd := range cases {
		risk, reason := sc.Classify(cmd)
		if risk != RiskHigh {
			t.Errorf("Classify(%q) risk = %v, want RiskHigh", cmd, risk)
		}
		if reason == "" {
			t.Errorf("Classify(%q) reason is empty", cmd)
		}
	}
}

func TestClassify_GitFilterBranch(t *testing.T) {
	sc := &SafetyClassifier{}
	risk, reason := sc.Classify("git filter-branch --all")
	if risk != RiskHigh {
		t.Errorf("Classify(git filter-branch) risk = %v, want RiskHigh", risk)
	}
	if reason == "" {
		t.Error("reason is empty")
	}
}

func TestClassify_ForceWithLease_IsLowRisk(t *testing.T) {
	sc := &SafetyClassifier{}
	cases := []string{
		"git push --force-with-lease",
		"git push --force-with-lease origin main",
		"git push origin main --force-with-lease",
	}
	for _, cmd := range cases {
		risk, _ := sc.Classify(cmd)
		if risk != RiskLow {
			t.Errorf("Classify(%q) risk = %v, want RiskLow (not RiskHigh)", cmd, risk)
		}
	}
}
