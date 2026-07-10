package chat

import (
	"fmt"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// charsPerToken mirrors aicommit's estimation heuristic — the project
// standard for "roughly how many tokens is this string".
const charsPerToken = 4

// trimHistory shrinks a conversation to roughly budgetTokens while
// keeping it STRUCTURALLY valid: a tool_result may never be orphaned from
// its tool_use (Anthropic rejects such histories outright), so trimming
// happens in two passes that both respect message pairing:
//
//  1. Elide old tool-result CONTENT (keep the message, stub the payload) —
//     tool outputs dominate history bytes and stale ones re-read poorly.
//     The most recent turn is never elided; the model may still be
//     reasoning over it.
//  2. Still over budget → drop whole turns from the front (a turn =
//     everything up to and including an assistant message with no tool
//     calls). The current in-flight turn is always kept.
//
// The input slice is never mutated; trimmed copies are returned.
func trimHistory(msgs []provider.ChatMessage, budgetTokens int) []provider.ChatMessage {
	if budgetTokens <= 0 || estimateTokens(msgs) <= budgetTokens {
		return msgs
	}

	out := make([]provider.ChatMessage, len(msgs))
	copy(out, msgs)

	// Pass 1: elide tool-result payloads outside the last turn.
	lastTurn := lastTurnStart(out)
	for i := 0; i < lastTurn && estimateTokens(out) > budgetTokens; i++ {
		m := out[i]
		if m.Role != "tool" || m.ToolResult == nil || len(m.ToolResult.Content) <= 64 {
			continue
		}
		stub := *m.ToolResult
		stub.Content = fmt.Sprintf("(elided: %d bytes of earlier tool output)", len(stub.Content))
		out[i].ToolResult = &stub
	}

	// Pass 2: drop whole leading turns. cut == lastTurnStart is still
	// valid (drops everything BEFORE the in-flight turn); only cutting
	// INTO the last turn is off-limits.
	for estimateTokens(out) > budgetTokens {
		cut := firstTurnEnd(out)
		if cut <= 0 || cut > lastTurnStart(out) {
			break // only the in-flight turn remains — keep it whole
		}
		out = out[cut:]
	}
	return out
}

// estimateTokens approximates the token weight of a conversation.
func estimateTokens(msgs []provider.ChatMessage) int {
	return sumChars(msgs) / charsPerToken
}

// sumChars totals the raw character weight behind estimateTokens, without
// the final division. trimmedTurnHistory caches this per-segment so a
// cached partial sum can be added to a freshly-scanned segment's sum
// BEFORE dividing — integer division does not distribute over addition
// (sumChars(a)/k + sumChars(b)/k can be less than sumChars(a+b)/k), so
// summing first keeps the cached, incremental estimate bit-exact with a
// single estimateTokens call over the concatenation.
func sumChars(msgs []provider.ChatMessage) int {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Text)
		if m.ToolResult != nil {
			chars += len(m.ToolResult.Content)
		}
		for _, c := range m.ToolCalls {
			chars += len(c.Name) + len(c.Input)
		}
	}
	return chars
}

// tokenBreakdown splits sumChars's total into the two buckets `/tokens`
// reports separately: "history" (user/assistant text and tool-call
// requests) and "tool results" (tool-result payloads) — the same
// per-message classification sumChars already walks, just kept apart
// instead of folded into one number.
func tokenBreakdown(msgs []provider.ChatMessage) (historyChars, toolChars int) {
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolResult != nil {
			toolChars += len(m.ToolResult.Content)
			continue
		}
		historyChars += len(m.Text)
		for _, c := range m.ToolCalls {
			historyChars += len(c.Name) + len(c.Input)
		}
	}
	return
}

// trimmedTurnHistory memoizes trimHistory's treatment of the conversation
// that predates the CURRENT turn, so a multi-round turn (up to
// MaxToolRounds round-trips) doesn't re-scan and re-copy the entire,
// largely-unchanged history from scratch on every round.
//
// trimHistory never touches the in-flight turn — everything from
// lastTurnStart(msgs) onward is always returned verbatim (see its
// docstring) — so once the turn starts, the split between the "older,
// cacheable" prefix and the "current turn" tail (which only grows, round
// by round, as tool calls/results are appended) never moves. forRound
// reuses the cached prefix and only re-scans the (small, this-turn-only)
// tail each round; it falls back to a full, authoritative trimHistory
// pass — and refreshes the cache from it — only when the growing tail
// pushes the running total over budget.
type trimmedTurnHistory struct {
	turnStart int                    // lastTurnStart(history) for the whole turn — fixed once the turn's user message lands
	prefix    []provider.ChatMessage // trimHistory's treatment of history[:turnStart], as of the last (re)computation
	prefixSum int                    // sumChars(prefix), cached alongside it
}

// newTrimmedTurnHistory seeds the cache right after the turn's user
// message has been appended, i.e. when turnStart == lastTurnStart(history).
func newTrimmedTurnHistory(history []provider.ChatMessage, turnStart, budgetTokens int) *trimmedTurnHistory {
	c := &trimmedTurnHistory{turnStart: turnStart}
	c.refresh(history, budgetTokens)
	return c
}

// refresh recomputes the cached prefix from a full, authoritative
// trimHistory pass over the CURRENT history, and returns that pass's
// result directly (so callers needn't re-derive it from the cache).
func (c *trimmedTurnHistory) refresh(history []provider.ChatMessage, budgetTokens int) []provider.ChatMessage {
	full := trimHistory(history, budgetTokens)
	tailLen := len(history) - c.turnStart
	prefixLen := len(full) - tailLen
	if prefixLen < 0 {
		prefixLen = 0 // defensive; trimHistory never shortens the in-flight turn
	}
	c.prefix = full[:prefixLen]
	c.prefixSum = sumChars(c.prefix)
	return full
}

// forRound returns the history to send for the current round.
func (c *trimmedTurnHistory) forRound(history []provider.ChatMessage, budgetTokens int) []provider.ChatMessage {
	if len(history) < c.turnStart {
		return trimHistory(history, budgetTokens) // defensive: turn boundary moved unexpectedly
	}
	tail := history[c.turnStart:]
	if (c.prefixSum+sumChars(tail))/charsPerToken <= budgetTokens {
		out := make([]provider.ChatMessage, 0, len(c.prefix)+len(tail))
		out = append(out, c.prefix...)
		out = append(out, tail...)
		return out
	}
	return c.refresh(history, budgetTokens)
}

// firstTurnEnd returns the index just past the first complete turn (its
// final assistant text message), or 0 when no complete turn exists.
func firstTurnEnd(msgs []provider.ChatMessage) int {
	for i, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) == 0 {
			return i + 1
		}
	}
	return 0
}

// lastTurnStart returns the index of the user message opening the most
// recent turn (0 when there is a single turn).
func lastTurnStart(msgs []provider.ChatMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return 0
}
