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

// newTestQAEngine creates a QAEngine wired to a FakeSummarizer and
// a FakeRunner that returns typical git context including branch list.
func newTestQAEngine(sum provider.Summarizer) *QAEngine {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD":  {Stdout: "feature/auth\n"},
			"rev-parse --short HEAD":       {Stdout: "abc1234\n"},
			"rev-parse --abbrev-ref @{u}":  {Stdout: "origin/feature/auth\n"},
			"status --porcelain=v2":        {Stdout: ""},
			"reflog -10 --format=%h %gs":   {Stdout: "abc1234 commit: add login\ndef5678 checkout: moving from main to feature/auth\n"},
			"branch --format=%(refname:short)": {Stdout: "main\nfeature/auth\ndev\n"},
		},
	}
	return &QAEngine{
		Summarizer: sum,
		Context:    &RepoContextCollector{Runner: r, TokenBudget: 2000},
		Lang:       "en",
		Timeout:    10 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Test: valid question → context-based answer (Requirement 5.1)
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_ValidQuestion(t *testing.T) {
	answerText := `To merge feature/auth into main, run:

  gk sync
  git checkout main
  gk merge feature/auth

Your current branch is feature/auth (abc1234).

Related commands:
- gk sync — sync your branch with the base
- gk merge — merge a branch with prechecks`

	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     answerText,
			Provider: "fake",
		},
	}
	q := newTestQAEngine(sum)

	result, err := q.Answer(context.Background(), "How do I merge my branch into main?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the answer contains branch names and commands.
	if !strings.Contains(result, "feature/auth") {
		t.Error("answer should contain actual branch name 'feature/auth'")
	}
	if !strings.Contains(result, "gk") {
		t.Error("answer should contain gk command suggestions")
	}

	// Verify Summarizer was called with Kind "ask".
	if sum.captured == nil {
		t.Fatal("Summarizer.Summarize was not called")
	}
	if sum.captured.Kind != "ask" {
		t.Errorf("SummarizeInput.Kind = %q, want %q", sum.captured.Kind, "ask")
	}
	if sum.captured.Lang != "en" {
		t.Errorf("SummarizeInput.Lang = %q, want %q", sum.captured.Lang, "en")
	}
}

// ---------------------------------------------------------------------------
// Test: non-git question → guidance message (Requirement 5.8)
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_NonGitQuestion(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	q := newTestQAEngine(sum)

	result, err := q.Answer(context.Background(), "What's the weather like today?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return guidance, not call the AI.
	if sum.captured != nil {
		t.Error("Summarizer should NOT be called for non-git question")
	}

	// Verify guidance content.
	if !strings.Contains(result, "git/gk") {
		t.Error("guidance should mention 'git/gk'")
	}
	if !strings.Contains(result, "gk do") || !strings.Contains(result, "gk ask") {
		t.Error("guidance should suggest gk commands")
	}
}

// ---------------------------------------------------------------------------
// Test: non-git question in Korean → Korean guidance (Requirement 5.8)
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_NonGitQuestion_Korean(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	q := newTestQAEngine(sum)
	q.Lang = "ko"

	result, err := q.Answer(context.Background(), "오늘 날씨 어때?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sum.captured != nil {
		t.Error("Summarizer should NOT be called for non-git question")
	}
	if !strings.Contains(result, "git/gk와 관련이 없는") {
		t.Error("Korean guidance should mention that the question is not git-related")
	}
}

// ---------------------------------------------------------------------------
// Test: Korean question handled (Requirement 5.2)
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_KoreanQuestion(t *testing.T) {
	answerText := `현재 브랜치 feature/auth에서 main으로 머지하려면:

  gk sync
  git checkout main
  gk merge feature/auth

관련 명령어:
- gk sync — 브랜치 동기화
- gk merge — 브랜치 머지`

	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     answerText,
			Provider: "fake",
		},
	}
	q := newTestQAEngine(sum)
	q.Lang = "ko"

	result, err := q.Answer(context.Background(), "현재 브랜치에서 main으로 어떻게 머지하나요?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "feature/auth") {
		t.Error("answer should contain actual branch name")
	}

	// Verify Lang is passed through.
	if sum.captured == nil {
		t.Fatal("Summarizer.Summarize was not called")
	}
	if sum.captured.Lang != "ko" {
		t.Errorf("SummarizeInput.Lang = %q, want %q", sum.captured.Lang, "ko")
	}
}

// ---------------------------------------------------------------------------
// Test: English question handled (Requirement 5.2)
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_EnglishQuestion(t *testing.T) {
	answerText := `To undo your last commit while keeping changes:

  git reset --soft HEAD~1

Related commands:
- gk commit — create a new commit`

	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     answerText,
			Provider: "fake",
		},
	}
	q := newTestQAEngine(sum)
	q.Lang = "en"

	result, err := q.Answer(context.Background(), "How do I undo my last commit?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "reset") {
		t.Error("answer should contain relevant git command")
	}
	if sum.captured.Lang != "en" {
		t.Errorf("SummarizeInput.Lang = %q, want %q", sum.captured.Lang, "en")
	}
}

