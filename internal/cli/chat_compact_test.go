package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/chat"
)

// fakeCompactCaller is a provider.ToolCaller that ALSO implements
// provider.Summarizer — engine.Caller.(provider.Summarizer) is exactly how
// handleChatCompact reaches the session's own provider (see
// remoteGuardedCaller.Summarize), so tests need a double satisfying both.
type fakeCompactCaller struct {
	sumResult provider.SummarizeResult
	sumErr    error
	sumCalls  int
}

func (f *fakeCompactCaller) ChatWithTools(context.Context, provider.ChatInput) (provider.ChatResult, error) {
	return provider.ChatResult{}, nil
}

func (f *fakeCompactCaller) Summarize(_ context.Context, _ provider.SummarizeInput) (provider.SummarizeResult, error) {
	f.sumCalls++
	if f.sumErr != nil {
		return provider.SummarizeResult{}, f.sumErr
	}
	return f.sumResult, nil
}

func chatTurnHistory(n int) []provider.ChatMessage {
	var msgs []provider.ChatMessage
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			provider.ChatMessage{Role: "user", Text: "q"},
			provider.ChatMessage{Role: "assistant", Text: "a"},
		)
	}
	return msgs
}

// TestHandleChatCompactUnsupportedProvider confirms a plain ToolCaller
// that does NOT implement provider.Summarizer degrades to an informative
// message instead of a panic on the failed type assertion.
func TestHandleChatCompactUnsupportedProvider(t *testing.T) {
	cmd, out, _ := chatTestCmd(t)
	engine := &chat.Engine{Caller: &chatScriptedCaller{}}
	engine.LoadHistory(chatTurnHistory(5))

	handleChatCompact(cmd, engine, "en", false)

	if !strings.Contains(out.String(), "does not support") {
		t.Errorf("output = %q, want an unsupported-provider message", out.String())
	}
}

// TestHandleChatCompactNothingToFold confirms a conversation at or below
// the keep-turns threshold reports "nothing to compact" and never calls
// the summarizer.
func TestHandleChatCompactNothingToFold(t *testing.T) {
	cmd, out, _ := chatTestCmd(t)
	caller := &fakeCompactCaller{sumErr: context.Canceled} // would fail loudly if called
	engine := &chat.Engine{Caller: caller}
	engine.LoadHistory(chatTurnHistory(2)) // == compactKeepTurns

	handleChatCompact(cmd, engine, "en", false)

	if caller.sumCalls != 0 {
		t.Errorf("summarizer called %d times, want 0", caller.sumCalls)
	}
	if !strings.Contains(out.String(), "not enough history") {
		t.Errorf("output = %q, want a not-enough-history message", out.String())
	}
}

// TestHandleChatCompactSuccess drives the happy path end to end through
// the CLI layer: engine.Caller is type-asserted to provider.Summarizer,
// Engine.Compact folds the older turns, and the confirmation line reports
// turn counts and a token before/after.
func TestHandleChatCompactSuccess(t *testing.T) {
	cmd, out, _ := chatTestCmd(t)
	caller := &fakeCompactCaller{sumResult: provider.SummarizeResult{Text: "a dense digest", Model: "m", TokensUsed: 12}}
	engine := &chat.Engine{Caller: caller, HistoryBudget: 1_000_000}
	engine.LoadHistory(chatTurnHistory(5))

	handleChatCompact(cmd, engine, "en", false)

	if caller.sumCalls != 1 {
		t.Fatalf("summarizer called %d times, want 1", caller.sumCalls)
	}
	if !strings.Contains(out.String(), "compacted") {
		t.Errorf("output = %q, want a confirmation mentioning the fold", out.String())
	}
	// The fold produces a synthetic user intro followed by the assistant
	// summary, not a bare assistant message: Anthropic's Messages API
	// rejects a history whose first message is assistant (and rejects two
	// consecutive same-role messages), so a lone summary at index 0 broke
	// every post-compact round on that provider. See compactSummaryMessages.
	h := engine.History()
	if len(h) < 2 {
		t.Fatalf("engine history after compact = %+v, want at least the intro+summary pair", h)
	}
	if h[0].Role != "user" {
		t.Errorf("history[0].Role = %q, want user — Anthropic rejects an assistant-first history", h[0].Role)
	}
	if h[1].Role != "assistant" || !strings.Contains(h[1].Text, "a dense digest") {
		t.Errorf("history[1] = %+v, want the assistant summary carrying the digest", h[1])
	}
}

// TestChatBudgetWarningLine pins the 80%-threshold gate: no budget
// configured or usage below 80% must produce no warning, and usage at or
// above 80% must produce a one-line, non-empty warning mentioning both
// the percentage and /compact.
func TestChatBudgetWarningLine(t *testing.T) {
	// No budget configured at all.
	e := &chat.Engine{}
	e.LoadHistory(chatTurnHistory(50))
	if got := chatBudgetWarningLine(e, false); got != "" {
		t.Errorf("no budget: warning = %q, want empty", got)
	}

	// Budget configured, usage well below 80%.
	low := &chat.Engine{HistoryBudget: 100_000}
	low.LoadHistory([]provider.ChatMessage{{Role: "user", Text: strings.Repeat("x", 40)}})
	if got := chatBudgetWarningLine(low, false); got != "" {
		t.Errorf("low usage: warning = %q, want empty", got)
	}

	// Budget configured, usage at 100% (well past the 80% threshold).
	high := &chat.Engine{HistoryBudget: 100}
	high.LoadHistory([]provider.ChatMessage{{Role: "user", Text: strings.Repeat("x", 400)}}) // 400/4=100 tok
	got := chatBudgetWarningLine(high, false)
	if got == "" {
		t.Fatal("high usage: want a non-empty warning")
	}
	if !strings.Contains(got, "/compact") {
		t.Errorf("warning = %q, want it to mention /compact", got)
	}
}

// TestPrintChatTokens smoke-tests the /tokens report: it must render all
// three composition buckets and the budget line when a budget is set.
func TestPrintChatTokens(t *testing.T) {
	_, out, _ := chatTestCmd(t)
	e := &chat.Engine{SystemPrompt: strings.Repeat("s", 400), HistoryBudget: 1000}
	e.LoadHistory([]provider.ChatMessage{
		{Role: "user", Text: strings.Repeat("u", 40)},
		{Role: "tool", ToolResult: &provider.ToolResult{Content: strings.Repeat("t", 40)}},
	})

	printChatTokens(out, e, false)
	s := out.String()
	for _, want := range []string{"system prompt", "history", "tool results", "history budget"} {
		if !strings.Contains(s, want) {
			t.Errorf("/tokens output missing %q:\n%s", want, s)
		}
	}
}

// TestPrintChatTokensNoBudget confirms the no-budget branch reports
// trimming as disabled instead of a bogus percentage.
func TestPrintChatTokensNoBudget(t *testing.T) {
	_, out, _ := chatTestCmd(t)
	e := &chat.Engine{}
	printChatTokens(out, e, false)
	if !strings.Contains(out.String(), "unset") {
		t.Errorf("output = %q, want it to report the budget as unset", out.String())
	}
}
