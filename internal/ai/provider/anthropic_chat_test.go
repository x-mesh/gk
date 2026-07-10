package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// decodeAnthropicChatRequest reads a recorded request body into the
// tool-capable chat request shape.
func decodeAnthropicChatRequest(t *testing.T, call FakeHTTPCall) anthropicChatRequest {
	t.Helper()
	b, err := io.ReadAll(call.Req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var req anthropicChatRequest
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("unmarshal chat request body: %v", err)
	}
	return req
}

// A tool_use response must surface as ChatResult.ToolCalls with the
// vendor-issued ID and raw input preserved, and the request must carry the
// tool definitions and system block.
func TestAnthropicChatToolUseRoundTrip(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:      "test-model",
			StopReason: "tool_use",
			Content: []anthropicContentBlock{
				{Type: "text", Text: "let me check"},
				{Type: "tool_use", ID: "toolu_01", Name: "git_log", Input: json.RawMessage(`{"limit":5}`)},
			},
			Usage: anthropicUsage{InputTokens: 10, OutputTokens: 5},
		})},
	}
	a := newTestAnthropic(client)

	res, err := a.ChatWithTools(context.Background(), ChatInput{
		System:   "you are a git chat",
		Messages: []ChatMessage{{Role: "user", Text: "what changed recently?"}},
		Tools: []ToolSpec{{
			Name:        "git_log",
			Description: "list commits",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if res.Text != "let me check" {
		t.Errorf("Text = %q, want %q", res.Text, "let me check")
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.ID != "toolu_01" || tc.Name != "git_log" || string(tc.Input) != `{"limit":5}` {
		t.Errorf("ToolCall = %+v, want id=toolu_01 name=git_log input={\"limit\":5}", tc)
	}
	if res.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", res.StopReason)
	}

	req := decodeAnthropicChatRequest(t, client.Calls[0])
	if len(req.Tools) != 1 || req.Tools[0].Name != "git_log" {
		t.Errorf("request Tools = %+v, want git_log", req.Tools)
	}
	if len(req.System) != 1 || req.System[0].Text != "you are a git chat" {
		t.Errorf("request System = %+v", req.System)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("request Messages = %+v", req.Messages)
	}
}

// Tool results ride back as tool_result blocks inside a user-role message,
// and consecutive tool messages coalesce into ONE user message (the
// Messages API rejects split results for a single assistant turn).
func TestAnthropicChatToolResultCoalescing(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:      "test-model",
			StopReason: "end_turn",
			Content:    []anthropicContentBlock{{Type: "text", Text: "final answer"}},
		})},
	}
	a := newTestAnthropic(client)

	history := []ChatMessage{
		{Role: "user", Text: "compare two files"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "toolu_a", Name: "file_read", Input: json.RawMessage(`{"path":"a.go"}`)},
			{ID: "toolu_b", Name: "file_read", Input: json.RawMessage(`{"path":"b.go"}`)},
		}},
		{Role: "tool", ToolResult: &ToolResult{ToolCallID: "toolu_a", Content: "package a"}},
		{Role: "tool", ToolResult: &ToolResult{ToolCallID: "toolu_b", Content: "package b", IsError: false}},
	}
	res, err := a.ChatWithTools(context.Background(), ChatInput{Messages: history})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if res.Text != "final answer" || len(res.ToolCalls) != 0 {
		t.Errorf("result = %+v, want final answer with no tool calls", res)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", res.StopReason)
	}

	req := decodeAnthropicChatRequest(t, client.Calls[0])
	if len(req.Messages) != 3 {
		t.Fatalf("wire messages = %d, want 3 (user, assistant, coalesced user): %+v", len(req.Messages), req.Messages)
	}
	last := req.Messages[2]
	if last.Role != "user" {
		t.Errorf("results message role = %q, want user", last.Role)
	}
	if len(last.Content) != 2 {
		t.Fatalf("coalesced tool_result blocks = %d, want 2", len(last.Content))
	}
	for i, want := range []string{"toolu_a", "toolu_b"} {
		if last.Content[i].Type != "tool_result" || last.Content[i].ToolUseID != want {
			t.Errorf("block[%d] = %+v, want tool_result for %s", i, last.Content[i], want)
		}
	}
	// The assistant turn keeps its tool_use blocks with empty-input default.
	asst := req.Messages[1]
	if asst.Role != "assistant" || len(asst.Content) != 2 || asst.Content[0].Type != "tool_use" {
		t.Errorf("assistant wire message = %+v", asst)
	}
}

// stop_sequence normalizes to end_turn; unknown reasons pass through.
func TestAnthropicChatStopReasonNormalization(t *testing.T) {
	if got := normalizeAnthropicStop("stop_sequence"); got != "end_turn" {
		t.Errorf("stop_sequence → %q, want end_turn", got)
	}
	if got := normalizeAnthropicStop("max_tokens"); got != "max_tokens" {
		t.Errorf("max_tokens → %q, want max_tokens", got)
	}
}

// A malformed history (tool message without a result) fails before any
// HTTP call — shape errors are caller bugs, not provider errors.
func TestAnthropicChatRejectsMalformedHistory(t *testing.T) {
	client := &FakeHTTPClient{}
	a := newTestAnthropic(client)
	_, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "tool"}},
	})
	if err == nil {
		t.Fatal("want error for tool message without result")
	}
	if len(client.Calls) != 0 {
		t.Errorf("HTTP calls = %d, want 0", len(client.Calls))
	}
}

// Missing API key surfaces ErrUnauthenticated without an HTTP call, same
// as every other Anthropic capability.
func TestAnthropicChatUnauthenticated(t *testing.T) {
	a := newTestAnthropic(&FakeHTTPClient{})
	a.APIKey = ""
	_, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "user", Text: "hi"}},
	})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("err = %v, want ErrUnauthenticated", err)
	}
}

// An empty response (no text, no tool calls) is a provider error — the
// engine must never spin on a contentless reply.
func TestAnthropicChatEmptyContentIsError(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:   "test-model",
			Content: []anthropicContentBlock{},
		})},
	}
	a := newTestAnthropic(client)
	_, err := a.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "user", Text: "hi"}},
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("err = %v, want ErrProviderResponse", err)
	}
}

// TestAnthropicChatMessagesRejectsAssistantFirst pins the invariant the
// Messages API enforces and /compact once violated: a history that opens
// with an assistant turn must fail loudly here, not as an opaque 400 from
// the API after the request has already been paid for.
func TestAnthropicChatMessagesRejectsAssistantFirst(t *testing.T) {
	_, err := anthropicChatMessages([]ChatMessage{
		{Role: "assistant", Text: "summary of earlier conversation"},
		{Role: "user", Text: "and then?"},
	})
	if err == nil {
		t.Fatal("assistant-first history accepted, want an error")
	}
	if !strings.Contains(err.Error(), "first message") {
		t.Errorf("error = %v, want it to name the first-message constraint", err)
	}
}

// A user-first history — the only shape gk chat may produce — still
// converts cleanly, including the compacted intro+summary pair.
func TestAnthropicChatMessagesAcceptsCompactedShape(t *testing.T) {
	msgs, err := anthropicChatMessages([]ChatMessage{
		{Role: "user", Text: "[/compact] Summarize our conversation so far"},
		{Role: "assistant", Text: "digest"},
		{Role: "user", Text: "next question"},
	})
	if err != nil {
		t.Fatalf("compacted history rejected: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Role != "user" {
		t.Errorf("messages = %+v, want 3 messages starting with user", msgs)
	}
}