// ---------------------------------------------------------------------------
// Test: Summarizer error → error propagated
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_SummarizerError(t *testing.T) {
	sum := &fakeSummarizer{
		err: fmt.Errorf("provider unavailable"),
	}
	q := newTestQAEngine(sum)

	result, err := q.Answer(context.Background(), "How do I rebase?")
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
// Test: timeout → error returned
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_Timeout(t *testing.T) {
	q := &QAEngine{
		Summarizer: &slowSummarizer{},
		Context: &RepoContextCollector{
			Runner: &git.FakeRunner{
				Responses: map[string]git.FakeResponse{
					"rev-parse --abbrev-ref HEAD":      {Stdout: "main\n"},
					"rev-parse --short HEAD":           {Stdout: "abc1234\n"},
					"rev-parse --abbrev-ref @{u}":      {Stdout: "origin/main\n"},
					"status --porcelain=v2":            {Stdout: ""},
					"reflog -10 --format=%h %gs":       {Stdout: ""},
					"branch --format=%(refname:short)": {Stdout: "main\n"},
				},
			},
			TokenBudget: 2000,
		},
		Lang:    "en",
		Timeout: 50 * time.Millisecond,
	}

	result, err := q.Answer(context.Background(), "How do I rebase?")
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
// Test: empty question → guidance message
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_EmptyQuestion(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	q := newTestQAEngine(sum)

	result, err := q.Answer(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return guidance, not call the AI.
	if sum.captured != nil {
		t.Error("Summarizer should NOT be called for empty question")
	}

	if !strings.Contains(result, "question") || !strings.Contains(result, "gk ask") {
		t.Error("guidance should mention how to use gk ask")
	}
}

// ---------------------------------------------------------------------------
// Test: whitespace-only question → guidance message
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_WhitespaceOnly(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	q := newTestQAEngine(sum)

	result, err := q.Answer(context.Background(), "   \n\t  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sum.captured != nil {
		t.Error("Summarizer should NOT be called for whitespace-only question")
	}
	if !strings.Contains(result, "gk ask") {
		t.Error("guidance should suggest 'gk ask'")
	}
}

// ---------------------------------------------------------------------------
// Test: empty question in Korean → Korean guidance
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_EmptyQuestion_Korean(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{Text: "should not be called"},
	}
	q := newTestQAEngine(sum)
	q.Lang = "ko"

	result, err := q.Answer(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "질문이 비어있습니다") {
		t.Error("Korean guidance should contain '질문이 비어있습니다'")
	}
}

// ---------------------------------------------------------------------------
// Test: nil Context → still works (Requirement 5.1)
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_NilContext(t *testing.T) {
	answerText := `To rebase your branch onto main:

  git rebase main

Related commands:
- gk sync — sync your branch`

	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     answerText,
			Provider: "fake",
		},
	}
	q := &QAEngine{
		Summarizer: sum,
		Context:    nil,
		Lang:       "en",
		Timeout:    10 * time.Second,
	}

	result, err := q.Answer(context.Background(), "How do I rebase onto main?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	if !strings.Contains(result, "rebase") {
		t.Error("answer should contain 'rebase'")
	}

	// Verify Summarizer was called.
	if sum.captured == nil {
		t.Fatal("Summarizer.Summarize was not called")
	}
	if sum.captured.Kind != "ask" {
		t.Errorf("SummarizeInput.Kind = %q, want %q", sum.captured.Kind, "ask")
	}
}

// ---------------------------------------------------------------------------
// Test: Lang is passed through to Summarizer
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_LangPassedThrough(t *testing.T) {
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     "답변입니다.",
			Provider: "fake",
		},
	}
	q := newTestQAEngine(sum)
	q.Lang = "ko"

	_, err := q.Answer(context.Background(), "rebase란 무엇인가요?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.captured.Lang != "ko" {
		t.Errorf("SummarizeInput.Lang = %q, want %q", sum.captured.Lang, "ko")
	}
}

// ---------------------------------------------------------------------------
// Test: CollectForQuestion is called (not just Collect)
// ---------------------------------------------------------------------------

func TestQAEngine_Answer_UsesCollectForQuestion(t *testing.T) {
	r := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --abbrev-ref HEAD":      {Stdout: "main\n"},
			"rev-parse --short HEAD":           {Stdout: "abc1234\n"},
			"rev-parse --abbrev-ref @{u}":      {Stdout: "origin/main\n"},
			"status --porcelain=v2":            {Stdout: ""},
			"reflog -10 --format=%h %gs":       {Stdout: "abc1234 commit: init\n"},
			"branch --format=%(refname:short)": {Stdout: "main\ndev\nfeature/x\n"},
		},
	}
	sum := &fakeSummarizer{
		response: provider.SummarizeResult{
			Text:     "Answer with branches.",
			Provider: "fake",
		},
	}
	q := &QAEngine{
		Summarizer: sum,
		Context:    &RepoContextCollector{Runner: r, TokenBudget: 2000},
		Lang:       "en",
		Timeout:    10 * time.Second,
	}

	_, err := q.Answer(context.Background(), "What branches do I have?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify that the branch list command was called (CollectForQuestion).
	branchCalled := false
	for _, call := range r.Calls {
		key := strings.Join(call.Args, " ")
		if key == "branch --format=%(refname:short)" {
			branchCalled = true
			break
		}
	}
	if !branchCalled {
		t.Error("CollectForQuestion should call 'git branch' for branch list")
	}
}
