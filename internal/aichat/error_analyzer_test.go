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
// helpers
// ---------------------------------------------------------------------------

// newTestAnalyzer creates an ErrorAnalyzer wired to a FakeSummarizer and
// a FakeRunner that returns typical git context.
func newTestAnalyzer(sum provider.Summarizer) *ErrorAnalyzer {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
			"rev-parse --short HEAD":      {Stdout: "abc1234\n"},
			"rev-parse --abbrev-ref @{u}": {Stdout: "origin/main\n"},
			"status --porcelain=v2":       {Stdout: ""},
			"reflog -10 --format=%h %gs":  {Stdout: "abc1234 commit: init\ndef5678 checkout: moving from dev to main\n"},
		},
	}
	return &ErrorAnalyzer{
		Summarizer: sum,
		Context:    &RepoContextCollector{Runner: r, TokenBudget: 2000},
		Lang:       "en",
		Timeout:    10 * time.Second,
	}
}

// newTestAnalyzerNoReflog creates an ErrorAnalyzer whose FakeRunner returns
// no reflog entries (simulating a fresh repo or expired reflog).
func newTestAnalyzerNoReflog(sum provider.Summarizer) *ErrorAnalyzer {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
			"rev-parse --short HEAD":      {Stdout: "abc1234\n"},
			"rev-parse --abbrev-ref @{u}": {Stdout: "origin/main\n"},
			"status --porcelain=v2":       {Stdout: ""},
			"reflog -10 --format=%h %gs":  {Stdout: ""},
		},
	}
	return &ErrorAnalyzer{
		Summarizer: sum,
		Context:    &RepoContextCollector{Runner: r, TokenBudget: 2000},
		Lang:       "en",
		Timeout:    10 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Test: valid error message → 3-section output (Cause, Solution, Prevention)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_DiagnoseError_ValidMessage(t *testing.T) {
	threeSectionResponse := `## Cause
The push was rejected because the remote contains work that you do not have locally.

## Solution
Run the following commands:
  gk pull
  gk push

## Prevention
Always run gk sync before pushing to keep your branch up to date.`

	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     threeSectionResponse,
			Provider: "fake",
		},
	}
	a := newTestAnalyzer(sum)

	result, err := a.DiagnoseError(context.Background(), "error: failed to push some refs to 'origin'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the 3-section structure is present.
	if !strings.Contains(result, "Cause") {
		t.Error("result should contain 'Cause' section")
	}
	if !strings.Contains(result, "Solution") {
		t.Error("result should contain 'Solution' section")
	}
	if !strings.Contains(result, "Prevention") {
		t.Error("result should contain 'Prevention' section")
	}

	// Verify Summarizer was called with Kind "explain".
	if sum.captured == nil {
		t.Fatal("Summarizer.Summarize was not called")
	}
	if sum.captured.Kind != "explain" {
		t.Errorf("SummarizeInput.Kind = %q, want %q", sum.captured.Kind, "explain")
	}
	if sum.captured.Lang != "en" {
		t.Errorf("SummarizeInput.Lang = %q, want %q", sum.captured.Lang, "en")
	}
}

// ---------------------------------------------------------------------------
// Test: empty error message → guidance message (Requirement 3.6)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_DiagnoseError_EmptyMessage(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	a := newTestAnalyzer(sum)

	result, err := a.DiagnoseError(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return guidance, not call the AI.
	if sum.captured != nil {
		t.Error("Summarizer should NOT be called for empty error message")
	}

	// Verify guidance content.
	if !strings.Contains(result, "merge conflict") {
		t.Error("guidance should mention 'merge conflict'")
	}
	if !strings.Contains(result, "gk explain --last") {
		t.Error("guidance should suggest 'gk explain --last'")
	}
}

// ---------------------------------------------------------------------------
// Test: whitespace-only error message → guidance message
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_DiagnoseError_WhitespaceOnly(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	a := newTestAnalyzer(sum)

	result, err := a.DiagnoseError(context.Background(), "   \n\t  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sum.captured != nil {
		t.Error("Summarizer should NOT be called for whitespace-only error message")
	}
	if !strings.Contains(result, "gk explain --last") {
		t.Error("guidance should suggest 'gk explain --last'")
	}
}

// ---------------------------------------------------------------------------
// Test: empty error message in Korean → Korean guidance
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_DiagnoseError_EmptyMessage_Korean(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	a := newTestAnalyzer(sum)
	a.Lang = "ko"

	result, err := a.DiagnoseError(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "에러 메시지가 비어있습니다") {
		t.Error("Korean guidance should contain '에러 메시지가 비어있습니다'")
	}
	if !strings.Contains(result, "gk explain --last") {
		t.Error("Korean guidance should suggest 'gk explain --last'")
	}
}

// ---------------------------------------------------------------------------
// Test: --last mode → reflog-based explanation (Requirement 4.1, 4.2)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_ExplainLast_WithReflog(t *testing.T) {
	stepByStepResponse := `The most recent command was a checkout from dev to main.

Step 1: Git updated HEAD to point to the main branch.
Step 2: The index was updated to match the main branch's tree.
Step 3: The working tree was updated to reflect the new branch state.

Changes:
- HEAD: moved from dev to main
- Index: updated to main's tree
- Working tree: files updated to match main`

	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     stepByStepResponse,
			Provider: "fake",
		},
	}
	a := newTestAnalyzer(sum)

	result, err := a.ExplainLast(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify step-by-step explanation is returned.
	if !strings.Contains(result, "Step 1") {
		t.Error("result should contain step-by-step explanation")
	}
	if !strings.Contains(result, "HEAD") {
		t.Error("result should mention HEAD changes")
	}

	// Verify Summarizer was called with Kind "explain-last".
	if sum.captured == nil {
		t.Fatal("Summarizer.Summarize was not called")
	}
	if sum.captured.Kind != "explain-last" {
		t.Errorf("SummarizeInput.Kind = %q, want %q", sum.captured.Kind, "explain-last")
	}
}

