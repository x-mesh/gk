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

	// Pass 2: drop whole leading turns.
	for estimateTokens(out) > budgetTokens {
		cut := firstTurnEnd(out)
		if cut <= 0 || cut >= lastTurnStart(out) {
			break // only the in-flight turn remains — keep it whole
		}
		out = out[cut:]
	}
	return out
}

// estimateTokens approximates the token weight of a conversation.
func estimateTokens(msgs []provider.ChatMessage) int {
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
	return chars / charsPerToken
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
