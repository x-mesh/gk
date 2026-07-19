package cli

import (
	"strings"
	"testing"
)

// Guards the interpretation-first contract of the status/next advisor. gk
// already computes recommended_commands with their reasons, so a prompt that
// leads with "pick one and justify it" buys a translation of a table gk
// already had. The prompt must lead with WHAT the change is — the one thing
// only the model can supply — and demote the command to a tail.
func TestStatusAssistPromptIsInterpretationFirst(t *testing.T) {
	p := buildStatusAssistPrompt(statusAssistFacts{Branch: "feat/x"}, "en", "")
	for _, want := range []string{"WHAT:", "WATCH:", "NEXT:"} {
		if !strings.Contains(p, want) {
			t.Errorf("status assist prompt missing %q:\n%s", want, p)
		}
	}
	if i, j := strings.Index(p, "WHAT:"), strings.Index(p, "NEXT:"); i > j {
		t.Errorf("WHAT must be asked for before NEXT (got WHAT at %d, NEXT at %d)", i, j)
	}
	// The failure mode that made the old output worthless: restating counts
	// the deterministic status line already prints.
	if !strings.Contains(p, "NEVER restate the file/line counts") {
		t.Errorf("prompt must forbid restating counts as the answer:\n%s", p)
	}
}

// The change shape only ships when there is a shape to describe, and its
// guardrails must ship with it — hunk_context is a locator that reads like a
// description, so the rule against mining it for purpose is not optional.
func TestStatusAssistPromptGuardsHunkContext(t *testing.T) {
	bare := buildStatusAssistPrompt(statusAssistFacts{Branch: "feat/x"}, "en", "")
	if strings.Contains(bare, "hunk_context") {
		t.Errorf("no changes → prompt must not mention hunk_context:\n%s", bare)
	}
	shaped := buildStatusAssistPrompt(statusAssistFacts{
		Branch:  "feat/x",
		Changes: []statusAssistChange{{Path: "a.go", Added: 3}},
	}, "en", "")
	for _, want := range []string{"hunk_context", "LOCATION only", "NEVER say"} {
		if !strings.Contains(shaped, want) {
			t.Errorf("shaped prompt missing %q:\n%s", want, shaped)
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
