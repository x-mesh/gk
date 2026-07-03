package provider

import (
	"context"
	"encoding/json"
)

// ToolCaller is an optional capability that providers may implement when
// their API supports native multi-turn tool calling (Anthropic Messages
// `tools`/`tool_use`, OpenAI Chat Completions `tools`/`tool_calls`).
// Callers detect support via Go type assertion: p.(ToolCaller). This keeps
// the core Provider interface stable — CLI-based adapters (gemini/qwen/kiro)
// are unaffected and simply never satisfy the assertion, mirroring how
// Summarizer and ConflictResolver gate their surfaces.
//
// A ToolCaller performs exactly ONE model round-trip per call: the caller
// owns the agentic loop (dispatching requested tools, appending results,
// re-invoking). Keeping the loop out of the provider means round caps,
// timeouts, and repeat detection live in one engine, not per vendor.
type ToolCaller interface {
	ChatWithTools(ctx context.Context, in ChatInput) (ChatResult, error)
}

// ToolSpec declares one tool the model may call. The schema travels
// verbatim to the vendor API (Anthropic `input_schema`, OpenAI
// `function.parameters`), so it must be a valid JSON Schema object.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ChatMessage is one turn in a tool-calling conversation. Exactly one of
// the content forms is populated per role:
//
//	"user"      — Text
//	"assistant" — Text and/or ToolCalls (a reply may carry prose alongside
//	              tool requests)
//	"tool"      — ToolResult (one message per completed call)
//
// Adapters translate this vendor-neutral shape into their wire format
// (Anthropic packs tool results into a user-role content block; OpenAI
// uses a dedicated "tool" role — callers never need to know).
type ChatMessage struct {
	Role       string
	Text       string
	ToolCalls  []ToolCall
	ToolResult *ToolResult
}

// ToolCall is a model-requested tool invocation. ID is vendor-issued
// (Anthropic "toolu_…", OpenAI "call_…") and must be echoed back in the
// matching ToolResult — IDs are NOT portable across vendors, which is why
// a conversation must stay on the provider it started with.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult carries the outcome of one ToolCall back to the model.
// Content must already be sanitized by the caller (redaction, size caps) —
// adapters forward it verbatim.
type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// ChatInput is one model round-trip request: the full running conversation
// plus the tool definitions in effect. Messages is the caller-owned
// history; adapters must not mutate it.
type ChatInput struct {
	System    string
	Messages  []ChatMessage
	Tools     []ToolSpec
	MaxTokens int
}

// ChatResult is the assistant's reply for one round-trip. When ToolCalls
// is non-empty the model wants tools executed and the conversation is not
// finished; an empty ToolCalls with Text is a final answer.
type ChatResult struct {
	Text       string
	ToolCalls  []ToolCall
	Model      string
	TokensUsed int
	// StopReason is the vendor stop reason normalized to
	// "tool_use" | "end_turn" | "max_tokens" (unknown values pass through).
	StopReason string
}
