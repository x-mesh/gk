package provider

import (
	"strings"
	"testing"
)

// These guard the "advisory, not summary" contract: if a future edit
// weakens the prompts back into plain summarization, these fail.

func TestSummarizeSystemPromptIsAdvisory(t *testing.T) {
	if !strings.Contains(summarizeSystemPrompt, "ADVISE") {
		t.Errorf("summarize system prompt should be advisory (contain ADVISE):\n%s", summarizeSystemPrompt)
	}
	if !strings.Contains(summarizeSystemPrompt, "mitigation") {
		t.Error("summarize system prompt should require a mitigation/next step for risks")
	}
}

func TestReviewPromptRequestsActionableFindings(t *testing.T) {
	p := buildSummarizeUserPrompt(SummarizeInput{Kind: "review", Diff: "+x", Lang: "en"})
	for _, want := range []string{"ACTIONABLE", "severity", "loc", "fix", `"verdict"`} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt missing %q:\n%s", want, p)
		}
	}
}

func TestPRPromptIsAdvisory(t *testing.T) {
	p := buildSummarizeUserPrompt(SummarizeInput{Kind: "pr", Diff: "+x", Lang: "en"})
	for _, want := range []string{"Reviewer focus", "Risks & mitigations", "Test plan"} {
		if !strings.Contains(p, want) {
			t.Errorf("pr prompt missing %q:\n%s", want, p)
		}
	}
}
