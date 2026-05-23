package cli

import (
	"strings"
	"testing"
)

// Guards the decision-first contract of the status/next advisor: it must ask
// for a single recommendation with reasoning and an alternative, not a menu.
func TestStatusAssistPromptIsDecisionFirst(t *testing.T) {
	p := buildStatusAssistPrompt(statusAssistFacts{Branch: "feat/x"}, "en", "")
	for _, want := range []string{"RECOMMEND", "WHY", "ALTERNATIVE", "decision"} {
		if !strings.Contains(p, want) {
			t.Errorf("status assist prompt missing %q:\n%s", want, p)
		}
	}
}

// renderReviewFindings must surface severity, location, and a fix for each
// finding (the actionable rubric), keyed by verdict.
func TestRenderReviewFindingsRubric(t *testing.T) {
	var b strings.Builder
	renderReviewFindings(&b, reviewFindings{
		Verdict: "changes_requested",
		Summary: "1 issue",
		Findings: []reviewFinding{
			{Severity: "high", Loc: "a.go:10", Issue: "leak", Why: "fd never closed", Fix: "defer f.Close()"},
		},
	})
	out := stripped(b.String())
	for _, want := range []string{"changes requested", "HIGH", "a.go:10", "leak", "why:", "fix:"} {
		if !strings.Contains(out, want) {
			t.Errorf("review render missing %q:\n%s", want, out)
		}
	}
}