// ---------------------------------------------------------------------------
// Test: no reflog → accuracy limitation notice (Requirement 4.5)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_ExplainLast_NoReflog(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	a := newTestAnalyzerNoReflog(sum)

	result, err := a.ExplainLast(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return notice, not call the AI.
	if sum.captured != nil {
		t.Error("Summarizer should NOT be called when reflog is empty")
	}

	// Verify accuracy limitation notice.
	if !strings.Contains(result, "reflog") {
		t.Error("notice should mention 'reflog'")
	}
	if !strings.Contains(result, "limited accuracy") || !strings.Contains(result, "cannot") {
		t.Error("notice should mention accuracy limitations")
	}
}

// ---------------------------------------------------------------------------
// Test: no reflog in Korean → Korean notice
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_ExplainLast_NoReflog_Korean(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	a := newTestAnalyzerNoReflog(sum)
	a.Lang = "ko"

	result, err := a.ExplainLast(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "reflog") {
		t.Error("Korean notice should mention 'reflog'")
	}
	if !strings.Contains(result, "정확도가 제한적") {
		t.Error("Korean notice should mention accuracy limitations")
	}
}

// ---------------------------------------------------------------------------
// Test: Summarizer error → error propagated (DiagnoseError)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_DiagnoseError_SummarizerError(t *testing.T) {
	sum := &fakeSummarizer{
		err: fmt.Errorf("provider unavailable"),
	}
	a := newTestAnalyzer(sum)

	result, err := a.DiagnoseError(context.Background(), "fatal: not a git repository")
	if err == nil {
		t.Fatal("expected error when Summarizer fails")
	}
	if result != "" {
		t.Errorf("expected empty result on error, got %q", result)
	}
	if !strings.Contains(err.Error(), "AI provider error") {
		t.Errorf("error should mention AI provider error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Summarizer error → error propagated (ExplainLast)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_ExplainLast_SummarizerError(t *testing.T) {
	sum := &fakeSummarizer{
		err: fmt.Errorf("rate limit exceeded"),
	}
	a := newTestAnalyzer(sum)

	result, err := a.ExplainLast(context.Background())
	if err == nil {
		t.Fatal("expected error when Summarizer fails")
	}
	if result != "" {
		t.Errorf("expected empty result on error, got %q", result)
	}
	if !strings.Contains(err.Error(), "AI provider error") {
		t.Errorf("error should mention AI provider error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: timeout → error returned (DiagnoseError)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_DiagnoseError_Timeout(t *testing.T) {
	a := &ErrorAnalyzer{
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
		Lang:    "en",
		Timeout: 50 * time.Millisecond,
	}

	result, err := a.DiagnoseError(context.Background(), "fatal: not a git repository")
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if result != "" {
		t.Errorf("expected empty result on timeout, got %q", result)
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error should mention deadline exceeded, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: timeout → error returned (ExplainLast)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_ExplainLast_Timeout(t *testing.T) {
	a := &ErrorAnalyzer{
		Summarizer: &slowSummarizer{},
		Context: &RepoContextCollector{
			Runner: &git.FakeRunner{
				Responses: map[string]git.FakeResponse{
					"rev-parse --abbrev-ref HEAD": {Stdout: "main\n"},
					"rev-parse --short HEAD":      {Stdout: "abc1234\n"},
					"rev-parse --abbrev-ref @{u}": {Stdout: "origin/main\n"},
					"status --porcelain=v2":       {Stdout: ""},
					"reflog -10 --format=%h %gs":  {Stdout: "abc1234 commit: init\n"},
				},
			},
			TokenBudget: 2000,
		},
		Lang:    "en",
		Timeout: 50 * time.Millisecond,
	}

	result, err := a.ExplainLast(context.Background())
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if result != "" {
		t.Errorf("expected empty result on timeout, got %q", result)
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error should mention deadline exceeded, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: nil Context → still works (DiagnoseError)
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_DiagnoseError_NilContext(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     "Cause: ...\nSolution: ...\nPrevention: ...",
			Provider: "fake",
		},
	}
	a := &ErrorAnalyzer{
		Summarizer: sum,
		Context:    nil,
		Lang:       "en",
		Timeout:    10 * time.Second,
	}

	result, err := a.DiagnoseError(context.Background(), "fatal: not a git repository")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

// ---------------------------------------------------------------------------
// Test: nil Context → ExplainLast returns no-reflog notice
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_ExplainLast_NilContext(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	a := &ErrorAnalyzer{
		Summarizer: sum,
		Context:    nil,
		Lang:       "en",
		Timeout:    10 * time.Second,
	}

	result, err := a.ExplainLast(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// nil Context means no reflog → should return notice.
	if sum.captured != nil {
		t.Error("Summarizer should NOT be called when Context is nil")
	}
	if !strings.Contains(result, "reflog") {
		t.Error("notice should mention 'reflog'")
	}
}

// ---------------------------------------------------------------------------
// Test: Lang is passed through to Summarizer
// ---------------------------------------------------------------------------

func TestErrorAnalyzer_DiagnoseError_LangPassedThrough(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     "원인: ...\n해결: ...\n예방: ...",
			Provider: "fake",
		},
	}
	a := newTestAnalyzer(sum)
	a.Lang = "ko"

	_, err := a.DiagnoseError(context.Background(), "error: push rejected")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.captured.Lang != "ko" {
		t.Errorf("SummarizeInput.Lang = %q, want %q", sum.captured.Lang, "ko")
	}
}
