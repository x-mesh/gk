package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Unit tests for checkBranch (pure function, no I/O)
// ---------------------------------------------------------------------------

func TestCheckBranch_Matches(t *testing.T) {
	patterns := []string{`^(feat|fix|chore|docs|refactor|test|perf|build|ci|revert)/[a-z0-9._-]+$`}
	res := checkBranch("feat/foo", patterns, nil)
	if !res.Matched {
		t.Fatalf("expected Matched=true, got false (reason: %s)", res.Reason)
	}
	if res.Skipped {
		t.Fatal("expected Skipped=false")
	}
}

func TestCheckBranch_ProtectedBypass(t *testing.T) {
	patterns := []string{`^(feat|fix)/[a-z0-9._-]+$`}
	protected := []string{"main", "master", "develop"}
	// "main" does not match the pattern but is protected — should be Skipped.
	res := checkBranch("main", patterns, protected)
	if !res.Skipped {
		t.Fatal("expected Skipped=true for protected branch 'main'")
	}
	if res.Matched {
		t.Fatal("Matched should be false when Skipped is true")
	}
}

func TestCheckBranch_NoPatterns(t *testing.T) {
	res := checkBranch("some-random-branch", nil, nil)
	if !res.Matched {
		t.Fatalf("expected Matched=true when no patterns configured, got false")
	}
	if !strings.Contains(res.Reason, "no patterns") {
		t.Fatalf("expected Reason to mention 'no patterns', got: %q", res.Reason)
	}
}

func TestCheckBranch_NoMatch(t *testing.T) {
	patterns := []string{`^(feat|fix)/[a-z0-9._-]+$`}
	res := checkBranch("random-name", patterns, nil)
	if res.Matched {
		t.Fatal("expected Matched=false for non-matching branch")
	}
	if res.Reason == "" {
		t.Fatal("expected non-empty Reason on failure")
	}
}

func TestCheckBranch_InvalidRegex(t *testing.T) {
	// Invalid pattern is skipped; valid second pattern should match.
	patterns := []string{"[invalid(", `^(feat|fix)/[a-z0-9._-]+$`}
	res := checkBranch("feat/bar", patterns, nil)
	if !res.Matched {
		t.Fatalf("expected Matched=true when second valid pattern matches, got false (reason: %s)", res.Reason)
	}
}

func TestCheckBranch_InvalidRegexOnlyNoMatch(t *testing.T) {
	// All patterns are invalid — should fail (not silently pass).
	patterns := []string{"[invalid(", "[also-bad("}
	res := checkBranch("feat/bar", patterns, nil)
	if res.Matched {
		t.Fatal("expected Matched=false when all patterns are invalid")
	}
}

// ---------------------------------------------------------------------------
// Unit tests for suggestBranchName
// ---------------------------------------------------------------------------

func TestSuggestBranchName_StandardPattern(t *testing.T) {
	patterns := []string{`^(feat|fix|chore)/.+`}
	got := suggestBranchName(patterns)
	if got != "feat/topic-name" {
		t.Fatalf("expected 'feat/topic-name', got %q", got)
	}
}

func TestSuggestBranchName_Empty(t *testing.T) {
	got := suggestBranchName(nil)
	if got != "" {
		t.Fatalf("expected empty string for nil patterns, got %q", got)
	}
}

func TestSuggestBranchName_ExoticPattern(t *testing.T) {
	// Pattern with no alternation block — should return "".
	patterns := []string{`^[a-z]+-[0-9]+$`}
	got := suggestBranchName(patterns)
	if got != "" {
		t.Fatalf("expected empty string for exotic pattern, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Integration tests: cobra command wired up, using --branch flag to avoid
// needing a real git repo (sidesteps os.Exit / git calls).
// ---------------------------------------------------------------------------

func newBranchCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "branch-check",
		RunE:          runBranchCheck,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().String("branch", "", "")
	cmd.Flags().StringSlice("patterns", nil, "")
	cmd.Flags().Bool("quiet", false, "")
	// Persistent flag expected by config.Load / RepoFlag
	cmd.Flags().String("repo", "", "")
	return cmd
}

func TestBranchCheckCmd_Pass_Protected(t *testing.T) {
	cmd := newBranchCheckCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--branch", "main"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected no error for protected branch 'main', got: %v", err)
	}
	if !strings.Contains(out.String(), "protected") {
		t.Fatalf("expected '(protected)' in output, got: %q", out.String())
	}
}

func TestBranchCheckCmd_Pass_PatternMatch(t *testing.T) {
	cmd := newBranchCheckCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	// Use default config patterns (config.Defaults().Branch.Patterns)
	cmd.SetArgs([]string{"--branch", "feat/my-feature"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected no error for valid branch name, got: %v", err)
	}
}

func TestBranchCheckCmd_Fail_NoMatch(t *testing.T) {
	cmd := newBranchCheckCmd()
	errBuf := &bytes.Buffer{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--branch", "INVALID_branch_name", "--patterns", `^(feat|fix)/[a-z]+$`})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-matching branch name")
	}
	if !strings.Contains(err.Error(), "did not match") {
		t.Fatalf("expected 'did not match' in error, got: %v", err)
	}
}

func TestBranchCheckCmd_Quiet(t *testing.T) {
	cmd := newBranchCheckCmd()
	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--branch", "INVALID", "--patterns", `^feat/.+$`, "--quiet"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error in quiet mode for non-matching branch")
	}
	// --quiet suppresses stderr output from our handler
	if errBuf.Len() != 0 {
		t.Fatalf("expected empty stderr in quiet mode, got: %q", errBuf.String())
	}
}

func TestBranchCheckCmd_OverridePatterns(t *testing.T) {
	cmd := newBranchCheckCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	// Custom pattern that matches "jira-123"
	cmd.SetArgs([]string{"--branch", "jira-123", "--patterns", `^jira-[0-9]+$`})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected pass with custom pattern, got: %v", err)
	}
}
