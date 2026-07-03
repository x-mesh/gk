package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

// decodeNvidiaChatWire reads a recorded request body into chatRequest for
// wire-shape assertions.
func decodeNvidiaChatWire(t *testing.T, call FakeHTTPCall) chatRequest {
	t.Helper()
	b, err := io.ReadAll(call.Req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var req chatRequest
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("unmarshal chat request: %v", err)
	}
	return req
}

// A tool_calls response surfaces as ChatResult.ToolCalls with the
// arguments string re-exposed as raw JSON input.
func TestNvidiaChatToolCallsRoundTrip(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model: "test-model",
			Choices: []chatChoice{{
				Message: chatMessage{
					Role: "assistant",
					ToolCalls: []chatToolCall{{
						ID:       "call_01",
						Type:     "function",
						Function: chatToolCallFunction{Name: "git_log", Arguments: `{"limit":5}`},
					}},
				},
				FinishReason: "tool_calls",
			}},
			Usage: &chatUsage{TotalTokens: 42},
		})},
	}
	n := newTestNvidia(client, "test-key")

	res, err := n.ChatWithTools(context.Background(), ChatInput{
		System:   "you are a git chat",
		Messages: []ChatMessage{{Role: "user", Text: "what changed?"}},
		Tools: []ToolSpec{{
			Name:        "git_log",
			Description: "list commits",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_01" || tc.Name != "git_log" || string(tc.Input) != `{"limit":5}` {
		t.Errorf("ToolCall = %+v", tc)
	}
	if res.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use (normalized from tool_calls)", res.StopReason)
	}
	if res.TokensUsed != 42 {
		t.Errorf("TokensUsed = %d, want 42", res.TokensUsed)
	}

	req := decodeNvidiaChatWire(t, client.Calls[0])
	if len(req.Tools) != 1 || req.Tools[0].Type != "function" || req.Tools[0].Function.Name != "git_log" {
		t.Errorf("request Tools = %+v", req.Tools)
	}
	// System prompt leads as a system-role message, then the user turn.
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("request Messages = %+v", req.Messages)
	}
}

// Tool results travel as role:"tool" messages keyed by tool_call_id, and
// the assistant turn echoes its tool_calls with a JSON-string arguments.
func TestNvidiaChatToolResultWireShape(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model: "test-model",
			Choices: []chatChoice{{
				Message:      chatMessage{Role: "assistant", Content: "final answer"},
				FinishReason: "stop",
			}},
		})},
	}
	n := newTestNvidia(client, "test-key")

	history := []ChatMessage{
		{Role: "user", Text: "read a file"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_a", Name: "file_read", Input: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: "tool", ToolResult: &ToolResult{ToolCallID: "call_a", Content: "package a"}},
	}
	res, err := n.ChatWithTools(context.Background(), ChatInput{Messages: history})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if res.Text != "final answer" || res.StopReason != "end_turn" {
		t.Errorf("result = %+v, want final answer / end_turn", res)
	}

	req := decodeNvidiaChatWire(t, client.Calls[0])
	if len(req.Messages) != 3 {
		t.Fatalf("wire messages = %d, want 3: %+v", len(req.Messages), req.Messages)
	}
	asst := req.Messages[1]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("assistant tool_calls = %+v", asst.ToolCalls)
	}
	tool := req.Messages[2]
	if tool.Role != "tool" || tool.ToolCallID != "call_a" || tool.Content != "package a" {
		t.Errorf("tool message = %+v", tool)
	}
}

// An empty tool result must not vanish under omitempty — the adapter
// substitutes a placeholder so the API sees content.
func TestNvidiaChatEmptyToolResultPlaceholder(t *testing.T) {
	msgs, err := openAIChatMessages("", []ChatMessage{
		{Role: "user", Text: "q"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "git_log"}}},
		{Role: "tool", ToolResult: &ToolResult{ToolCallID: "c1", Content: ""}},
	})
	if err != nil {
		t.Fatalf("openAIChatMessages: %v", err)
	}
	if msgs[2].Content == "" {
		t.Error("empty tool result content must get a placeholder")
	}
}

// finish_reason normalization matches the vendor-neutral vocabulary.
func TestNvidiaChatStopReasonNormalization(t *testing.T) {
	cases := map[string]string{
		"tool_calls": "tool_use",
		"stop":       "end_turn",
		"length":     "max_tokens",
		"weird":      "weird",
	}
	for in, want := range cases {
		if got := normalizeOpenAIStop(in); got != want {
			t.Errorf("normalizeOpenAIStop(%q) = %q, want %q", in, got, want)
		}
	}
}

// Malformed history fails before HTTP; missing key is ErrUnauthenticated.
func TestNvidiaChatInputValidation(t *testing.T) {
	client := &FakeHTTPClient{}
	n := newTestNvidia(client, "test-key")
	if _, err := n.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "tool"}},
	}); err == nil {
		t.Error("want error for tool message without result")
	}
	if len(client.Calls) != 0 {
		t.Errorf("HTTP calls = %d, want 0", len(client.Calls))
	}

	n2 := newTestNvidia(&FakeHTTPClient{}, "")
	_, err := n2.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "user", Text: "hi"}},
	})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("err = %v, want ErrUnauthenticated", err)
	}
}

// OpenAI and Groq delegate ChatWithTools through their embedded Nvidia
// adapter — verify the delegation is wired (not just the assertion).
func TestOpenAIGroqChatDelegation(t *testing.T) {
	resp := func() *http.Response {
		return okResponse(chatResponse{
			Model: "m",
			Choices: []chatChoice{{
				Message:      chatMessage{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			}},
		})
	}
	o := NewOpenAI()
	o.APIKey = "k"
	o.Client = &FakeHTTPClient{Responses: []*http.Response{resp()}}
	o.nv = o.toNvidia()
	if _, err := o.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "user", Text: "hi"}},
	}); err != nil {
		t.Errorf("OpenAI delegation: %v", err)
	}

	g := NewGroq()
	g.APIKey = "k"
	g.Client = &FakeHTTPClient{Responses: []*http.Response{resp()}}
	g.nv = g.toNvidia()
	if _, err := g.ChatWithTools(context.Background(), ChatInput{
		Messages: []ChatMessage{{Role: "user", Text: "hi"}},
	}); err != nil {
		t.Errorf("Groq delegation: %v", err)
	}
}
