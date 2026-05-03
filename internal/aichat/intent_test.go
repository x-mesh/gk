package aichat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

// ---------------------------------------------------------------------------
// FakeSummarizer — test double for provider.Summarizer
// ---------------------------------------------------------------------------

// fakeSummarizer implements provider.Summarizer for testing.
type fakeSummarizer struct {
	response provider.SummarizeResult
	err      error
	// captured records the last SummarizeInput for assertion.
	captured *provider.SummarizeInput
}

func (f *fakeSummarizer) Summarize(_ context.Context, in provider.SummarizeInput) (provider.SummarizeResult, error) {
	f.captured = &in
	return f.response, f.err
}

// ---------------------------------------------------------------------------
// slowSummarizer — blocks until context is cancelled (for timeout tests)
// ---------------------------------------------------------------------------

type slowSummarizer struct{}

func (s *slowSummarizer) Summarize(ctx context.Context, _ provider.SummarizeInput) (provider.SummarizeResult, error) {
	<-ctx.Done()
	return provider.SummarizeResult{}, ctx.Err()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestParser(sum provider.Summarizer) *IntentParser {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
			"rev-parse --short HEAD":      {Stdout: "abc1234\n"},
			"rev-parse --abbrev-ref @{u}": {Stdout: "origin/main\n"},
			"status --porcelain=v2":       {Stdout: ""},
			"reflog -10 --format=%h %gs":  {Stdout: "abc1234 commit: init\n"},
		},
	}
	return &IntentParser{
		Summarizer: sum,
		Context:    &RepoContextCollector{Runner: r, TokenBudget: 2000},
		Safety:     &SafetyClassifier{},
		Lang:       "en",
		Timeout:    10 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Test: valid response → ExecutionPlan created
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_ValidResponse(t *testing.T) {
	validJSON := `{"commands":[
		{"command":"git add .","description":"stage all changes","dangerous":false},
		{"command":"gk push","description":"push to remote","dangerous":false}
	]}`

	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     validJSON,
			Provider: "fake",
		},
	}
	p := newTestParser(sum)

	plan, err := p.Parse(context.Background(), "push my changes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "git add ." {
		t.Errorf("command[0] = %q, want %q", plan.Commands[0].Command, "git add .")
	}
	if plan.Commands[1].Command != "gk push" {
		t.Errorf("command[1] = %q, want %q", plan.Commands[1].Command, "gk push")
	}

	// Verify Summarizer was called with Kind "do".
	if sum.captured == nil {
		t.Fatal("Summarizer.Summarize was not called")
	}
	if sum.captured.Kind != "do" {
		t.Errorf("SummarizeInput.Kind = %q, want %q", sum.captured.Kind, "do")
	}
}

