package chat

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// TestEngineTokenUsageBreakdown pins the per-bucket char/token split
// `/tokens` renders: system prompt, history (user/assistant text + tool
// call requests), and tool results are counted separately, and UsedTokens
// is the authoritative estimateTokens(history) figure (not a re-derivation
// from the split buckets, which can round differently — see the
// TokenUsageReport docstring).
func TestEngineTokenUsageBreakdown(t *testing.T) {
	e := &Engine{SystemPrompt: strings.Repeat("s", 40), HistoryBudget: 1000}
	e.LoadHistory([]provider.ChatMessage{
		{Role: "user", Text: strings.Repeat("u", 20)},
		{Role: "assistant", Text: strings.Repeat("a", 20), ToolCalls: []provider.ToolCall{
			{Name: "git_log", Input: []byte(`{"n":1}`)}, // 7+7=14 chars
		}},
		{Role: "tool", ToolResult: &provider.ToolResult{Content: strings.Repeat("t", 100)}},
	})

	r := e.TokenUsage()
	if r.SystemChars != 40 || r.SystemTokens != 10 {
		t.Errorf("system = %d chars / %d tok, want 40/10", r.SystemChars, r.SystemTokens)
	}
	wantHistChars := 20 + 20 + len("git_log") + len(`{"n":1}`)
	if r.HistoryChars != wantHistChars {
		t.Errorf("history chars = %d, want %d", r.HistoryChars, wantHistChars)
	}
	if r.ToolChars != 100 {
		t.Errorf("tool chars = %d, want 100", r.ToolChars)
	}
	if r.UsedTokens != estimateTokens(e.History()) {
		t.Errorf("UsedTokens = %d, want estimateTokens(history) = %d", r.UsedTokens, estimateTokens(e.History()))
	}
	if r.BudgetTokens != 1000 {
		t.Errorf("BudgetTokens = %d, want 1000 (Engine.HistoryBudget)", r.BudgetTokens)
	}
}

// TestTokenUsageReportPercent covers Percent()'s two edge behaviors: no
// budget configured must read as 0 (never mistaken for "100% headroom" or
// treated as a real ratio), and a configured budget divides normally.
func TestTokenUsageReportPercent(t *testing.T) {
	noBudget := TokenUsageReport{UsedTokens: 500, BudgetTokens: 0}
	if got := noBudget.Percent(); got != 0 {
		t.Errorf("Percent() with no budget = %v, want 0", got)
	}
	half := TokenUsageReport{UsedTokens: 50, BudgetTokens: 100}
	if got := half.Percent(); got != 0.5 {
		t.Errorf("Percent() = %v, want 0.5", got)
	}
}
