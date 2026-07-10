package chat

// TokenUsageReport breaks down `/tokens`' context-composition display:
// chars and chars/4 estimates for the system prompt, the "history" bucket
// (user/assistant text plus tool-call requests), and the "tool results"
// bucket, plus the history-budget headroom trimHistory enforces.
//
// The system prompt is deliberately kept OUT of UsedTokens/BudgetTokens:
// HistoryBudget/trimHistory only ever operate on the replayed message
// slice (see estimateTokens's own docstring — it never takes SystemPrompt
// as an argument), so folding the system prompt into the budget number
// would let a user read "80% of budget used" as covering something it
// structurally never has.
type TokenUsageReport struct {
	SystemChars, SystemTokens   int
	HistoryChars, HistoryTokens int
	ToolChars, ToolTokens       int
	// UsedTokens is estimateTokens(history) — the exact figure trimHistory
	// compares against BudgetTokens (HistoryTokens+ToolTokens approximates
	// it for display, but per-bucket division can round differently than
	// dividing the combined char sum once; UsedTokens is the authoritative
	// number, the per-bucket split is composition detail).
	UsedTokens int
	// BudgetTokens is Engine.HistoryBudget as configured; 0 means no
	// budget (trimming disabled), matching trimHistory's own contract.
	BudgetTokens int
}

// Percent returns UsedTokens/BudgetTokens in [0,1]. No budget configured
// (BudgetTokens <= 0) reports 0 — "no budget" must never read as "100%
// available" nor "0% used" being mistaken for "budget exhausted"; callers
// gate on BudgetTokens > 0 before treating this as a real percentage.
func (r TokenUsageReport) Percent() float64 {
	if r.BudgetTokens <= 0 {
		return 0
	}
	return float64(r.UsedTokens) / float64(r.BudgetTokens)
}

// TokenUsage computes the current context composition — cheap, pure
// arithmetic over e.history/e.SystemPrompt, safe to call as often as
// `/tokens` or the end-of-turn 80%-budget warning need.
func (e *Engine) TokenUsage() TokenUsageReport {
	histChars, toolChars := tokenBreakdown(e.history)
	return TokenUsageReport{
		SystemChars:   len(e.SystemPrompt),
		SystemTokens:  len(e.SystemPrompt) / charsPerToken,
		HistoryChars:  histChars,
		HistoryTokens: histChars / charsPerToken,
		ToolChars:     toolChars,
		ToolTokens:    toolChars / charsPerToken,
		UsedTokens:    estimateTokens(e.history),
		BudgetTokens:  e.HistoryBudget,
	}
}