// ---------------------------------------------------------------------------
// Test: invalid JSON response → error returned
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_InvalidJSON(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     "I'm sorry, I can't do that.",
			Provider: "fake",
		},
	}

	var dbgMessages []string
	p := newTestParser(sum)
	p.Dbg = func(format string, args ...any) {
		dbgMessages = append(dbgMessages, fmt.Sprintf(format, args...))
	}

	plan, err := p.Parse(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
	if plan != nil {
		t.Fatal("expected nil plan for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "failed to parse AI response") {
		t.Errorf("error should mention parse failure, got: %v", err)
	}

	// Verify Dbg was called with the raw response.
	if len(dbgMessages) == 0 {
		t.Error("expected Dbg to be called with raw AI response")
	}
	found := false
	for _, msg := range dbgMessages {
		if strings.Contains(msg, "I'm sorry") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Dbg should contain the raw AI response text")
	}
}

// ---------------------------------------------------------------------------
// Test: response with dangerous commands → Risk fields set
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_DangerousCommands(t *testing.T) {
	dangerousJSON := `{"commands":[
		{"command":"git add .","description":"stage all","dangerous":false},
		{"command":"git push --force","description":"force push","dangerous":true},
		{"command":"git reset --hard HEAD~1","description":"undo last commit","dangerous":true}
	]}`

	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     dangerousJSON,
			Provider: "fake",
		},
	}
	p := newTestParser(sum)

	plan, err := p.Parse(context.Background(), "undo last commit and force push")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Commands) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(plan.Commands))
	}

	// Command 0: git add . → RiskNone
	if plan.Commands[0].Risk != RiskNone {
		t.Errorf("command[0] Risk = %v, want RiskNone", plan.Commands[0].Risk)
	}

	// Command 1: git push --force → RiskHigh
	if plan.Commands[1].Risk != RiskHigh {
		t.Errorf("command[1] Risk = %v, want RiskHigh", plan.Commands[1].Risk)
	}
	if plan.Commands[1].RiskReason == "" {
		t.Error("command[1] RiskReason should not be empty")
	}

	// Command 2: git reset --hard → RiskHigh
	if plan.Commands[2].Risk != RiskHigh {
		t.Errorf("command[2] Risk = %v, want RiskHigh", plan.Commands[2].Risk)
	}
	if plan.Commands[2].RiskReason == "" {
		t.Error("command[2] RiskReason should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Test: timeout → error returned
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_Timeout(t *testing.T) {
	p := &IntentParser{
		Summarizer: &slowSummarizer{},
		Context: &RepoContextCollector{
			Runner: &git.FakeRunner{
				Responses: map[string]git.FakeResponse{
					"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
					"rev-parse --short HEAD":      {Stdout: "abc1234\n"},
					"rev-parse --abbrev-ref @{u}": {Stdout: "origin/main\n"},
					"status --porcelain=v2":       {Stdout: ""},
					"reflog -10 --format=%h %gs":  {Stdout: ""},
				},
			},
			TokenBudget: 2000,
		},
		Safety:  &SafetyClassifier{},
		Lang:    "en",
		Timeout: 50 * time.Millisecond,
	}

	plan, err := p.Parse(context.Background(), "push my changes")
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if plan != nil {
		t.Fatal("expected nil plan on timeout")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error should mention deadline exceeded, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Summarizer error → error propagated
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_SummarizerError(t *testing.T) {
	sum := &fakeSummarizer{
		err: fmt.Errorf("provider unavailable"),
	}
	p := newTestParser(sum)

	plan, err := p.Parse(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error when Summarizer fails")
	}
	if plan != nil {
		t.Fatal("expected nil plan when Summarizer fails")
	}
	if !strings.Contains(err.Error(), "AI provider error") {
		t.Errorf("error should mention AI provider error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: nil Context (no repo context) → still works
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_NilContext(t *testing.T) {
	validJSON := `{"commands":[{"command":"git status","description":"check status"}]}`
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     validJSON,
			Provider: "fake",
		},
	}
	p := &IntentParser{
		Summarizer: sum,
		Context:    nil,
		Safety:     &SafetyClassifier{},
		Lang:       "ko",
		Timeout:    10 * time.Second,
	}

	plan, err := p.Parse(context.Background(), "상태 확인")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(plan.Commands))
	}
}

// ---------------------------------------------------------------------------
// Test: nil Safety → risk fields not set but no panic
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_NilSafety(t *testing.T) {
	validJSON := `{"commands":[{"command":"git push --force","description":"force push","dangerous":true}]}`
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     validJSON,
			Provider: "fake",
		},
	}
	p := &IntentParser{
		Summarizer: sum,
		Context:    nil,
		Safety:     nil,
		Lang:       "en",
		Timeout:    10 * time.Second,
	}

	plan, err := p.Parse(context.Background(), "force push")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Risk should remain at zero value (RiskNone) since Safety is nil.
	if plan.Commands[0].Risk != RiskNone {
		t.Errorf("Risk = %v, want RiskNone (Safety is nil)", plan.Commands[0].Risk)
	}
}

// ---------------------------------------------------------------------------
// Test: markdown-wrapped JSON response → parsed correctly
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_MarkdownWrappedJSON(t *testing.T) {
	mdJSON := "Here is the plan:\n```json\n{\"commands\":[{\"command\":\"gk sync\",\"description\":\"sync branch\"}]}\n```\nDone."
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     mdJSON,
			Provider: "fake",
		},
	}
	p := newTestParser(sum)

	plan, err := p.Parse(context.Background(), "sync my branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "gk sync" {
		t.Errorf("command = %q, want %q", plan.Commands[0].Command, "gk sync")
	}
}

// ---------------------------------------------------------------------------
// Test: Lang is passed through to Summarizer
// ---------------------------------------------------------------------------

func TestIntentParser_Parse_LangPassedThrough(t *testing.T) {
	validJSON := `{"commands":[{"command":"git status","description":"상태 확인"}]}`
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     validJSON,
			Provider: "fake",
		},
	}
	p := newTestParser(sum)
	p.Lang = "ko"

	_, err := p.Parse(context.Background(), "상태 확인")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.captured.Lang != "ko" {
		t.Errorf("SummarizeInput.Lang = %q, want %q", sum.captured.Lang, "ko")
	}
}
